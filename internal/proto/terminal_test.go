package proto

import (
	"encoding/json"
	"testing"
)

func TestClampTerminalSize(t *testing.T) {
	cases := []struct {
		name               string
		cols, rows         uint16
		wantCols, wantRows uint16
	}{
		{"typical", 120, 40, 120, 40},
		{"zero means conventional default", 0, 0, 80, 24},
		{"zero cols only", 0, 40, 80, 40},
		{"zero rows only", 120, 0, 120, 24},
		{"below minimum clamps up", 4, 1, TerminalMinCols, TerminalMinRows},
		{"above maximum clamps down", 9000, 9000, TerminalMaxCols, TerminalMaxRows},
		{"at the bounds is unchanged", TerminalMinCols, TerminalMaxRows,
			TerminalMinCols, TerminalMaxRows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := ClampTerminalSize(tc.cols, tc.rows)
			if cols != tc.wantCols || rows != tc.wantRows {
				t.Errorf("ClampTerminalSize(%d, %d) = (%d, %d), want (%d, %d)",
					tc.cols, tc.rows, cols, rows, tc.wantCols, tc.wantRows)
			}
		})
	}
}

// A clamped size must never be zero in either dimension. A zero row count makes
// some shells misbehave in ways that look like a crash rather than a bad size,
// so this is worth asserting separately from the table above.
func TestClampTerminalSizeIsNeverZero(t *testing.T) {
	for _, cols := range []uint16{0, 1, 80, 65535} {
		for _, rows := range []uint16{0, 1, 24, 65535} {
			gotCols, gotRows := ClampTerminalSize(cols, rows)
			if gotCols == 0 || gotRows == 0 {
				t.Fatalf("ClampTerminalSize(%d, %d) produced a zero dimension: (%d, %d)",
					cols, rows, gotCols, gotRows)
			}
		}
	}
}

func TestTerminalRoundTrip(t *testing.T) {
	raw, err := Encode(TypeTerminalOpen, "req-1",
		TerminalOpen{Session: "tok", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal envelope: %v", err)
	}
	if env.Type != TypeTerminalOpen || env.ID != "req-1" || env.V != Version {
		t.Fatalf("envelope = %+v", env)
	}
	var open TerminalOpen
	if err := Decode(&env, &open); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if open.Session != "tok" || open.Cols != 100 || open.Rows != 30 {
		t.Errorf("TerminalOpen = %+v", open)
	}
}

// The session token must not appear in any JSON the panel sends anywhere except
// the TerminalOpen payload on the metrics connection. This test pins the field
// name so a rename cannot silently move it into a logged position.
func TestTerminalOpenSessionFieldName(t *testing.T) {
	raw, err := json.Marshal(TerminalOpen{Session: "s", Cols: 1, Rows: 2})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := m["session"]; !ok {
		t.Errorf("TerminalOpen JSON = %s, want a \"session\" field", raw)
	}
}

// A refusal must carry a reason. An agent that answers OK=false with an empty
// Error leaves the operator with a blank pane and nothing to act on, which is
// the failure this message type exists to prevent.
func TestTerminalResultCarriesReason(t *testing.T) {
	res := TerminalResult{OK: false, Error: "terminals are disabled on this agent"}
	raw, err := Encode(TypeTerminalResult, "req-1", res)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var got TerminalResult
	if err := Decode(&env, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.OK || got.Error == "" {
		t.Errorf("TerminalResult = %+v, want a refusal with a reason", got)
	}
}
