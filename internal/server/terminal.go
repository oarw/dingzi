package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/proto"
)

// Terminal session limits.
const (
	// terminalTokenTTL is how long a minted session token stays claimable. It
	// only has to cover the agent noticing the request and opening one
	// connection, so it is short: a token that outlives its use is a credential
	// waiting to be replayed.
	terminalTokenTTL = 30 * time.Second

	// maxTerminalsPerMachine and maxTerminalsTotal bound concurrent sessions.
	// Each one holds a pty, two goroutines and a 32KiB buffer on the agent, so
	// this is a real resource, and "however many the operator clicks" is not a
	// bound.
	maxTerminalsPerMachine = 4
	maxTerminalsTotal      = 16

	// terminalIdleTimeout closes a session with no operator input.
	//
	// Reset by input only, not by output. A tab left open running `tail -f`
	// would otherwise hold a shell open forever, which is the case this exists
	// for. The cost is that watching a long build with no typing ends the
	// session; 15 minutes makes that unlikely and reopening is cheap.
	terminalIdleTimeout = 15 * time.Minute

	// terminalDialWait is how long the panel holds a browser connection waiting
	// for the agent to dial back after it already agreed to open a shell.
	terminalDialWait = 20 * time.Second

	terminalWriteWait = 10 * time.Second
	// terminalMaxFrame caps one inbound frame. Terminal input is keystrokes and
	// the occasional paste; output is chunked by the agent's read buffer.
	terminalMaxFrame = 1 << 20
)

// pendingTerminal is a session waiting for its agent half.
type pendingTerminal struct {
	machineID int64
	created   time.Time
	// deliver receives the agent's connection exactly once.
	deliver chan *websocket.Conn
}

// terminalRegistry tracks pending and active terminal sessions.
type terminalRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingTerminal
	// perMachine counts both pending and active sessions, so the cap cannot be
	// bypassed by opening many at once and racing the agent dial-backs.
	perMachine map[int64]int
	total      int
}

func newTerminalRegistry() *terminalRegistry {
	return &terminalRegistry{
		pending:    map[string]*pendingTerminal{},
		perMachine: map[int64]int{},
	}
}

// reserve mints a token and counts the session against the caps.
func (tr *terminalRegistry) reserve(machineID int64) (string, *pendingTerminal, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)

	tr.mu.Lock()
	defer tr.mu.Unlock()

	// Expire stale reservations first. Without this an agent that never dials
	// back would consume a slot until the process restarts.
	now := time.Now()
	for t, p := range tr.pending {
		if now.Sub(p.created) > terminalTokenTTL {
			delete(tr.pending, t)
			tr.drop(p.machineID)
		}
	}

	if tr.total >= maxTerminalsTotal {
		return "", nil, errTooManyTerminals
	}
	if tr.perMachine[machineID] >= maxTerminalsPerMachine {
		return "", nil, errTooManyTerminalsHere
	}

	p := &pendingTerminal{
		machineID: machineID,
		created:   now,
		deliver:   make(chan *websocket.Conn, 1),
	}
	tr.pending[tok] = p
	tr.perMachine[machineID]++
	tr.total++
	return tok, p, nil
}

// claim consumes a token, returning the session it belongs to.
//
// Single use: the token is deleted whether or not the caller goes on to
// succeed, so a leaked token cannot be replayed to attach a second shell.
func (tr *terminalRegistry) claim(tok string) (*pendingTerminal, bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	p, ok := tr.pending[tok]
	if !ok {
		return nil, false
	}
	delete(tr.pending, tok)
	if time.Since(p.created) > terminalTokenTTL {
		tr.drop(p.machineID)
		return nil, false
	}
	return p, true
}

// release gives back a session slot.
func (tr *terminalRegistry) release(tok string, machineID int64) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if _, ok := tr.pending[tok]; ok {
		delete(tr.pending, tok)
	}
	tr.drop(machineID)
}

// drop decrements the counters. Caller holds the lock.
func (tr *terminalRegistry) drop(machineID int64) {
	if tr.perMachine[machineID] > 0 {
		tr.perMachine[machineID]--
		if tr.perMachine[machineID] == 0 {
			delete(tr.perMachine, machineID)
		}
	}
	if tr.total > 0 {
		tr.total--
	}
}

type terminalError string

func (e terminalError) Error() string { return string(e) }

const (
	errTooManyTerminals     = terminalError("面板同时打开的终端过多，请先关闭一个")
	errTooManyTerminalsHere = terminalError("这台机器同时打开的终端过多，请先关闭一个")
)

// notify sends a control frame to a terminal peer.
func notify(conn *websocket.Conn, c proto.TerminalControl) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(terminalWriteWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, raw)
}

// handleBrowserTerminal opens a terminal for an authenticated operator.
func (s *Server) handleBrowserTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.opts.TerminalEnabled {
		writeErr(w, http.StatusForbidden, "此面板已禁用网页终端")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	view, exists := s.hub.View(id, time.Now())
	if !exists {
		writeErr(w, http.StatusNotFound, "机器不存在")
		return
	}

	// Upgrade before reporting the remaining failures, so they can be shown as
	// text inside the terminal pane instead of surfacing as an unexplained
	// WebSocket error.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(terminalMaxFrame)

	agent, online := s.hub.Conn(id)
	if !online {
		_ = notify(conn, proto.TerminalControl{
			Type: proto.TerminalNotice, Message: "机器当前离线，无法打开终端"})
		return
	}
	if !view.Host.TerminalEnabled {
		_ = notify(conn, proto.TerminalControl{Type: proto.TerminalNotice,
			Message: "该机器的 agent 未启用终端。加上 --allow-terminal 重启 agent 后可用。"})
		return
	}

	cols, rows := proto.ClampTerminalSize(
		uint16(atoiDefault(r.URL.Query().Get("cols"), 80)),
		uint16(atoiDefault(r.URL.Query().Get("rows"), 24)))

	tok, pend, err := s.terminals.reserve(id)
	if err != nil {
		_ = notify(conn, proto.TerminalControl{
			Type: proto.TerminalNotice, Message: err.Error()})
		return
	}
	defer s.terminals.release(tok, id)

	ip := clientIP(r)
	// Audit line before the shell exists, so an attempt is recorded even if it
	// then fails. A terminal that opened and a terminal that tried are both
	// worth knowing about.
	s.log.Info("terminal requested",
		slog.Int64("machine", id), slog.String("name", view.Name),
		slog.String("ip", ip))

	res, err := agent.OpenTerminal(r.Context(), proto.TerminalOpen{
		Session: tok, Cols: cols, Rows: rows})
	if err != nil {
		_ = notify(conn, proto.TerminalControl{Type: proto.TerminalNotice,
			Message: "agent 没有响应终端请求：" + err.Error()})
		return
	}
	if !res.OK {
		_ = notify(conn, proto.TerminalControl{Type: proto.TerminalNotice,
			Message: "agent 拒绝打开终端：" + res.Error})
		return
	}

	var agentWS *websocket.Conn
	select {
	case agentWS = <-pend.deliver:
	case <-time.After(terminalDialWait):
		_ = notify(conn, proto.TerminalControl{Type: proto.TerminalNotice,
			Message: "agent 已接受请求但没有连回面板，可能是网络问题"})
		return
	case <-r.Context().Done():
		return
	}
	defer agentWS.Close()

	_ = notify(conn, proto.TerminalControl{
		Type: proto.TerminalReady, Shell: res.Shell})

	started := time.Now()
	s.log.Info("terminal opened",
		slog.Int64("machine", id), slog.String("name", view.Name),
		slog.String("ip", ip), slog.String("shell", res.Shell))

	reason := bridge(conn, agentWS)

	s.log.Info("terminal closed",
		slog.Int64("machine", id), slog.String("name", view.Name),
		slog.String("ip", ip),
		slog.Duration("duration", time.Since(started).Round(time.Second)),
		slog.String("reason", reason))
}

// handleAgentTerminal receives an agent's dial-back and hands it to the waiting
// browser handler.
func (s *Server) handleAgentTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.authAgent(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tok := r.Header.Get(proto.TerminalSessionHeader)
	if tok == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}
	pend, ok := s.terminals.claim(tok)
	if !ok {
		// Either a replay, a token that expired, or a browser that gave up.
		http.Error(w, "unknown or expired session", http.StatusGone)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.terminals.release(tok, pend.machineID)
		return
	}
	conn.SetReadLimit(terminalMaxFrame)

	select {
	case pend.deliver <- conn:
		// The browser handler owns the connection now and will close it.
	default:
		conn.Close()
		s.terminals.release(tok, pend.machineID)
	}
}

// bridge copies frames between the browser and the agent until either end
// stops, and reports which end ended it.
//
// Frame types are preserved: binary stays binary so terminal bytes are never
// reinterpreted, text stays text so control messages arrive as control
// messages. The bridge does not parse either — it does not need to know what a
// resize is to forward one.
func bridge(browser, agent *websocket.Conn) string {
	// idle carries a reset signal from the browser-to-agent direction.
	idle := time.NewTimer(terminalIdleTimeout)
	defer idle.Stop()
	activity := make(chan struct{}, 1)

	done := make(chan string, 2)

	go func() {
		for {
			typ, data, err := browser.ReadMessage()
			if err != nil {
				done <- "browser closed"
				return
			}
			select {
			case activity <- struct{}{}:
			default:
			}
			if err := agent.SetWriteDeadline(time.Now().Add(terminalWriteWait)); err != nil {
				done <- "agent write failed"
				return
			}
			if err := agent.WriteMessage(typ, data); err != nil {
				done <- "agent write failed"
				return
			}
		}
	}()

	go func() {
		for {
			typ, data, err := agent.ReadMessage()
			if err != nil {
				done <- "shell exited"
				return
			}
			if err := browser.SetWriteDeadline(time.Now().Add(terminalWriteWait)); err != nil {
				done <- "browser write failed"
				return
			}
			if err := browser.WriteMessage(typ, data); err != nil {
				done <- "browser write failed"
				return
			}
		}
	}()

	for {
		select {
		case reason := <-done:
			return reason
		case <-activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(terminalIdleTimeout)
		case <-idle.C:
			_ = notify(browser, proto.TerminalControl{Type: proto.TerminalNotice,
				Message: "\r\n[终端因长时间无输入已关闭]\r\n"})
			// Closing both ends unblocks the copy goroutines, which is what lets
			// them exit rather than leaking for the life of the process.
			browser.Close()
			agent.Close()
			return "idle timeout"
		}
	}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return def
		}
		n = n*10 + int(s[i]-'0')
		if n > 65535 {
			return def
		}
	}
	return n
}
