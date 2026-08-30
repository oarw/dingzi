package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/oarw/dingzi/internal/proto"
)

// Config is the agent's runtime configuration.
//
// Precedence is command line > environment > file > default. The file is
// optional: an agent started with just --server and --secret works, and writes
// the file only to persist the UUID it generated.
type Config struct {
	// Server is the panel's base URL. Scheme decides TLS with no separate flag:
	// https:// and wss:// are encrypted, http:// and ws:// are not. Nezha v0's
	// --tls / --insecure pair was routinely configured backwards, and a boolean
	// that contradicts the URL beside it has no correct resolution.
	Server string `yaml:"server"`

	// Secret authenticates registration. Treat it as eventually-leaked: it is
	// present on every monitored machine.
	Secret string `yaml:"secret"`

	// UUID is this machine's stable identity, generated on first run and
	// persisted. It survives agent reinstalls, so the panel does not accumulate
	// a duplicate entry every time the binary is replaced.
	UUID string `yaml:"uuid"`

	// Name is an optional display name proposed at first registration only.
	// Renaming a machine in the panel is not undone by the next reconnect.
	Name string `yaml:"name,omitempty"`

	// ReportInterval is the fallback seconds between samples, used until the
	// server's welcome frame supplies its own value.
	ReportInterval float64 `yaml:"report_interval,omitempty"`

	// Mounts restricts disk accounting to these mountpoints. Empty means every
	// physical filesystem.
	Mounts []string `yaml:"mounts,omitempty"`

	// IgnoreInterfaces overrides the built-in list of network interface name
	// prefixes excluded from traffic totals.
	IgnoreInterfaces []string `yaml:"ignore_interfaces,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification. Present for
	// self-signed panels; it is a real downgrade and the agent says so at start.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`

	// path is where this config was loaded from, for writing back.
	path string `yaml:"-"`
}

// DefaultReportInterval is used when neither the file nor the server specifies
// one. One second is what makes the panel feel live; the server overrides it.
const DefaultReportInterval = 1.0

// LoadConfig reads the config file at path if it exists, then applies
// environment overrides, then the non-empty fields of override (the command
// line). A missing file is not an error.
func LoadConfig(path string, override Config) (*Config, error) {
	cfg := &Config{ReportInterval: DefaultReportInterval, path: path}

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, cfg); err != nil {
				// A corrupt file must not be silently replaced by defaults: the
				// operator's secret may still be in there, and starting fresh
				// would register the machine as a new one.
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			// First run.
		default:
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		cfg.path = path
	}

	cfg.applyEnv()
	cfg.applyOverride(override)

	if cfg.ReportInterval <= 0 {
		cfg.ReportInterval = DefaultReportInterval
	}
	return cfg, nil
}

// applyEnv layers DINGZI_* environment variables over the file.
func (c *Config) applyEnv() {
	if v := os.Getenv("DINGZI_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("DINGZI_SECRET"); v != "" {
		c.Secret = v
	}
	if v := os.Getenv("DINGZI_NAME"); v != "" {
		c.Name = v
	}
	if v := os.Getenv("DINGZI_UUID"); v != "" {
		c.UUID = v
	}
	if v := os.Getenv("DINGZI_REPORT_INTERVAL"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			c.ReportInterval = f
		}
	}
	if v := os.Getenv("DINGZI_INSECURE_SKIP_VERIFY"); v != "" {
		c.InsecureSkipVerify = v == "1" || strings.EqualFold(v, "true")
	}
}

// applyOverride layers the command line over everything. Only non-zero fields
// override, so an unset flag does not erase a configured value.
func (c *Config) applyOverride(o Config) {
	if o.Server != "" {
		c.Server = o.Server
	}
	if o.Secret != "" {
		c.Secret = o.Secret
	}
	if o.Name != "" {
		c.Name = o.Name
	}
	if o.UUID != "" {
		c.UUID = o.UUID
	}
	if o.ReportInterval > 0 {
		c.ReportInterval = o.ReportInterval
	}
	if len(o.Mounts) > 0 {
		c.Mounts = o.Mounts
	}
	if len(o.IgnoreInterfaces) > 0 {
		c.IgnoreInterfaces = o.IgnoreInterfaces
	}
	if o.InsecureSkipVerify {
		c.InsecureSkipVerify = true
	}
}

// Validate checks the config is usable and normalises the server URL.
func (c *Config) Validate() error {
	if c.Server == "" {
		return errors.New("no server: pass --server https://panel.example.com")
	}
	if c.Secret == "" {
		return errors.New("no secret: pass --secret <agent key from the panel>")
	}
	ws, err := WebSocketURL(c.Server)
	if err != nil {
		return err
	}
	c.Server = ws
	return nil
}

// WebSocketURL converts a panel base URL into the agent WebSocket endpoint.
// Scheme alone decides TLS, so there is no flag that can contradict it.
func WebSocketURL(base string) (string, error) {
	if !strings.Contains(base, "://") {
		// A bare host is the most common paste. Default to TLS rather than
		// silently sending a secret in clear text.
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("bad server URL %q: %w", base, err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("server URL %q: scheme must be https, http, wss or ws", base)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + proto.Path
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// EnsureUUID generates and persists a UUID if the config has none. It returns
// true when a new identity was created, so the caller can log it once.
func (c *Config) EnsureUUID() (bool, error) {
	if c.UUID != "" {
		return false, nil
	}
	c.UUID = uuid.NewString()
	if c.path == "" {
		// No file to persist to. The agent still runs, but it will register as a
		// new machine on every restart, so the caller warns about it.
		return true, nil
	}
	if err := c.Save(); err != nil {
		return true, fmt.Errorf("persist new UUID: %w", err)
	}
	return true, nil
}

// Save writes the config atomically: a temporary file in the same directory,
// fsync'd, then renamed over the target. A crash or a full disk mid-write
// leaves the previous file intact rather than a truncated one the agent cannot
// parse on next boot.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("no config path to save to")
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Same directory as the target: rename is only atomic within a filesystem.
	tmp, err := os.CreateTemp(dir, ".dingzi-agent-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	// The file holds a secret, so tighten the mode before writing to it.
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	// Without the sync, a crash can leave a renamed file whose contents were
	// never flushed — the rename is ordered, the data is not.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("replace %s: %w", c.path, err)
	}
	return nil
}

// Interval returns the report interval as a duration, clamped to a sane range.
// A server that sends a nonsense value cannot make the agent either hammer the
// panel or go effectively silent.
func (c *Config) Interval(serverSeconds float64) time.Duration {
	s := c.ReportInterval
	if serverSeconds > 0 {
		s = serverSeconds
	}
	if s < 0.5 {
		s = 0.5
	}
	if s > 300 {
		s = 300
	}
	return time.Duration(s * float64(time.Second))
}
