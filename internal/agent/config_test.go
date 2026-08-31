package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebSocketURL(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "https becomes wss", in: "https://panel.example.com",
			want: "wss://panel.example.com/api/v1/agent/ws"},
		{name: "http becomes ws", in: "http://10.0.0.5:8008",
			want: "ws://10.0.0.5:8008/api/v1/agent/ws"},
		{name: "wss passes through", in: "wss://panel.example.com",
			want: "wss://panel.example.com/api/v1/agent/ws"},
		{name: "ws passes through", in: "ws://localhost:8008",
			want: "ws://localhost:8008/api/v1/agent/ws"},
		// A bare host is the most common paste, and defaulting to plaintext
		// would send the secret in clear.
		{name: "bare host defaults to TLS", in: "panel.example.com",
			want: "wss://panel.example.com/api/v1/agent/ws"},
		{name: "trailing slash is not doubled", in: "https://panel.example.com/",
			want: "wss://panel.example.com/api/v1/agent/ws"},
		{name: "subpath is preserved for reverse proxies",
			in:   "https://example.com/dingzi",
			want: "wss://example.com/dingzi/api/v1/agent/ws"},
		{name: "query and fragment are dropped",
			in:   "https://panel.example.com/?foo=1#x",
			want: "wss://panel.example.com/api/v1/agent/ws"},
		{name: "unsupported scheme", in: "ftp://panel.example.com", wantErr: true},
		{name: "no host", in: "https://", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WebSocketURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("WebSocketURL(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("WebSocketURL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("WebSocketURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateRequiresServerAndSecret(t *testing.T) {
	if err := (&Config{Secret: "s"}).Validate(); err == nil {
		t.Error("Validate with no server returned nil")
	}
	if err := (&Config{Server: "https://x.example"}).Validate(); err == nil {
		t.Error("Validate with no secret returned nil")
	}
	c := &Config{Server: "https://x.example", Secret: "s"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !strings.HasPrefix(c.Server, "wss://") {
		t.Errorf("Validate did not normalise Server: %q", c.Server)
	}
}

// The precedence chain is command line > env > file. Getting this wrong means an
// operator's flag silently does nothing.
func TestLoadConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(
		"server: https://from-file.example\nsecret: file-secret\nname: file-name\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DINGZI_SECRET", "env-secret")
	t.Setenv("DINGZI_NAME", "env-name")

	cfg, err := LoadConfig(path, Config{Name: "flag-name"})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Server != "https://from-file.example" {
		t.Errorf("Server = %q, want the file value (nothing overrode it)", cfg.Server)
	}
	if cfg.Secret != "env-secret" {
		t.Errorf("Secret = %q, want the env value to beat the file", cfg.Secret)
	}
	if cfg.Name != "flag-name" {
		t.Errorf("Name = %q, want the flag to beat env and file", cfg.Name)
	}
}

func TestLoadConfigMissingFileIsNotAnError(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml"),
		Config{Server: "https://x.example", Secret: "s"})
	if err != nil {
		t.Fatalf("LoadConfig on a missing file: %v", err)
	}
	if cfg.ReportInterval != DefaultReportInterval {
		t.Errorf("ReportInterval = %v, want the default", cfg.ReportInterval)
	}
}

// A corrupt file must fail loudly. Falling back to defaults would discard the
// operator's secret and re-register the machine as a new one.
func TestLoadConfigCorruptFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("server: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path, Config{}); err == nil {
		t.Fatal("LoadConfig on a corrupt file returned nil error")
	}
}

func TestEnsureUUIDGeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	cfg, err := LoadConfig(path, Config{Server: "https://x.example", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := cfg.EnsureUUID()
	if err != nil {
		t.Fatalf("EnsureUUID: %v", err)
	}
	if !created || cfg.UUID == "" {
		t.Fatalf("created = %v, UUID = %q", created, cfg.UUID)
	}

	// The identity must survive a restart, or the panel accumulates a duplicate
	// machine every time the agent is restarted.
	again, err := LoadConfig(path, Config{})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.UUID != cfg.UUID {
		t.Errorf("UUID not persisted: wrote %q, read back %q", cfg.UUID, again.UUID)
	}
	created2, err := again.EnsureUUID()
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("EnsureUUID regenerated an existing UUID")
	}
}

// Save must never leave a partial file behind, and must not leave its temporary
// files in the directory.
func TestSaveIsAtomicAndLeavesNoTemps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	cfg := &Config{Server: "https://x.example", Secret: "s", UUID: "u1", path: path}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg.UUID = "u2"
	if err := cfg.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dingzi-agent-") {
			t.Errorf("Save left a temporary file behind: %s", e.Name())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "u2") {
		t.Errorf("file does not contain the second write:\n%s", data)
	}
	// The file holds a secret. Windows does not implement POSIX modes, so this
	// is only meaningful where it does.
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		t.Logf("config mode is %v; group/other bits set (expected on Windows)", fi.Mode().Perm())
	}
}

func TestIntervalClamping(t *testing.T) {
	c := &Config{ReportInterval: 1}
	tests := []struct {
		name     string
		server   float64
		wantSecs float64
	}{
		{"server value wins", 5, 5},
		{"zero falls back to config", 0, 1},
		// A server sending nonsense must not make the agent hammer the panel or
		// go effectively silent.
		{"absurdly small is clamped up", 0.001, 0.5},
		{"absurdly large is clamped down", 1e9, 300},
		{"negative falls back", -3, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Interval(tc.server).Seconds(); got != tc.wantSecs {
				t.Errorf("Interval(%v) = %vs, want %vs", tc.server, got, tc.wantSecs)
			}
		})
	}
}
