package server

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

func TestLoginThrottleUsesTransportPeer(t *testing.T) {
	s, _ := testPanel(t)
	for i, header := range []string{"192.0.2.1", "192.0.2.2, 192.0.2.3"} {
		r := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("X-Forwarded-For", header)
		r.Header.Set("X-Real-IP", header)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if i > 0 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("attempt %d: status %d, want %d", i, w.Code, want)
		}
	}
}

func TestSessionChannelsEndWithLogin(t *testing.T) {
	for _, end := range []string{"logout", "expiry", "eviction"} {
		t.Run(end, func(t *testing.T) {
			s := newSessionStore()
			defer s.close()
			tok, err := s.issue()
			if err != nil {
				t.Fatal(err)
			}
			if end == "expiry" {
				s.mu.Lock()
				s.tokens[tok].expires = time.Now().Add(25 * time.Millisecond)
				s.mu.Unlock()
			}
			first, ok := s.sessionDone(tok)
			if !ok {
				t.Fatal("new login cannot open channel")
			}
			second, ok := s.sessionDone(tok)
			if !ok {
				t.Fatal("second channel rejected")
			}
			switch end {
			case "logout":
				s.revoke(tok)
			case "eviction":
				for i := 0; i < 256; i++ {
					if _, err := s.issue(); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, done := range []<-chan struct{}{first, second} {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("login ended but channel retained authority")
				}
			}
			if s.valid(tok) {
				t.Fatal("ended login remains valid")
			}
			if _, ok := s.sessionDone(tok); ok {
				t.Fatal("ended login opened a new channel")
			}
		})
	}
}

func TestLargeCountersDoNotDiscardOtherMachines(t *testing.T) {
	s, _ := testPanel(t)
	first := testMachine(t, s)
	second, err := s.register(proto.Hello{UUID: "second-machine", Name: "healthy"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	large := Sample{At: now, State: proto.State{
		MemUsed: math.MaxUint64, SwapUsed: math.MaxUint64, DiskUsed: math.MaxUint64,
		NetInTransfer: math.MaxUint64, NetOutTransfer: math.MaxUint64,
	}, MemTotal: math.MaxUint64, SwapTotal: math.MaxUint64, DiskTotal: math.MaxUint64,
		NetInSpeed: math.MaxUint64, NetOutSpeed: math.MaxUint64}
	healthy := Sample{At: now, State: proto.State{MemUsed: 42}}
	if err := s.store.insertBatch([]queued{{id: first.ID, sample: large}, {id: second.ID, sample: healthy}}); err != nil {
		t.Fatalf("mixed sample batch: %v", err)
	}
	var used int64
	if err := s.store.db.QueryRow("SELECT mem_used FROM samples WHERE server_id = ?", second.ID).Scan(&used); err != nil || used != 42 {
		t.Fatalf("healthy sample lost: used=%d err=%v", used, err)
	}
	if err := s.store.db.QueryRow("SELECT mem_used FROM samples WHERE server_id = ?", first.ID).Scan(&used); err != nil || used != math.MaxInt64 {
		t.Fatalf("large sample not bounded: used=%d err=%v", used, err)
	}
	snap := map[int64]trafficSnapshot{
		first.ID:  {Traffic: Traffic{InBytes: math.MaxUint64, OutBytes: math.MaxUint64, LastRawIn: math.MaxUint64, LastRawOut: math.MaxUint64}, LastSeen: now},
		second.ID: {Traffic: Traffic{InBytes: 123, OutBytes: 456}, LastSeen: now},
	}
	if err := s.store.SaveTraffic(context.Background(), snap); err != nil {
		t.Fatalf("mixed traffic snapshot: %v", err)
	}
	var in, out int64
	if err := s.store.db.QueryRow("SELECT in_bytes, out_bytes FROM servers WHERE id = ?", second.ID).Scan(&in, &out); err != nil || in != 123 || out != 456 {
		t.Fatalf("healthy traffic lost: %d/%d err=%v", in, out, err)
	}
	traffic := Traffic{HasRaw: true, InBytes: math.MaxInt64 - 1}
	traffic.Accumulate(math.MaxUint64, math.MaxUint64, now)
	traffic.Accumulate(0, 0, now)
	traffic.Accumulate(math.MaxUint64, math.MaxUint64, now)
	if traffic.InBytes != math.MaxInt64 || traffic.OutBytes != math.MaxInt64 {
		t.Fatalf("traffic overflowed instead of saturating: %+v", traffic)
	}
}
