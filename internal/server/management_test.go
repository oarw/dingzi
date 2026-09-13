package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

func testPanel(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(Options{AgentSecret: "test-registration-secret"}, st, log)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.sessions.issue()
	if err != nil {
		t.Fatal(err)
	}
	return s, &http.Cookie{Name: sessionCookie, Value: token}
}

func testMachine(t *testing.T, s *Server) *Machine {
	t.Helper()
	m, err := s.register(proto.Hello{UUID: "test-machine-uuid", Name: "original",
		Host: proto.Host{MemTotal: 1000, SwapTotal: 500, DiskTotal: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func requestJSON(s *Server, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestMachinePatchIsAtomic(t *testing.T) {
	s, cookie := testPanel(t)
	testMachine(t, s)
	w := requestJSON(s, cookie, "PATCH", "/api/v1/servers/1", `{"name":"changed","reset_day":32}`)
	if w.Code != 400 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	m, _ := s.hub.View(1, time.Now())
	stored, err := s.store.Machines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "original" || stored[0].Name != "original" {
		t.Fatal("invalid request partly saved")
	}
	if w := requestJSON(s, nil, "PATCH", "/api/v1/servers/1", `{"name":"changed"}`); w.Code != 401 {
		t.Fatalf("unauthenticated write: %d", w.Code)
	}
	w = requestJSON(s, cookie, "PATCH", "/api/v1/servers/1", `{"name":"changed","quota":100000,"count_mode":"out"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestHistoryRatiosAndRates(t *testing.T) {
	s, _ := testPanel(t)
	m := testMachine(t, s)
	now := time.Now().Add(-time.Minute)
	sm, _ := s.hub.Push(m.ID, proto.State{CPU: 15, MemUsed: 250, SwapUsed: 100, DiskUsed: 2000}, now)
	sm.NetInSpeed, sm.NetOutSpeed = 1024, 2048
	if err := s.store.insertBatch([]queued{{id: m.ID, sample: sm}}); err != nil {
		t.Fatal(err)
	}
	w := requestJSON(s, nil, "GET", "/api/v1/servers/1/history", "")
	var result struct {
		Points []HistoryPoint `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("history: %s", w.Body)
	}
	p := result.Points[0]
	if p.Mem == nil || *p.Mem != 25 || p.NetOutSpeed == nil || *p.NetOutSpeed != 2048 {
		t.Fatalf("history: %+v", p)
	}
	if w := requestJSON(s, nil, "GET", "/api/v1/servers/999/history", ""); w.Code != 404 {
		t.Fatalf("missing machine returned %d", w.Code)
	}
}

func TestTrafficSurvivesRestartWithoutMovingLastSeen(t *testing.T) {
	s, _ := testPanel(t)
	m := testMachine(t, s)
	at := time.Now().Add(-time.Hour)
	s.hub.Push(m.ID, proto.State{NetInTransfer: 100, NetOutTransfer: 200}, at)
	s.hub.Push(m.ID, proto.State{NetInTransfer: 150, NetOutTransfer: 250}, at.Add(time.Second))
	s.persistTraffic(context.Background())
	machines, err := s.store.Machines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := machines[0]
	if r.LastSeen.Unix() != at.Add(time.Second).Unix() {
		t.Fatal("persistence moved last_seen")
	}
	r.Traffic.Accumulate(200, 300, at.Add(2*time.Second))
	if r.Traffic.InBytes != 100 || r.Traffic.OutBytes != 100 {
		t.Fatalf("lost baseline: %+v", r.Traffic)
	}
}

func TestCycleStartMonthEnds(t *testing.T) {
	for _, tc := range []struct {
		now, want string
		day       int
	}{
		{"2026-03-30", "2026-02-28", 31},
		{"2024-03-01", "2024-02-29", 31},
		{"2026-01-01", "2025-12-15", 15},
		{"2026-02-28", "2026-02-28", 31},
	} {
		now, _ := time.Parse("2006-01-02", tc.now)
		if got := CycleStart(now, tc.day).Format("2006-01-02"); got != tc.want {
			t.Errorf("%s day %d = %s, want %s", tc.now, tc.day, got, tc.want)
		}
	}
}

func TestDeletedMachineDoesNotDiscardOtherSamples(t *testing.T) {
	s, _ := testPanel(t)
	m := testMachine(t, s)
	if err := s.store.insertBatch([]queued{
		{id: 999, sample: Sample{At: time.Now()}},
		{id: m.ID, sample: Sample{At: time.Now()}},
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM samples").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d samples", count)
	}
}
