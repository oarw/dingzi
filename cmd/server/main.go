// Command dingzi-server is the monitoring panel: one binary, one port, an
// embedded UI and a SQLite file.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/oarw/dingzi/internal/server"
)

var version = "dev"

// panelConfig is the panel's persisted secrets and settings.
type panelConfig struct {
	AgentSecret  string `yaml:"agent_secret"`
	PasswordHash string `yaml:"password_hash"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dingzi-server:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":8008", "address to listen on")
	data := flag.String("data", "./data", "directory for the database and config")
	interval := flag.Float64("interval", 1, "seconds between agent samples")
	retentionDays := flag.Int("retention-days", 30, "days of raw samples to keep")
	secureCookie := flag.Bool("secure-cookie", false,
		"mark the session cookie Secure (enable when served over https)")
	terminal := flag.Bool("terminal", true,
		"allow web terminals; agents must also be started with --allow-terminal")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("dingzi-server", version)
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))

	if err := os.MkdirAll(*data, 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	cfgPath := filepath.Join(*data, "config.yaml")
	cfg, firstRun, plainPassword, err := loadOrInitConfig(cfgPath)
	if err != nil {
		return err
	}

	store, err := server.OpenStore(filepath.Join(*data, "dingzi.db"), log)
	if err != nil {
		return err
	}
	defer store.Close()

	srv, err := server.New(server.Options{
		AgentSecret:     cfg.AgentSecret,
		PasswordHash:    cfg.PasswordHash,
		Interval:        *interval,
		SecureCookie:    *secureCookie,
		TerminalEnabled: *terminal,
		Retention:       time.Duration(*retentionDays) * 24 * time.Hour,
	}, store, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.Maintain(ctx)

	httpSrv := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// No WriteTimeout on purpose: it applies to the whole response, which for
		// a WebSocket is the entire session, so any value would cut agent
		// connections and terminals at that deadline.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	printBanner(*listen, cfg.AgentSecret, plainPassword, firstRun, *terminal)

	errc := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutCtx)
}

// loadOrInitConfig reads the config, generating secrets on first run.
func loadOrInitConfig(path string) (panelConfig, bool, string, error) {
	var cfg panelConfig

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			// Refuse rather than regenerate. Overwriting would invalidate every
			// agent secret in the fleet and lock the operator out of the panel,
			// both at once, in response to a file that may just need a typo
			// fixed.
			return cfg, false, "", fmt.Errorf(
				"config %s is unreadable (%w) — fix or move it, "+
					"regenerating would invalidate every agent secret", path, err)
		}
		if cfg.AgentSecret == "" {
			return cfg, false, "", fmt.Errorf("config %s has no agent_secret", path)
		}
		return cfg, false, "", nil

	case errors.Is(err, os.ErrNotExist):
		// Generated, never chosen: there is no default password to leak and no
		// weak one to validate.
		secret, err := randomToken(32)
		if err != nil {
			return cfg, false, "", err
		}
		password, err := randomToken(9)
		if err != nil {
			return cfg, false, "", err
		}
		hash, err := server.HashPassword(password)
		if err != nil {
			return cfg, false, "", err
		}
		cfg = panelConfig{AgentSecret: secret, PasswordHash: hash}
		if err := writeConfigAtomic(path, cfg); err != nil {
			return cfg, false, "", err
		}
		return cfg, true, password, nil

	default:
		return cfg, false, "", fmt.Errorf("reading %s: %w", path, err)
	}
}

// writeConfigAtomic writes the config so a crash cannot leave a half-written
// file holding the only copy of the fleet's secret.
func writeConfigAtomic(path string, cfg panelConfig) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	// Temp file in the same directory: rename is only atomic within a
	// filesystem.
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	// Without the fsync the rename is ordered but the contents are not, so a
	// power loss can leave an empty file where the secret should be.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func printBanner(listen, secret, password string, firstRun, terminal bool) {
	fmt.Println()
	fmt.Println("  dingzi 面板已启动  ", version)
	fmt.Println("  监听地址:", listen)
	if terminal {
		fmt.Println("  网页终端: 面板已允许（agent 需加 --allow-terminal 才可用）")
	} else {
		fmt.Println("  网页终端: 已禁用")
	}
	fmt.Println()
	if firstRun {
		fmt.Println("  ── 首次启动，以下信息只显示这一次 ──")
		fmt.Println()
		fmt.Println("  管理员密码:", password)
		fmt.Println()
		fmt.Println("  Agent 密钥:", secret)
		fmt.Println()
		fmt.Println("  安装 agent:")
		fmt.Printf("    dingzi-agent --server <面板地址> --secret %s\n", secret)
		fmt.Println()
		fmt.Println("  容器内需要网页终端时追加 --allow-terminal")
		fmt.Println()
	} else {
		fmt.Println("  Agent 密钥:", secret)
		fmt.Println()
	}
}
