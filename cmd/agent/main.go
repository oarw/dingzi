// Command dingzi-agent reports one machine's metrics to a dingzi panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/oarw/dingzi/internal/agent"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dingzi-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the config file (optional)")
	server := flag.String("server", "", "panel URL, e.g. https://panel.example.com")
	secret := flag.String("secret", "", "agent secret from the panel")
	name := flag.String("name", "", "display name on first registration")
	interval := flag.Float64("interval", 0, "seconds between samples")
	mounts := flag.String("mounts", "", "comma-separated mount points to total for disk usage")
	allowTerminal := flag.Bool("allow-terminal", false,
		"allow the panel to open a shell on this machine")
	insecure := flag.Bool("insecure-skip-verify", false,
		"skip TLS certificate verification (not recommended)")
	debug := flag.Bool("debug", false, "verbose logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("dingzi-agent", version)
		return nil
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := agent.LoadConfig(*configPath, agent.Config{
		Server:             *server,
		Secret:             *secret,
		Name:               *name,
		ReportInterval:     *interval,
		Mounts:             splitList(*mounts),
		AllowTerminal:      *allowTerminal,
		InsecureSkipVerify: *insecure,
	})
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	generated, err := cfg.EnsureUUID()
	if err != nil {
		return err
	}
	if generated {
		if cfg.Path() == "" {
			// Without a config file the uuid is regenerated on every start, so
			// the panel registers the machine again and the history splits.
			log.Warn("no config file, so this machine's identity is not saved — " +
				"pass --config to keep one identity across restarts")
		} else if err := cfg.Save(); err != nil {
			log.Warn("could not save the config", slog.Any("error", err))
		}
	}

	if cfg.InsecureSkipVerify {
		log.Warn("TLS certificate verification is off — " +
			"the agent secret can be intercepted by anything on the path")
	}
	if cfg.AllowTerminal {
		// Said plainly at startup, once, where an operator will see it. The shell
		// runs as this process's user, which for a monitoring agent is usually
		// root.
		log.Warn("web terminal is enabled — the panel can open a shell on this " +
			"machine, running as the user this agent runs as")
	}

	col, err := agent.NewCollector(cfg.Mounts, cfg.IgnoreInterfaces)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", slog.String("version", version),
		slog.String("server", cfg.Server), slog.String("uuid", cfg.UUID),
		slog.Bool("terminal", cfg.AllowTerminal))

	client := agent.NewClient(cfg, col, version, log)
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
