package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

// ErrNoMachine reports a machine id that does not exist.
var ErrNoMachine = errors.New("no such machine")

const machineCols = `id, uuid, name, host, version, first_seen, last_seen,
	cycle_start, in_bytes, out_bytes, quota, reset_day, count_mode, last_raw_in, last_raw_out, has_raw`

// scanMachine reads one row.
func scanMachine(scan func(...any) error) (*Machine, error) {
	var (
		m           Machine
		hostJSON    string
		first, last int64
		cycle       int64
	)
	err := scan(&m.ID, &m.UUID, &m.Name, &hostJSON, &m.Version, &first, &last,
		&cycle, &m.Traffic.InBytes, &m.Traffic.OutBytes,
		&m.Traffic.Quota, &m.Traffic.ResetDay, &m.Traffic.CountMode,
		&m.Traffic.LastRawIn, &m.Traffic.LastRawOut, &m.Traffic.HasRaw)
	if err != nil {
		return nil, err
	}
	m.FirstSeen = time.Unix(first, 0)
	m.LastSeen = time.Unix(last, 0)
	if cycle > 0 {
		m.Traffic.CycleStart = time.Unix(cycle, 0)
	}
	// Unreadable host JSON still yields a usable machine. The panel showing
	// blanks for kernel and CPU model beats the machine vanishing from the list.
	_ = json.Unmarshal([]byte(hostJSON), &m.Host)
	return &m, nil
}

func (s *Store) machineByUUID(ctx context.Context, uuid string) (*Machine, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+machineCols+` FROM servers WHERE uuid = ?`, uuid)
	m, err := scanMachine(row.Scan)
	if err != nil {
		return nil, fmt.Errorf("loading machine %s: %w", uuid, err)
	}
	return m, nil
}

// Machines loads the whole fleet.
func (s *Store) Machines(ctx context.Context) ([]*Machine, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+machineCols+` FROM servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Machine
	for rows.Next() {
		m, err := scanMachine(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveTraffic persists the cycle counters.
//
// Called on a timer rather than per sample: these are counters that only need
// to survive a restart, and writing them every second would multiply the write
// load for no gain.
func (s *Store) SaveTraffic(ctx context.Context, snap map[int64]trafficSnapshot) error {
	if len(snap) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
UPDATE servers SET cycle_start = ?, in_bytes = ?, out_bytes = ?, last_seen = ?,
 last_raw_in = ?, last_raw_out = ?, has_raw = ?
WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for id, t := range snap {
		var cycle int64
		if !t.CycleStart.IsZero() {
			cycle = t.CycleStart.Unix()
		}
		if _, err := stmt.ExecContext(ctx, cycle, t.InBytes, t.OutBytes, t.LastSeen.Unix(),
			t.LastRawIn, t.LastRawOut, t.HasRaw, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateHost persists changed static facts.
func (s *Store) UpdateHost(ctx context.Context, id int64, h proto.Host) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE servers SET host = ? WHERE id = ?`, marshalHost(h), id)
	return err
}

// Rename changes a machine's display name.
func (s *Store) Rename(ctx context.Context, id int64, name string) error {
	return s.affectOne(ctx, `UPDATE servers SET name = ? WHERE id = ?`, name, id)
}

// SetQuota changes the quota configuration.
func (s *Store) SetQuota(
	ctx context.Context, id int64, quota uint64, resetDay int, mode string,
	cycleStart time.Time,
) error {
	var cycle int64
	if !cycleStart.IsZero() {
		cycle = cycleStart.Unix()
	}
	return s.affectOne(ctx,
		`UPDATE servers SET quota = ?, reset_day = ?, count_mode = ?, cycle_start = ?
		 WHERE id = ?`, quota, resetDay, mode, cycle, id)
}

// DeleteMachine removes a machine and its samples.
func (s *Store) DeleteMachine(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var uuid string
	if err := tx.QueryRowContext(ctx, "SELECT uuid FROM servers WHERE id=?", id).Scan(&uuid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoMachine
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_credentials(uuid,token_hash,revoked) VALUES(?,'',1)
 ON CONFLICT(uuid) DO UPDATE SET revoked=1`, uuid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM servers WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) affectOne(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNoMachine
	}
	return nil
}

// HistoryPoint is one aggregated bucket.
type HistoryPoint struct {
	At          int64    `json:"at"`
	CPU         float64  `json:"cpu"`
	MemUsed     uint64   `json:"mem_used"`
	SwapUsed    uint64   `json:"swap_used"`
	DiskUsed    uint64   `json:"disk_used"`
	Load1       float64  `json:"load1"`
	Mem         *float64 `json:"mem"`
	Swap        *float64 `json:"swap"`
	Disk        *float64 `json:"disk"`
	NetInSpeed  *float64 `json:"net_in_speed"`
	NetOutSpeed *float64 `json:"net_out_speed"`
}

// History returns downsampled history for one machine.
//
// The aggregation happens in SQL. A day of one-second samples is 86,400 rows
// per machine, and pulling those into memory to produce 200 chart points is the
// query that makes a panel fall over once it has real history in it.
func (s *Store) History(
	ctx context.Context, id int64, since time.Time, buckets int,
) ([]HistoryPoint, error) {
	if buckets < 1 {
		buckets = 200
	}
	if buckets > 1000 {
		buckets = 1000
	}
	span := time.Since(since).Seconds()
	width := (int64(span) + int64(buckets) - 1) / int64(buckets)
	if width < 1 {
		width = 1
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT MIN(at) AS bucket_at,
       AVG(cpu), AVG(mem_used), AVG(swap_used), AVG(disk_used), AVG(load1),
       AVG(CASE WHEN mem_total > 0 THEN MIN(100, 100.0 * mem_used / mem_total) END),
       AVG(CASE WHEN swap_total > 0 THEN MIN(100, 100.0 * swap_used / swap_total)
                WHEN mem_total > 0 THEN 0 END),
       AVG(CASE WHEN disk_total > 0 THEN MIN(100, 100.0 * disk_used / disk_total) END),
       AVG(net_in_speed), AVG(net_out_speed)
FROM samples
WHERE server_id = ? AND at >= ? AND at <= ?
GROUP BY (at - ?) / ?
ORDER BY bucket_at`, id, since.Unix(), time.Now().Unix(), since.Unix(), width)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]HistoryPoint, 0, buckets)
	for rows.Next() {
		var (
			p               HistoryPoint
			mem, swap, disk float64
		)
		if err := rows.Scan(&p.At, &p.CPU, &mem, &swap, &disk, &p.Load1,
			&p.Mem, &p.Swap, &p.Disk, &p.NetInSpeed, &p.NetOutSpeed); err != nil {
			return nil, err
		}
		p.CPU = round2(p.CPU)
		p.MemUsed, p.SwapUsed, p.DiskUsed = uint64(mem), uint64(swap), uint64(disk)
		p.Load1 = round2(p.Load1)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Prune deletes samples older than the retention window.
func (s *Store) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	cutoff := time.Now().Add(-keep).Unix()
	var total int64
	for _, table := range []string{"samples", "monitor_results", "alert_events"} {
		for {
			res, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid IN (SELECT rowid FROM "+table+" WHERE at < ? LIMIT 10000)", cutoff)
			if err != nil {
				return total, err
			}
			n, err := res.RowsAffected()
			total += n
			if err != nil {
				return total, err
			}
			if n < 10000 {
				break
			}
		}
	}
	return total, nil
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
