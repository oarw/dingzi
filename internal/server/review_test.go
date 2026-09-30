package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

// Configuration transactions read before writing. A concurrent sample flush
// must not invalidate that read snapshot and make the configuration fail.
func TestWriteTransactionSurvivesCompetingWriter(t *testing.T) {
	s, _ := testPanel(t)
	m := testMachine(t, s)
	ctx := context.Background()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM servers WHERE id=?", m.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	other, err := s.store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ExecContext(ctx, "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	_, competingErr := other.ExecContext(ctx, "UPDATE servers SET version='sample-writer' WHERE id=?", m.ID)
	if competingErr != nil && !strings.Contains(competingErr.Error(), "SQLITE_BUSY") {
		t.Fatal(competingErr)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE servers SET name='configured' WHERE id=?", m.ID); err != nil {
		t.Fatalf("configuration lost its write transaction after reading: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if competingErr != nil {
		if _, err := other.ExecContext(ctx, "UPDATE servers SET version='sample-writer' WHERE id=?", m.ID); err != nil {
			t.Fatalf("competing writer could not proceed after commit: %v", err)
		}
	}
	var version string
	if err := other.QueryRowContext(ctx, "SELECT name,version FROM servers WHERE id=?", m.ID).Scan(&name, &version); err != nil {
		t.Fatal(err)
	}
	if name != "configured" || version != "sample-writer" {
		t.Fatalf("lost writes: name=%q version=%q", name, version)
	}
}

func TestOnlineStatusHonorsIntervalAndFirstSampleDeadline(t *testing.T) {
	for _, interval := range []float64{1, 30} {
		t.Run(time.Duration(interval*float64(time.Second)).String(), func(t *testing.T) {
			base, _ := testPanel(t)
			s, err := New(Options{Interval: interval}, base.store, base.log)
			if err != nil {
				t.Fatal(err)
			}
			m := testMachine(t, s)
			s.hub.Attach(m.ID, &agentConn{})
			now := time.Now()
			deadline := max(30*time.Second, time.Duration(3*interval*float64(time.Second)))
			if v, _ := s.hub.View(m.ID, now.Add(deadline+time.Second)); v.Online {
				t.Fatal("agent without any samples stayed online past its deadline")
			}
			s.hub.Push(m.ID, proto.State{}, now)
			if v, _ := s.hub.View(m.ID, now.Add(deadline-time.Second)); !v.Online {
				t.Fatal("healthy agent marked offline before its report deadline")
			}
			if v, _ := s.hub.View(m.ID, now.Add(deadline+time.Second)); v.Online {
				t.Fatal("stale samples kept agent online")
			}
			if views := s.hub.Snapshot(now.Add(deadline + time.Second)); len(views) != 1 || views[0].Online {
				t.Fatal("fleet and single-machine online status disagree")
			}
		})
	}
}

func TestCycleStartUsesUTC(t *testing.T) {
	for _, tc := range []struct{ at, want string }{
		{"2026-09-01T00:30:00+08:00", "2026-08-01T00:00:00Z"},
		{"2026-08-31T20:30:00-07:00", "2026-09-01T00:00:00Z"},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := CycleStart(at, 1).Format(time.RFC3339); got != tc.want {
			t.Errorf("CycleStart(%s) = %s, want %s", tc.at, got, tc.want)
		}
	}
}

func TestReconnectCannotReviveStaleSamples(t *testing.T) {
	s, _ := testPanel(t)
	m := testMachine(t, s)
	now := time.Now()
	s.hub.Push(m.ID, proto.State{CPU: 99}, now.Add(-time.Minute))
	s.hub.Attach(m.ID, &agentConn{})
	if v, _ := s.hub.View(m.ID, now); v.Online {
		t.Fatal("reconnecting made an expired sample appear live before any new report")
	}
	if views := s.hub.Snapshot(now); len(views) != 1 || views[0].Online {
		t.Fatal("fleet revived an expired sample on reconnect")
	}
	s.hub.Push(m.ID, proto.State{}, now)
	if v, _ := s.hub.View(m.ID, now); !v.Online {
		t.Fatal("fresh report did not restore online status")
	}
}
