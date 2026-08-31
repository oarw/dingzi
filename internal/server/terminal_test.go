package server

import (
	"errors"
	"testing"
	"time"
)

func TestReserveMintsDistinctTokens(t *testing.T) {
	tr := newTerminalRegistry()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		tok, _, err := tr.reserve(1)
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		if seen[tok] {
			t.Fatalf("reserve produced a duplicate token %q", tok)
		}
		if len(tok) < 40 {
			t.Errorf("token %q is shorter than expected for 32 random bytes", tok)
		}
		seen[tok] = true
		tr.release(tok, 1)
	}
}

// A token must work once. A second claim with the same token is a replay, and
// the whole point of a single-use credential is that it fails.
func TestClaimIsSingleUse(t *testing.T) {
	tr := newTerminalRegistry()
	tok, want, err := tr.reserve(7)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	got, ok := tr.claim(tok)
	if !ok {
		t.Fatal("first claim failed")
	}
	if got != want {
		t.Error("claim returned a different pending session than reserve created")
	}
	if _, ok := tr.claim(tok); ok {
		t.Error("second claim of the same token succeeded — token is replayable")
	}
}

func TestClaimRejectsUnknownToken(t *testing.T) {
	tr := newTerminalRegistry()
	if _, ok := tr.claim("never-issued"); ok {
		t.Error("claim accepted a token that was never issued")
	}
}

func TestClaimRejectsExpiredToken(t *testing.T) {
	tr := newTerminalRegistry()
	tok, pend, err := tr.reserve(1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Reach in rather than sleeping for the real TTL.
	pend.created = time.Now().Add(-terminalTokenTTL - time.Second)

	if _, ok := tr.claim(tok); ok {
		t.Error("claim accepted an expired token")
	}
	// The slot must come back, or an agent that dials back late would cost the
	// machine a session slot permanently.
	if tr.total != 0 {
		t.Errorf("total = %d after an expired claim, want 0", tr.total)
	}
}

func TestPerMachineCap(t *testing.T) {
	tr := newTerminalRegistry()
	var tokens []string
	for i := 0; i < maxTerminalsPerMachine; i++ {
		tok, _, err := tr.reserve(1)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		tokens = append(tokens, tok)
	}
	_, _, err := tr.reserve(1)
	if !errors.Is(err, errTooManyTerminalsHere) {
		t.Fatalf("reserve past the per-machine cap: err = %v, want %v",
			err, errTooManyTerminalsHere)
	}
	// A different machine still has room: the cap is per machine, not global at
	// this level.
	if _, _, err := tr.reserve(2); err != nil {
		t.Errorf("reserve on a second machine: %v", err)
	}

	tr.release(tokens[0], 1)
	if _, _, err := tr.reserve(1); err != nil {
		t.Errorf("reserve after release: %v", err)
	}
}

func TestGlobalCap(t *testing.T) {
	tr := newTerminalRegistry()
	// Spread across machines so the per-machine cap is not what stops us.
	machines := int64(maxTerminalsTotal/maxTerminalsPerMachine + 1)
	made := 0
	for m := int64(1); m <= machines && made < maxTerminalsTotal; m++ {
		for i := 0; i < maxTerminalsPerMachine && made < maxTerminalsTotal; i++ {
			if _, _, err := tr.reserve(m); err != nil {
				t.Fatalf("reserve on machine %d: %v", m, err)
			}
			made++
		}
	}
	if _, _, err := tr.reserve(machines + 1); !errors.Is(err, errTooManyTerminals) {
		t.Fatalf("reserve past the global cap: err = %v, want %v",
			err, errTooManyTerminals)
	}
}

// The counters must return to zero. A leak here shows up as a panel that
// gradually refuses to open terminals until it is restarted, which is a
// miserable thing to diagnose.
func TestCountersReturnToZero(t *testing.T) {
	tr := newTerminalRegistry()

	// Path 1: reserved then released without ever being claimed.
	tok, _, err := tr.reserve(1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	tr.release(tok, 1)

	// Path 2: reserved, claimed, then released when the session ended.
	tok2, _, err := tr.reserve(1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, ok := tr.claim(tok2); !ok {
		t.Fatal("claim failed")
	}
	tr.release(tok2, 1)

	if tr.total != 0 {
		t.Errorf("total = %d, want 0", tr.total)
	}
	if len(tr.perMachine) != 0 {
		t.Errorf("perMachine = %v, want empty", tr.perMachine)
	}
	if len(tr.pending) != 0 {
		t.Errorf("pending = %v, want empty", tr.pending)
	}
}

// release must not drive a counter negative, which would let the caps be
// bypassed by an unbalanced release.
func TestReleaseIsIdempotent(t *testing.T) {
	tr := newTerminalRegistry()
	tok, _, err := tr.reserve(1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	tr.release(tok, 1)
	tr.release(tok, 1)
	tr.release(tok, 1)

	if tr.total != 0 {
		t.Errorf("total = %d after repeated release, want 0", tr.total)
	}
	for i := 0; i < maxTerminalsPerMachine; i++ {
		if _, _, err := tr.reserve(1); err != nil {
			t.Fatalf("reserve %d after repeated release: %v", i, err)
		}
	}
	if _, _, err := tr.reserve(1); !errors.Is(err, errTooManyTerminalsHere) {
		t.Errorf("cap was bypassed by repeated release: err = %v", err)
	}
}

// Stale reservations must be swept, or an agent that never dials back holds a
// slot until the process restarts.
func TestReserveSweepsStaleReservations(t *testing.T) {
	tr := newTerminalRegistry()
	for i := 0; i < maxTerminalsPerMachine; i++ {
		_, pend, err := tr.reserve(1)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		pend.created = time.Now().Add(-terminalTokenTTL - time.Second)
	}
	// The next reserve sweeps the expired ones and therefore has room.
	if _, _, err := tr.reserve(1); err != nil {
		t.Errorf("reserve after stale reservations expired: %v", err)
	}
	if tr.total != 1 {
		t.Errorf("total = %d, want 1 (four swept, one live)", tr.total)
	}
}

func TestAtoiDefault(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
	}{
		{"", 80, 80},
		{"120", 80, 120},
		{"0", 80, 0},
		{"abc", 80, 80},
		{"12x", 80, 80},
		{"-5", 80, 80},
		{"99999", 80, 80}, // past the uint16 range, so the default stands
	}
	for _, tc := range cases {
		if got := atoiDefault(tc.in, tc.def); got != tc.want {
			t.Errorf("atoiDefault(%q, %d) = %d, want %d", tc.in, tc.def, got, tc.want)
		}
	}
}
