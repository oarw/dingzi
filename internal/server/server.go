package server

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

//go:embed web
var webFS embed.FS

// Options configures a panel.
type Options struct {
	// AgentSecret is the shared secret every agent presents.
	AgentSecret string
	// PasswordHash is the bcrypt hash of the admin password.
	PasswordHash string
	// Interval is the seconds between samples the panel asks agents for.
	Interval float64
	// SecureCookie marks the session cookie Secure. Off by default because a
	// panel reached over plain http would otherwise be unable to log in at all,
	// with no visible reason.
	SecureCookie bool
	// TerminalEnabled allows web terminals panel-wide. The agent must opt in
	// separately; this is the operator's switch for the panel half.
	TerminalEnabled bool
	// Retention is how long raw samples are kept.
	Retention time.Duration
}

// Server is the panel.
type Server struct {
	opts      Options
	log       *slog.Logger
	store     *Store
	hub       *Hub
	sessions  *sessionStore
	terminals *terminalRegistry
}

// New builds a panel and loads the known fleet.
func New(opts Options, store *Store, log *slog.Logger) (*Server, error) {
	if opts.Interval <= 0 {
		opts.Interval = 1
	}
	if opts.Retention == 0 {
		opts.Retention = 30 * 24 * time.Hour
	}

	s := &Server{
		opts: opts, log: log, store: store,
		hub:       NewHub(),
		sessions:  newSessionStore(),
		terminals: newTerminalRegistry(),
	}

	// Load the fleet up front so a restart does not appear to lose every
	// machine until each agent happens to reconnect.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	machines, err := store.Machines(ctx)
	if err != nil {
		return nil, err
	}
	s.hub.Load(machines)
	log.Info("panel ready", slog.Int("known_machines", len(machines)))
	return s, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Agent transport.
	mux.HandleFunc("GET "+proto.Path, s.serveAgent)
	mux.HandleFunc("GET "+proto.TerminalAgentPath, s.handleAgentTerminal)

	// Session.
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/session", s.handleSession)

	// Readable without a session: the fleet view is the point of the panel and
	// is commonly left on a wall display. Nothing here is a secret the agent
	// secret protects.
	mux.HandleFunc("GET /api/v1/servers", s.handleServers)
	mux.HandleFunc("GET /api/v1/servers/{id}/history", s.handleHistory)

	// Mutations and terminals require a session.
	mux.HandleFunc("PATCH /api/v1/servers/{id}", s.requireAuth(s.handlePatchServer))
	mux.HandleFunc("DELETE /api/v1/servers/{id}", s.requireAuth(s.handleDeleteServer))
	mux.HandleFunc("GET /api/v1/servers/{id}/terminal",
		s.requireAuth(s.handleBrowserTerminal))

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("embedded web assets missing: " + err.Error())
	}
	mux.Handle("/", noCacheHTML(http.FileServer(http.FS(sub))))

	return mux
}

// noCacheHTML stops a browser serving a stale panel after an upgrade. Static
// assets carry no version in their names, so without this an operator who
// upgrades sees the old UI talking to the new API.
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// register records an agent's hello.
func (s *Server) register(hello proto.Hello) (*Machine, error) {
	m, err := s.store.Register(hello, time.Now())
	if err != nil {
		return nil, err
	}
	// Put keeps the hub's existing entry if there is one, so the ring buffer and
	// the traffic accumulator survive a reconnect.
	live := s.hub.Put(m)
	if live.FirstSeen.IsZero() {
		live.FirstSeen = time.Now()
	}
	if m.ID == live.ID && live.Version != hello.Version {
		live.Version = hello.Version
	}
	return live, nil
}

// onState records a metrics sample.
func (s *Server) onState(id int64, st proto.State) {
	sample, ok := s.hub.Push(id, st, time.Now())
	if !ok {
		return
	}
	s.store.QueueSample(id, sample)
}

// onHostUpdate records changed static facts.
func (s *Server) onHostUpdate(id int64, host proto.Host) {
	if !s.hub.SetHost(id, host) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.UpdateHost(ctx, id, host); err != nil {
		s.log.Warn("saving host update failed", slog.Int64("id", id), slog.Any("error", err))
	}
}

// Maintain runs the panel's periodic work until ctx is cancelled.
func (s *Server) Maintain(ctx context.Context) {
	traffic := time.NewTicker(30 * time.Second)
	defer traffic.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			// One last persist on the way out, with a context that outlives the
			// cancelled one, so a clean shutdown does not discard up to 30
			// seconds of every machine's traffic.
			final, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), 10*time.Second)
			s.persistTraffic(final)
			cancel()
			return
		case <-traffic.C:
			tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			s.persistTraffic(tctx)
			cancel()
		case <-prune.C:
			s.pruneOld(ctx)
		}
	}
}

// persistTraffic writes the cycle counters.
func (s *Server) persistTraffic(ctx context.Context) {
	// Snapshot under the hub lock, write outside it. Holding the lock across
	// disk I/O would queue every agent's sample behind the slowest write.
	snap := s.hub.TrafficSnapshot()
	if err := s.store.SaveTraffic(ctx, snap); err != nil {
		s.log.Warn("saving traffic failed", slog.Any("error", err))
	}
}

func (s *Server) pruneOld(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	n, err := s.store.Prune(pctx, s.opts.Retention)
	if err != nil {
		s.log.Warn("pruning old samples failed", slog.Any("error", err))
		return
	}
	if n > 0 {
		s.log.Info("pruned old samples", slog.Int64("rows", n))
	}
}
