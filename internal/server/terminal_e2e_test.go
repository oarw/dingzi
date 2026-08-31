//go:build unix

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/agent"
	"github.com/oarw/dingzi/internal/proto"
)

// This exercises the whole terminal path in one process: panel, agent, a real
// pty running a real shell, and the WebSocket bridge between them. It is the
// only test that can catch the failures that matter most here — a pty that never
// attaches, a shell that starts but produces nothing, a bridge that drops the
// frame type — because every one of those passes a unit test of its own parts.
//
// Unix only, since that is where startPTY does anything.

const (
	testAgentSecret = "test-agent-secret-value-32-bytes-x"
	testPassword    = "test-panel-password"
)

// harness is a running panel with one connected agent.
type harness struct {
	t      *testing.T
	http   *httptest.Server
	server *Server
	store  *Store
	cookie *http.Cookie
	id     int64
}

func newHarness(t *testing.T, allowTerminal, panelTerminal bool) *harness {
	t.Helper()

	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := OpenStore(filepath.Join(dir, "test.db"), log)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	srv, err := New(Options{
		AgentSecret:     testAgentSecret,
		PasswordHash:    hash,
		Interval:        0.5,
		TerminalEnabled: panelTerminal,
	}, store, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	h := &harness{t: t, http: ts, server: srv, store: store}
	h.startAgent(dir, allowTerminal)
	h.login()
	return h
}

// startAgent runs a real agent against the test panel.
func (h *harness) startAgent(dir string, allowTerminal bool) {
	h.t.Helper()

	cfg, err := agent.LoadConfig(filepath.Join(dir, "agent.yaml"), agent.Config{
		Server:        h.http.URL,
		Secret:        testAgentSecret,
		Name:          "e2e-terminal-box",
		AllowTerminal: allowTerminal,
	})
	if err != nil {
		h.t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		h.t.Fatalf("Validate: %v", err)
	}
	if _, err := cfg.EnsureUUID(); err != nil {
		h.t.Fatalf("EnsureUUID: %v", err)
	}

	col, err := agent.NewCollector(nil, nil)
	if err != nil {
		h.t.Fatalf("NewCollector: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := agent.NewClient(cfg, col, "0.0.0-test", log)

	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	go client.Run(ctx)

	// Wait for the agent to register and report at least one sample, so the
	// machine is genuinely online rather than merely known.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		views := h.server.hub.Snapshot(time.Now())
		if len(views) == 1 && views[0].Online && views[0].HasNow {
			h.id = views[0].ID
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatal("agent did not come online within 20s")
}

func (h *harness) login() {
	h.t.Helper()
	body := strings.NewReader(`{"password":"` + testPassword + `"}`)
	resp, err := http.Post(h.http.URL+"/api/v1/login", "application/json", body)
	if err != nil {
		h.t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login: HTTP %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			h.cookie = c
			return
		}
	}
	h.t.Fatal("login returned no session cookie")
}

// dialTerminal opens the browser side of a terminal.
func (h *harness) dialTerminal(authed bool) (*websocket.Conn, *http.Response, error) {
	u, _ := url.Parse(h.http.URL)
	u.Scheme = "ws"
	u.Path = fmt.Sprintf("/api/v1/servers/%d/terminal", h.id)
	u.RawQuery = "cols=100&rows=30"

	hdr := http.Header{}
	if authed && h.cookie != nil {
		hdr.Set("Cookie", h.cookie.Name+"="+h.cookie.Value)
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	return d.Dial(u.String(), hdr)
}

// readControl reads text frames until one arrives, returning it. Binary frames
// are shell output and are returned through out.
func readControl(
	t *testing.T, conn *websocket.Conn, out *bytes.Buffer, within time.Duration,
) proto.TerminalControl {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for a control frame: %v", err)
		}
		if typ == websocket.BinaryMessage {
			out.Write(data)
			continue
		}
		var c proto.TerminalControl
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("undecodable control frame %q: %v", data, err)
		}
		return c
	}
}

// TestTerminalEndToEnd opens a shell, runs a command and reads its output.
func TestTerminalEndToEnd(t *testing.T) {
	if os.Getenv("CI") == "" && testing.Short() {
		t.Skip("starts a real shell; run without -short")
	}
	h := newHarness(t, true, true)

	conn, _, err := h.dialTerminal(true)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close()

	var out bytes.Buffer
	ready := readControl(t, conn, &out, 25*time.Second)
	if ready.Type == proto.TerminalNotice {
		t.Fatalf("terminal refused: %s", ready.Message)
	}
	if ready.Type != proto.TerminalReady {
		t.Fatalf("first control frame = %q, want %q", ready.Type, proto.TerminalReady)
	}
	if ready.Shell == "" {
		t.Error("ready frame carries no shell name")
	}
	t.Logf("shell attached: %s", ready.Shell)

	// The command's output differs from its own echo, so finding it proves the
	// shell actually ran it rather than the terminal merely echoing keystrokes.
	if err := conn.WriteMessage(websocket.BinaryMessage,
		[]byte("echo DZ_$((21*2))_END\n")); err != nil {
		t.Fatalf("write command: %v", err)
	}

	const want = "DZ_42_END"
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("shell output never contained %q; got:\n%s", want, out.String())
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading shell output: %v (so far: %q)", err, out.String())
		}
		if typ == websocket.BinaryMessage {
			out.Write(data)
		}
	}
}

// A resize must reach the pty. If it does not, full-screen tools draw into the
// wrong box, which is the single most common way a web terminal is subtly broken.
func TestTerminalResizeReachesPTY(t *testing.T) {
	if os.Getenv("CI") == "" && testing.Short() {
		t.Skip("starts a real shell; run without -short")
	}
	h := newHarness(t, true, true)

	conn, _, err := h.dialTerminal(true)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close()

	var out bytes.Buffer
	if c := readControl(t, conn, &out, 25*time.Second); c.Type != proto.TerminalReady {
		t.Fatalf("not ready: %+v", c)
	}

	const cols, rows = 133, 41
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(fmt.Sprintf(`{"type":"resize","cols":%d,"rows":%d}`, cols, rows))); err != nil {
		t.Fatalf("send resize: %v", err)
	}
	// Give the resize time to land before asking the shell what it thinks.
	time.Sleep(500 * time.Millisecond)

	// stty reports the pty's own idea of its size, which is the thing under test
	// rather than anything the panel is tracking.
	//
	// The busybox fallback matters: an Alpine image has the stty applet but no
	// /bin/stty symlink, so the plain call fails there — and Alpine is the case
	// this feature exists for, so skipping the assertion there would leave the
	// most likely breakage unverified.
	// The "no stty" sentinel is computed by the shell rather than written
	// literally, because the pty echoes the command back: a literal sentinel
	// would appear in that echo and be detected before the command had run.
	if err := conn.WriteMessage(websocket.BinaryMessage,
		[]byte("stty size 2>/dev/null || busybox stty size 2>/dev/null || "+
			"echo NOSTTY$((41+1))\n"),
	); err != nil {
		t.Fatalf("write command: %v", err)
	}

	want := fmt.Sprintf("%d %d", rows, cols)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		if strings.Contains(out.String(), "NOSTTY42") {
			t.Skip("no stty on this image, cannot verify the pty size")
		}
		if time.Now().After(deadline) {
			t.Fatalf("pty size never reported %q; output was:\n%s", want, out.String())
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading output: %v (so far: %q)", err, out.String())
		}
		if typ == websocket.BinaryMessage {
			out.Write(data)
		}
	}
}

// An agent without --allow-terminal must refuse, and the refusal must arrive as
// a readable message. This is the security boundary, so it gets a test that
// fails loudly if it ever stops holding.
func TestTerminalRefusedWithoutAgentOptIn(t *testing.T) {
	h := newHarness(t, false, true)

	conn, _, err := h.dialTerminal(true)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.Close()

	var out bytes.Buffer
	c := readControl(t, conn, &out, 20*time.Second)
	if c.Type != proto.TerminalNotice {
		t.Fatalf("control frame = %+v, want a notice refusing the terminal", c)
	}
	if c.Message == "" {
		t.Error("refusal carries no message, so the operator sees a blank pane")
	}
	t.Logf("refusal: %s", c.Message)
}

// The panel-wide switch must also hold, and before the upgrade, so a client
// gets an HTTP status rather than a socket that opens and immediately says no.
func TestTerminalRefusedWhenPanelDisabled(t *testing.T) {
	h := newHarness(t, true, false)

	_, resp, err := h.dialTerminal(true)
	if err == nil {
		t.Fatal("dial succeeded with terminals disabled panel-wide")
	}
	if resp == nil {
		t.Fatalf("dial failed with no HTTP response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("HTTP %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

// An unauthenticated terminal request must be rejected before the upgrade.
func TestTerminalRequiresSession(t *testing.T) {
	h := newHarness(t, true, true)

	_, resp, err := h.dialTerminal(false)
	if err == nil {
		t.Fatal("dial succeeded without a session cookie")
	}
	if resp == nil {
		t.Fatalf("dial failed with no HTTP response: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// A session token is minted for one dial-back. Replaying it must fail, or a
// leaked token would be usable to attach a second shell.
func TestAgentTerminalRejectsUnknownToken(t *testing.T) {
	h := newHarness(t, true, true)

	u, _ := url.Parse(h.http.URL)
	u.Scheme = "ws"
	u.Path = proto.TerminalAgentPath

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+testAgentSecret)
	hdr.Set(proto.TerminalSessionHeader, "not-a-real-token")

	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	_, resp, err := d.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("dial-back succeeded with a token that was never issued")
	}
	if resp == nil || resp.StatusCode != http.StatusGone {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Errorf("HTTP %d, want %d", code, http.StatusGone)
	}
}

// The agent endpoint must check the agent secret, not just the session token.
func TestAgentTerminalRequiresSecret(t *testing.T) {
	h := newHarness(t, true, true)

	u, _ := url.Parse(h.http.URL)
	u.Scheme = "ws"
	u.Path = proto.TerminalAgentPath

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer wrong-secret")
	hdr.Set(proto.TerminalSessionHeader, "anything")

	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	_, resp, err := d.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("dial-back succeeded with a wrong agent secret")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Errorf("HTTP %d, want %d", code, http.StatusUnauthorized)
	}
}

// The fleet API must report the agent's terminal capability, since that is what
// the UI uses to decide whether to offer the control at all.
func TestServersAPIReportsTerminalCapability(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) {
			h := newHarness(t, allow, true)

			resp, err := http.Get(h.http.URL + "/api/v1/servers")
			if err != nil {
				t.Fatalf("GET servers: %v", err)
			}
			defer resp.Body.Close()

			var payload struct {
				Servers []struct {
					TerminalEnabled bool `json:"terminal_enabled"`
				} `json:"servers"`
				TerminalEnabled bool `json:"terminal_enabled"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(payload.Servers) != 1 {
				t.Fatalf("got %d servers, want 1", len(payload.Servers))
			}
			if payload.Servers[0].TerminalEnabled != allow {
				t.Errorf("server terminal_enabled = %v, want %v",
					payload.Servers[0].TerminalEnabled, allow)
			}
			if !payload.TerminalEnabled {
				t.Error("panel terminal_enabled = false, want true")
			}
		})
	}
}
