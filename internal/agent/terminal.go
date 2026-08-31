package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/oarw/dingzi/internal/proto"
)

// ptySession is a shell attached to a pseudo-terminal.
//
// The interface exists so the platform-specific part is one small file per
// platform rather than build tags scattered through the session logic.
type ptySession interface {
	io.ReadWriteCloser
	// Resize applies a new window size to the pty.
	Resize(cols, rows uint16) error
	// Shell is the interpreter that was started, reported to the panel so an
	// operator can tell a busybox sh from a bash.
	Shell() string
}

const (
	// terminalReadBuf is the chunk size for pty output. Large enough that a
	// build log does not turn into a frame per line, small enough that a
	// keystroke echoes immediately.
	terminalReadBuf = 32 * 1024

	terminalDialTimeout = 15 * time.Second
	terminalWriteWait   = 10 * time.Second
	terminalMaxFrame    = 1 << 20
)

// handleTerminalOpen answers the panel's request for a shell.
//
// It always answers, including to refuse: a silent agent leaves the operator
// looking at a blank pane with no way to tell "terminals are off here" from
// "this agent is wedged".
func (c *Client) handleTerminalOpen(ctx context.Context, reqID string, open proto.TerminalOpen) {
	refuse := func(reason string) {
		if err := c.send(proto.TypeTerminalResult, reqID,
			proto.TerminalResult{OK: false, Error: reason}); err != nil {
			c.log.Warn("could not report terminal refusal", slog.Any("error", err))
		}
	}

	if !c.cfg.AllowTerminal {
		// The panel already checks Host.TerminalEnabled and hides the control,
		// so reaching here means the panel's view is stale or something bypassed
		// it. Refusing here is the check that actually counts.
		c.log.Warn("refused a terminal request: --allow-terminal is not set")
		refuse("这台机器的 agent 没有启用终端（需要 --allow-terminal）")
		return
	}
	if open.Session == "" {
		refuse("终端请求缺少会话标识")
		return
	}

	cols, rows := proto.ClampTerminalSize(open.Cols, open.Rows)

	sess, err := startPTY(cols, rows)
	if err != nil {
		c.log.Warn("could not start a shell", slog.Any("error", err))
		refuse(err.Error())
		return
	}
	// Covers every exit below, including the dial failing after the shell
	// already started.
	defer sess.Close()

	if err := c.send(proto.TypeTerminalResult, reqID,
		proto.TerminalResult{OK: true, Shell: sess.Shell()}); err != nil {
		return
	}

	conn, err := c.dialTerminal(ctx, open.Session)
	if err != nil {
		// The panel is already waiting and will time out with its own message;
		// this log is for the machine's own operator.
		c.log.Warn("terminal dial-back failed", slog.Any("error", err))
		return
	}
	defer conn.Close()
	conn.SetReadLimit(terminalMaxFrame)

	c.log.Info("terminal session started", slog.String("shell", sess.Shell()))
	started := time.Now()
	reason := pumpTerminal(conn, sess)
	c.log.Info("terminal session ended",
		slog.Duration("duration", time.Since(started).Round(time.Second)),
		slog.String("reason", reason))
}

// dialTerminal opens the second WebSocket connection carrying the pty.
func (c *Client) dialTerminal(ctx context.Context, session string) (*websocket.Conn, error) {
	d := websocket.Dialer{
		HandshakeTimeout: terminalDialTimeout,
		Proxy:            http.ProxyFromEnvironment,
	}
	if c.cfg.InsecureSkipVerify {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.cfg.Secret)
	h.Set(proto.TerminalSessionHeader, session)
	h.Set("User-Agent", "dingzi-agent/"+c.version)

	url := c.cfg.TerminalURL()
	conn, resp, err := d.DialContext(ctx, url, h)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s: HTTP %d: %w", url, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	return conn, nil
}

// pumpTerminal copies between the WebSocket and the pty until either ends.
func pumpTerminal(conn *websocket.Conn, sess ptySession) string {
	done := make(chan string, 2)

	// WebSocket to pty: keystrokes and control messages.
	go func() {
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				done <- "panel closed the connection"
				return
			}
			switch typ {
			case websocket.BinaryMessage:
				if _, err := sess.Write(data); err != nil {
					done <- "shell input failed"
					return
				}
			case websocket.TextMessage:
				var ctl proto.TerminalControl
				if err := json.Unmarshal(data, &ctl); err != nil {
					continue
				}
				if ctl.Type == proto.TerminalResize {
					cols, rows := proto.ClampTerminalSize(ctl.Cols, ctl.Rows)
					if err := sess.Resize(cols, rows); err != nil {
						// A failed resize is cosmetic. Ending the session over it
						// would be worse than a briefly wrong window size.
						continue
					}
				}
			}
		}
	}()

	// pty to WebSocket: shell output as binary frames, so bytes that are not
	// valid UTF-8 are delivered exactly as the shell produced them instead of
	// being replaced with question marks.
	go func() {
		buf := make([]byte, terminalReadBuf)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				if werr := conn.SetWriteDeadline(
					time.Now().Add(terminalWriteWait)); werr != nil {
					done <- "panel write failed"
					return
				}
				if werr := conn.WriteMessage(
					websocket.BinaryMessage, buf[:n]); werr != nil {
					done <- "panel write failed"
					return
				}
			}
			if err != nil {
				// A closed pty reads as EIO on Linux rather than EOF. Both mean
				// the shell exited, which is a normal end to a session, not a
				// fault worth reporting as one.
				if errors.Is(err, io.EOF) || isPTYClosed(err) {
					done <- "shell exited"
					return
				}
				done <- "shell read failed: " + err.Error()
				return
			}
		}
	}()

	reason := <-done
	// Closing both ends releases the other goroutine, which would otherwise
	// block on a read for the life of the process.
	_ = conn.Close()
	_ = sess.Close()
	return reason
}
