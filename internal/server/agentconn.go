package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/proto"
)

// Timings for the agent side of the metrics connection. Deliberately more
// tolerant than the agent's own: the panel should not disconnect an agent that
// is merely slow, because a reconnect costs a hello, a host collection and a
// gap in the chart.
const (
	agentReadWait  = 60 * time.Second
	agentPingEvery = 20 * time.Second
	agentWriteWait = 10 * time.Second

	// agentMaxFrame caps an inbound frame. A state sample with sensors and GPUs
	// is a few KiB; 256KiB is generous without letting one agent allocate
	// arbitrarily.
	agentMaxFrame = 1 << 18

	// taskTimeout bounds how long a dispatched task may take before the panel
	// stops waiting for its result.
	taskTimeout = 60 * time.Second

	// terminalOpenTimeout bounds how long the panel waits for an agent to say
	// whether it will start a shell. Short, because the operator is watching a
	// blank pane: an agent that has not answered in this long is not going to.
	terminalOpenTimeout = 15 * time.Second
)

// pending is a reply the panel is waiting for.
type pending struct {
	want string
	ch   chan *proto.Envelope
}

// agentConn is one live agent metrics connection.
type agentConn struct {
	conn *websocket.Conn
	log  *slog.Logger

	// machine identity, set once at handshake and read-only after.
	id   int64
	uuid string

	// writeMu serialises writes. Concurrent writes to a websocket are a data
	// race, and the panel writes from several places: the ping loop, task
	// dispatch, terminal open.
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]pending
	nextID  uint64

	closeOnce sync.Once
	closed    chan struct{}
}

func newAgentConn(conn *websocket.Conn, log *slog.Logger) *agentConn {
	return &agentConn{
		conn:    conn,
		log:     log,
		pending: map[string]pending{},
		closed:  make(chan struct{}),
	}
}

// send writes one frame.
func (c *agentConn) send(typ, id string, payload any) error {
	raw, err := proto.Encode(typ, id, payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(agentWriteWait)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, raw)
}

// closeWith tells the agent why it is being dropped, then closes.
//
// The message is marked fatal so the agent stops rather than reconnecting in a
// tight loop against a rejection that cannot change — a bad secret retried
// every second is a self-inflicted denial of service on the panel.
func (c *agentConn) closeWith(reason string) {
	_ = c.send(proto.TypeError, "", proto.ErrorPayload{Message: reason, Fatal: true})
	c.close()
}

func (c *agentConn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

// resolve hands a reply to whoever is waiting for it.
func (c *agentConn) resolve(env *proto.Envelope) {
	if env.ID == "" {
		return
	}
	c.mu.Lock()
	p, ok := c.pending[env.ID]
	if ok {
		delete(c.pending, env.ID)
	}
	c.mu.Unlock()
	if !ok {
		// A reply to a request that already timed out. Dropping it is correct;
		// the waiter has gone.
		return
	}
	select {
	case p.ch <- env:
	default:
	}
}

// request sends a frame and waits for the agent's correlated reply.
//
// Every exit path removes the pending entry, including the timeout and the
// connection-closed paths. That is what keeps the map bounded by the number of
// in-flight requests rather than by the number ever sent: an agent that
// vanishes mid-request would otherwise leak an entry per request, forever.
func (c *agentConn) request(
	ctx context.Context, typ, want string, payload any, timeout time.Duration,
) (*proto.Envelope, error) {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("%d", c.nextID)
	ch := make(chan *proto.Envelope, 1)
	c.pending[id] = pending{want: want, ch: ch}
	c.mu.Unlock()

	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	if err := c.send(typ, id, payload); err != nil {
		forget()
		return nil, fmt.Errorf("send %s: %w", typ, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case env := <-ch:
		return env, nil
	case <-timer.C:
		forget()
		return nil, fmt.Errorf("agent did not answer %s within %s", typ, timeout)
	case <-c.closed:
		forget()
		return nil, errors.New("agent disconnected")
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

// Dispatch runs a reachability check on this agent.
func (c *agentConn) Dispatch(ctx context.Context, t proto.Task) (proto.TaskResult, error) {
	var res proto.TaskResult
	env, err := c.request(ctx, proto.TypeTask, proto.TypeTaskResult, t, taskTimeout)
	if err != nil {
		return res, err
	}
	if err := proto.Decode(env, &res); err != nil {
		return res, err
	}
	return res, nil
}

// OpenTerminal asks the agent to start a shell and dial back with this session
// token.
//
// The returned result distinguishes a refusal from a failure to answer. Both
// leave the operator without a terminal, but only one of them is worth
// investigating, and a panel that reports them identically sends people looking
// in the wrong place.
func (c *agentConn) OpenTerminal(
	ctx context.Context, open proto.TerminalOpen,
) (proto.TerminalResult, error) {
	var res proto.TerminalResult
	env, err := c.request(ctx, proto.TypeTerminalOpen, proto.TypeTerminalResult,
		open, terminalOpenTimeout)
	if err != nil {
		return res, err
	}
	if err := proto.Decode(env, &res); err != nil {
		return res, err
	}
	return res, nil
}
