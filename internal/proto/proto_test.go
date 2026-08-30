package proto

import (
	"encoding/json"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := Hello{
		UUID:    "9f1c-aaaa",
		Version: "0.1.0",
		Host:    Host{Platform: "ubuntu", Arch: "amd64", CPUThreads: 8, MemTotal: 1 << 34},
	}
	raw, err := Encode(TypeHello, "", in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.V != Version {
		t.Errorf("version = %d, want %d", env.V, Version)
	}
	if env.Type != TypeHello {
		t.Errorf("type = %q, want %q", env.Type, TypeHello)
	}
	var out Hello
	if err := Decode(&env, &out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.UUID != in.UUID || out.Host.CPUThreads != in.Host.CPUThreads {
		t.Errorf("round trip lost data: got %+v want %+v", out, in)
	}
}

// A frame with no payload must report that rather than leaving the caller with
// a zero value it cannot distinguish from real data.
func TestDecodeEmptyPayload(t *testing.T) {
	var out State
	if err := Decode(&Envelope{V: Version, Type: TypeState}, &out); err == nil {
		t.Fatal("Decode on an empty payload returned nil error")
	}
}

func TestHostUptime(t *testing.T) {
	const now = 1_700_000_000
	tests := []struct {
		name string
		boot uint64
		want uint64
	}{
		{"an hour of uptime", now - 3600, 3600},
		{"unset boot time", 0, 0},
		// A machine whose clock is ahead of the panel would otherwise produce a
		// nonsense uptime that underflows to an enormous number.
		{"boot time in the future", now + 500, 0},
		{"booted exactly now", now, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Host{BootTime: tc.boot}).Uptime(now); got != tc.want {
				t.Errorf("Uptime = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTaskValid(t *testing.T) {
	tests := []struct {
		name string
		task Task
		want bool
	}{
		{"ping", Task{Type: TaskPing, Target: "1.1.1.1", TimeoutMS: 3000}, true},
		{"tcp", Task{Type: TaskTCP, Target: "example.com:443", TimeoutMS: 3000}, true},
		{"http", Task{Type: TaskHTTP, Target: "https://example.com", TimeoutMS: 5000}, true},
		{"unknown type", Task{Type: "exec", Target: "rm -rf /", TimeoutMS: 1000}, false},
		{"empty target", Task{Type: TaskPing, TimeoutMS: 1000}, false},
		{"zero timeout", Task{Type: TaskPing, Target: "1.1.1.1"}, false},
		{"negative timeout", Task{Type: TaskPing, Target: "1.1.1.1", TimeoutMS: -1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.Valid(); got != tc.want {
				t.Errorf("Valid = %v, want %v", got, tc.want)
			}
		})
	}
}

// Unknown message types must decode as an envelope without error so a newer
// server can talk to an older agent. The agent ignores what it does not know.
func TestUnknownTypeStillParses(t *testing.T) {
	raw := []byte(`{"v":1,"type":"some_future_thing","data":{"x":1}}`)
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unknown type failed to parse: %v", err)
	}
	if env.Type != "some_future_thing" {
		t.Errorf("type = %q", env.Type)
	}
}
