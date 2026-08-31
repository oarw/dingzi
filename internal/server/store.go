package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oarw/dingzi/internal/proto"
)

// Sample write batching.
//
// SQLite permits one writer at a time. N agents writing directly would contend
// on the write lock, and a busy_timeout does not fix that — it converts
// contention into retries, and a retry that runs out of patience is a lost
// sample. So every sample goes through one goroutine that batches them, and one
// batch is one transaction: one fsync instead of hundreds.
const (
	writeQueue = 4096
	batchMax   = 256
	batchEvery = 2 * time.Second
)

// Store is the panel's persistence.
type Store struct {
	db  *sql.DB
	log *slog.Logger

	writeC chan queued
	done   chan struct{}
	wg     sync.WaitGroup

	warnOnce sync.Once
}

// queued pairs a sample with the machine it belongs to.
type queued struct {
	id     int64
	sample Sample
}

// OpenStore opens or creates the database.
func OpenStore(path string, log *slog.Logger) (*Store, error) {
	// WAL so a reader never blocks the writer, which is what lets the live view
	// stay responsive while samples are being committed.
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	s := &Store{
		db:     db,
		log:    log,
		writeC: make(chan queued, writeQueue),
		done:   make(chan struct{}),
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS servers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid          TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    host          TEXT    NOT NULL DEFAULT '{}',
    version       TEXT    NOT NULL DEFAULT '',
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL,
    cycle_start   INTEGER NOT NULL DEFAULT 0,
    in_bytes      INTEGER NOT NULL DEFAULT 0,
    out_bytes     INTEGER NOT NULL DEFAULT 0,
    quota         INTEGER NOT NULL DEFAULT 0,
    reset_day     INTEGER NOT NULL DEFAULT 1,
    count_mode    TEXT    NOT NULL DEFAULT 'sum'
);

CREATE TABLE IF NOT EXISTS samples (
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    at        INTEGER NOT NULL,
    cpu       REAL    NOT NULL,
    mem_used  INTEGER NOT NULL,
    swap_used INTEGER NOT NULL,
    disk_used INTEGER NOT NULL,
    net_in    INTEGER NOT NULL,
    net_out   INTEGER NOT NULL,
    load1     REAL    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_samples_server_at ON samples(server_id, at);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close drains pending writes and closes the database.
func (s *Store) Close() error {
	close(s.done)
	s.wg.Wait()
	return s.db.Close()
}

// marshalHost encodes static facts for storage.
func marshalHost(h proto.Host) string {
	raw, err := json.Marshal(h)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// Register creates or refreshes a machine and returns it.
func (s *Store) Register(hello proto.Hello, now time.Time) (*Machine, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name := hello.Name
	if name == "" {
		name = hello.Host.Hostname
	}
	if name == "" {
		name = hello.UUID[:8]
	}

	// The UPDATE deliberately omits name. A machine renamed in the panel must
	// keep that name across reconnects; letting the agent's flag win would make
	// the rename appear to work and then silently revert.
	const upsert = `
INSERT INTO servers (uuid, name, host, version, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(uuid) DO UPDATE SET
    host = excluded.host,
    version = excluded.version,
    last_seen = excluded.last_seen`

	sec := now.Unix()
	if _, err := s.db.ExecContext(ctx, upsert, hello.UUID, name,
		marshalHost(hello.Host), hello.Version, sec, sec); err != nil {
		return nil, fmt.Errorf("register %s: %w", hello.UUID, err)
	}
	return s.machineByUUID(ctx, hello.UUID)
}

// QueueSample hands a sample to the writer.
//
// It never blocks. Stalling an agent's read loop to wait for disk would turn
// slow storage into a fleet-wide outage: every agent behind the same writer
// would miss its heartbeat and be marked offline. Losing a sample costs one
// point on a graph; blocking costs the live view.
func (s *Store) QueueSample(id int64, sm Sample) {
	select {
	case s.writeC <- queued{id: id, sample: sm}:
	default:
		// Warn once. A per-drop log would itself become the load problem the
		// drop is trying to shed.
		s.warnOnce.Do(func() {
			s.log.Warn("sample write queue is full, dropping samples — " +
				"history will have gaps, the live view is unaffected")
		})
	}
}

// writer batches queued samples into transactions.
func (s *Store) writer() {
	defer s.wg.Done()

	batch := make([]queued, 0, batchMax)
	t := time.NewTicker(batchEvery)
	defer t.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.insertBatch(batch); err != nil {
			s.log.Error("writing samples failed", slog.Any("error", err))
		}
		batch = batch[:0]
	}

	for {
		select {
		case q := <-s.writeC:
			batch = append(batch, q)
			if len(batch) >= batchMax {
				flush()
			}
		case <-t.C:
			flush()
		case <-s.done:
			// Drain what is already queued so a clean shutdown does not throw
			// away the last two seconds.
			for {
				select {
				case q := <-s.writeC:
					batch = append(batch, q)
					if len(batch) >= batchMax {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// insertBatch writes one transaction.
func (s *Store) insertBatch(batch []queued) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO samples (server_id, at, cpu, mem_used, swap_used, disk_used, net_in, net_out, load1)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, q := range batch {
		st := q.sample.State
		if _, err := stmt.ExecContext(ctx, q.id, q.sample.At.Unix(),
			st.CPU, st.MemUsed, st.SwapUsed, st.DiskUsed,
			st.NetInTransfer, st.NetOutTransfer, st.Load1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
