package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/proto"
)

// upgrader is shared by the agent endpoints.
//
// CheckOrigin is left at its default, which rejects a cross-origin browser
// handshake and allows a request with no Origin header at all. Agents send no
// Origin, so they pass; a malicious web page cannot use a visitor's cookies to
// open one of these, because browsers always set Origin. Overriding this to
// always-true is the usual way that protection gets thrown away.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// serveAgent handles the agent metrics connection.
func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request) {
	// Authenticate before upgrading. A rejected agent then gets a readable HTTP
	// 401 in its logs instead of an opaque closed WebSocket.
	if !s.authAgent(r) {
		s.log.Warn("agent rejected: bad secret", slog.String("ip", clientIP(r)))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		return
	}
	conn.SetReadLimit(agentMaxFrame)

	c := newAgentConn(conn, s.log)
	defer c.close()

	hello, err := s.handshake(c)
	if err != nil {
		s.log.Warn("agent handshake failed",
			slog.String("ip", clientIP(r)), slog.Any("error", err))
		c.closeWith(err.Error())
		return
	}

	m, err := s.register(hello)
	if err != nil {
		s.log.Error("registering agent failed", slog.Any("error", err))
		c.closeWith("the panel could not record this machine")
		return
	}
	c.id, c.uuid = m.ID, m.UUID

	if err := c.send(proto.TypeWelcome, "", proto.Welcome{
		ServerID:     m.ID,
		Name:         m.Name,
		Interval:     s.opts.Interval,
		ServerTimeMS: time.Now().UnixMilli(),
	}); err != nil {
		return
	}

	s.hub.Attach(m.ID, c)
	defer s.hub.Detach(m.ID, c)

	s.log.Info("agent connected",
		slog.Int64("id", m.ID), slog.String("name", m.Name),
		slog.String("version", hello.Version), slog.String("ip", clientIP(r)),
		slog.Bool("terminal", hello.Host.TerminalEnabled))
	defer s.log.Info("agent disconnected",
		slog.Int64("id", m.ID), slog.String("name", m.Name))

	go s.pingAgent(c)
	s.readAgent(c)
}

// authAgent checks the shared agent secret.
func (s *Server) authAgent(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		// Query fallback for environments where a header cannot be set. The
		// header is preferred because query strings are recorded in access logs.
		got = r.URL.Query().Get("secret")
	}
	// Constant-time: a length-independent comparison leaks the secret one byte
	// at a time to anyone who can measure response timing.
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.AgentSecret)) == 1
}

// handshake reads and validates the agent's first frame.
func (s *Server) handshake(c *agentConn) (proto.Hello, error) {
	var hello proto.Hello
	if err := c.conn.SetReadDeadline(time.Now().Add(agentReadWait)); err != nil {
		return hello, err
	}
	var env proto.Envelope
	if err := c.conn.ReadJSON(&env); err != nil {
		return hello, fmt.Errorf("reading hello: %w", err)
	}
	if env.Type != proto.TypeHello {
		return hello, fmt.Errorf("expected %q first, got %q", proto.TypeHello, env.Type)
	}
	if err := proto.Decode(&env, &hello); err != nil {
		return hello, err
	}
	if hello.UUID == "" {
		return hello, errors.New("hello has no uuid")
	}
	if env.V != proto.Version {
		return hello, fmt.Errorf(
			"protocol version %d is not supported, this panel speaks %d — update the agent",
			env.V, proto.Version)
	}
	return hello, nil
}

// pingAgent proves the panel is alive and detects a half-open connection.
func (s *Server) pingAgent(c *agentConn) {
	t := time.NewTicker(agentPingEvery)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			if err := c.send(proto.TypePing, "", struct{}{}); err != nil {
				c.close()
				return
			}
		}
	}
}

// readAgent processes inbound frames until the connection fails.
func (s *Server) readAgent(c *agentConn) {
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(agentReadWait))
	})
	for {
		var env proto.Envelope
		if err := c.conn.ReadJSON(&env); err != nil {
			return
		}
		// Any frame proves the agent is alive.
		if err := c.conn.SetReadDeadline(time.Now().Add(agentReadWait)); err != nil {
			return
		}

		switch env.Type {
		case proto.TypeState:
			var st proto.State
			if err := proto.Decode(&env, &st); err != nil {
				s.log.Warn("undecodable state", slog.Int64("id", c.id),
					slog.Any("error", err))
				continue
			}
			s.onState(c.id, st)

		case proto.TypeHostUpdate:
			var host proto.Host
			if err := proto.Decode(&env, &host); err != nil {
				continue
			}
			s.onHostUpdate(c.id, host)

		case proto.TypePing:
			if err := c.send(proto.TypePong, env.ID, struct{}{}); err != nil {
				return
			}

		case proto.TypePong:
			// Read deadline already extended above.

		case proto.TypeTaskResult, proto.TypeTerminalResult:
			c.resolve(&env)

		default:
			s.log.Debug("ignoring unknown agent frame",
				slog.String("type", env.Type), slog.Int64("id", c.id))
		}
	}
}
