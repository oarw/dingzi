package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/proto"
)

// Reconnect and liveness timings.
const (
	// backoffMin and backoffMax bound the reconnect delay. The ceiling is two
	// minutes: an operator restarting the panel should not wait an hour for a
	// fleet to come back, which is what an unbounded doubling produces.
	backoffMin = 1 * time.Second
	backoffMax = 2 * time.Minute

	// pongWait is how long the agent tolerates silence from the server before
	// declaring the connection dead and rebuilding it. This is the fix for the
	// stuck-offline failure: a TCP connection through a middlebox can be
	// half-open indefinitely, where writes succeed into a void and the agent
	// believes it is connected while the panel shows it offline.
	pongWait = 45 * time.Second

	// pingEvery is how often the agent proves it is alive. Well under pongWait
	// so a single lost frame does not tear down a healthy connection.
	pingEvery = 15 * time.Second

	// writeWait bounds a single write. Without it a blocked socket wedges the
	// writer goroutine forever.
	writeWait = 10 * time.Second

	// handshakeTimeout bounds connection setup.
	handshakeTimeout = 20 * time.Second

	// maxFrame caps an inbound frame. The largest thing a server legitimately
	// sends is a task, which is a few hundred bytes.
	maxFrame = 1 << 16
)

// Client maintains the connection to the panel.
//
// Run never returns on a recoverable error: it reconnects with exponential
// backoff plus jitter, forever. Only a fatal server rejection (bad secret,
// protocol mismatch) or a cancelled context stops it, because retrying those
// with identical inputs cannot succeed.
type Client struct {
	cfg     *Config
	col     *Collector
	version string
	log     *slog.Logger

	// mu guards conn. The read loop and the sample loop both touch it.
	mu   sync.Mutex
	conn *websocket.Conn

	// lastHost is the host info most recently sent, so changes can be detected
	// without resending static facts on every sample.
	lastHost     proto.Host
	lastHostSent time.Time
}

// NewClient builds a client. The collector is reused across reconnects so the
// network rate baseline is not lost every time the panel restarts.
func NewClient(cfg *Config, col *Collector, version string, log *slog.Logger) *Client {
	return &Client{cfg: cfg, col: col, version: version, log: log}
}

// fatalError marks a rejection that retrying cannot fix.
type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

// Run connects and reports until ctx is cancelled or the server rejects the
// agent fatally.
func (c *Client) Run(ctx context.Context) error {
	backoff := backoffMin
	for {
		start := time.Now()
		err := c.session(ctx)

		if ctx.Err() != nil {
			return ctx.Err()
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			// Retrying a bad secret forever would hammer the panel and bury the
			// real cause in reconnect noise.
			return fmt.Errorf("server rejected this agent: %s", fatal.msg)
		}

		// A session that stayed up a while was healthy; the next failure should
		// retry promptly rather than inheriting a long backoff from an outage
		// that has since been fixed.
		if time.Since(start) > 2*pongWait {
			backoff = backoffMin
		}

		wait := jitter(backoff)
		c.log.Warn("disconnected, reconnecting",
			slog.Any("error", err), slog.Duration("in", wait))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		backoff = min(time.Duration(float64(backoff)*1.7), backoffMax)
	}
}

// jitter spreads reconnects so a fleet that lost the panel together does not
// return as a synchronised thundering herd. Full jitter over [d/2, d].
func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// session runs one connection from handshake to failure.
func (c *Client) session(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	conn.SetReadLimit(maxFrame)
	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	// The control-frame pong handler extends the deadline. The application-level
	// ping below is the primary channel because some proxies drop control
	// frames, but honouring both costs nothing.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	host, err := c.col.Host(ctx)
	if err != nil {
		// Partial host info is still worth reporting; the panel shows blanks
		// rather than dropping the machine.
		c.log.Warn("host info incomplete", slog.Any("error", err))
	}
	hello := proto.Hello{
		UUID: c.cfg.UUID, Version: c.version, Name: c.cfg.Name, Host: host,
	}
	if err := c.send(proto.TypeHello, "", hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	c.lastHost, c.lastHostSent = host, time.Now()

	welcome, err := c.awaitWelcome(conn)
	if err != nil {
		return err
	}
	skew := time.Since(time.UnixMilli(welcome.ServerTimeMS))
	c.log.Info("connected",
		slog.String("name", welcome.Name),
		slog.Int64("server_id", welcome.ServerID),
		slog.Float64("interval_s", welcome.Interval),
		slog.Duration("clock_skew", skew.Round(time.Millisecond)))

	// One goroutine reads, one writes. Sharing a websocket write across
	// goroutines is a data race, so every write funnels through c.send under the
	// mutex, and only this session's loops call it.
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)
	go func() { errc <- c.readLoop(sessCtx, conn) }()
	go func() { errc <- c.reportLoop(sessCtx, welcome.Interval) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		// Tell the server we are going rather than leaving it to a timeout.
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second))
		return ctx.Err()
	}
}

// dial opens the WebSocket connection.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	d := websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
		Proxy:            http.ProxyFromEnvironment,
	}
	if c.cfg.InsecureSkipVerify {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// The secret travels in a header, not the query string: query strings land
	// in proxy and server access logs.
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.cfg.Secret)
	h.Set("User-Agent", "dingzi-agent/"+c.version)

	conn, resp, err := d.DialContext(ctx, c.cfg.Server, h)
	if err != nil {
		if resp != nil {
			// A 401 will never succeed on retry with the same secret.
			if resp.StatusCode == http.StatusUnauthorized ||
				resp.StatusCode == http.StatusForbidden {
				return nil, &fatalError{msg: fmt.Sprintf(
					"HTTP %d — check the agent secret", resp.StatusCode)}
			}
			return nil, fmt.Errorf("dial %s: HTTP %d: %w", c.cfg.Server, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dial %s: %w", c.cfg.Server, err)
	}
	return conn, nil
}

// awaitWelcome reads frames until the welcome arrives, rejecting anything else.
func (c *Client) awaitWelcome(conn *websocket.Conn) (proto.Welcome, error) {
	var w proto.Welcome
	for {
		var env proto.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return w, fmt.Errorf("waiting for welcome: %w", err)
		}
		switch env.Type {
		case proto.TypeWelcome:
			if err := proto.Decode(&env, &w); err != nil {
				return w, err
			}
			return w, nil
		case proto.TypeError:
			var e proto.ErrorPayload
			_ = proto.Decode(&env, &e)
			if e.Fatal {
				return w, &fatalError{msg: e.Message}
			}
			return w, fmt.Errorf("server error before welcome: %s", e.Message)
		default:
			// Ignore rather than fail: a newer server may send something this
			// agent does not know about yet.
			c.log.Debug("ignoring frame before welcome", slog.String("type", env.Type))
		}
	}
}

// send writes one frame. All writes go through here so the mutex serialises them.
func (c *Client) send(typ, id string, payload any) error {
	raw, err := proto.Encode(typ, id, payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("not connected")
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, raw)
}

// readLoop handles inbound frames for the life of the connection.
func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		var env proto.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read: %w", err)
		}
		// Any frame proves the server is alive, which is what makes the
		// half-open connection detectable.
		if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			return err
		}

		switch env.Type {
		case proto.TypePing:
			if err := c.send(proto.TypePong, env.ID, struct{}{}); err != nil {
				return fmt.Errorf("pong: %w", err)
			}
		case proto.TypeTask:
			var t proto.Task
			if err := proto.Decode(&env, &t); err != nil {
				c.log.Warn("undecodable task", slog.Any("error", err))
				continue
			}
			// Each task runs in its own goroutine: a 5-second ping must not
			// delay the metrics stream or another task behind it.
			go c.runTask(ctx, env.ID, t)
		case proto.TypeError:
			var e proto.ErrorPayload
			_ = proto.Decode(&env, &e)
			if e.Fatal {
				return &fatalError{msg: e.Message}
			}
			c.log.Warn("server reported an error", slog.String("message", e.Message))
		default:
			c.log.Debug("ignoring unknown frame", slog.String("type", env.Type))
		}
	}
}

// reportLoop sends metrics samples at the server's requested interval.
func (c *Client) reportLoop(ctx context.Context, serverInterval float64) error {
	interval := c.cfg.Interval(serverInterval)
	sample := time.NewTicker(interval)
	defer sample.Stop()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-sample.C:
			if err := c.send(proto.TypeState, "", c.col.State(ctx)); err != nil {
				return fmt.Errorf("send state: %w", err)
			}
			c.maybeResendHost(ctx)

		case <-ping.C:
			// Application-level ping, above the WebSocket control frame, because
			// some proxies swallow control frames entirely. Ping is
			// bidirectional: the server answers with a pong, and that reply is
			// what refreshes this side's read deadline.
			if err := c.send(proto.TypePing, "", struct{}{}); err != nil {
				return fmt.Errorf("heartbeat: %w", err)
			}
		}
	}
}

// maybeResendHost reports static facts again when they change — a kernel
// upgrade, a resized disk. Checked at most every five minutes, because the
// collection is far more expensive than a metrics sample.
func (c *Client) maybeResendHost(ctx context.Context) {
	if time.Since(c.lastHostSent) < 5*time.Minute {
		return
	}
	host, err := c.col.Host(ctx)
	if err != nil {
		return
	}
	c.lastHostSent = time.Now()
	if host.Equal(c.lastHost) {
		return
	}
	if err := c.send(proto.TypeHostUpdate, "", host); err != nil {
		c.log.Warn("host update failed", slog.Any("error", err))
		return
	}
	c.lastHost = host
}

