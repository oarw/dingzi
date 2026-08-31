package proto

// Terminal session support.
//
// # Why a second connection
//
// Terminal traffic does not share the metrics connection. Terminal output is
// bursty and can be large — `cat` a log, run `top`, and one machine produces
// more bytes in a second than a day of samples. That traffic sitting in front
// of a heartbeat is how a monitoring connection dies: the metrics link has a
// read deadline on both ends (45s agent, 60s server), and a burst that delays a
// ping past it tears down the link and marks a healthy machine offline. So
// monitoring stays on its own connection, with its own deadlines, and a
// terminal that floods only harms itself.
//
// # Why the agent dials back
//
// The panel cannot open a connection to an agent: agents sit behind NAT with no
// inbound port, which is the whole reason the metrics connection is
// agent-initiated. So opening a terminal is a two-step dance. The panel sends
// [TerminalOpen] down the existing metrics connection carrying a session token;
// the agent makes a fresh WebSocket connection to [TerminalAgentPath] and
// presents that token; the panel matches the two halves and copies bytes
// between them.
//
// # Security posture
//
// A web terminal is remote command execution. There is no honest way to
// describe it as less than that: a pty can be written to programmatically, so
// "shell session" and "run this command" differ in ceremony, not in
// consequence. This is why the package has no exec task type — see task.go —
// and the terminal does not reintroduce one by the back door.
//
// What makes it defensible is that the agent refuses by default. The operator
// enables it per machine with --allow-terminal, so a compromised panel reaches
// only the machines whose owner deliberately opted in, and the decision lives
// on the machine being risked rather than in the thing that might be
// compromised. [Host.TerminalEnabled] reports that choice so the panel can show
// which machines are exposed.
//
// The rest is bounded blast radius rather than prevention: single-use tokens
// with a short lifetime, a cap on concurrent sessions, an idle timeout, and an
// audit line per session. A stolen live panel session can still open a shell.
// That is a real residual risk and the README says so rather than implying the
// token ceremony fixed it.

// TerminalAgentPath is where an agent dials back to attach a pty to a pending
// session. It is a distinct endpoint from [Path] so the two connection kinds
// cannot be confused for one another by either side.
const TerminalAgentPath = "/api/v1/agent/terminal"

// TerminalSessionHeader carries the single-use session token on the agent's
// dial-back. A header rather than a query parameter, for the same reason the
// agent secret uses one: query strings are recorded in proxy and server access
// logs, and a token in a log file outlives the session it was minted for.
const TerminalSessionHeader = "X-Dingzi-Session"

// Message types for terminal setup. These travel on the metrics connection;
// the terminal payload itself never does.
const (
	// TypeTerminalOpen (server to agent) asks for a shell. Carries
	// [TerminalOpen].
	TypeTerminalOpen = "terminal_open"
	// TypeTerminalResult (agent to server) answers it with [TerminalResult].
	//
	// The agent always answers, including to refuse. Silence would leave the
	// operator watching a blank pane with no way to tell "the agent has
	// terminals disabled" from "the agent is wedged" — two problems with
	// nothing in common.
	TypeTerminalResult = "terminal_result"
)

// Terminal control frame types, sent as JSON text frames on a terminal
// connection.
const (
	// TerminalResize changes the pty window size.
	TerminalResize = "resize"
)

// Size bounds for a pty. A terminal is a display, not a data structure: values
// outside these produce nothing useful and a zero row count makes some shells
// misbehave badly enough to look like a crash.
const (
	TerminalMinCols = 20
	TerminalMinRows = 5
	TerminalMaxCols = 500
	TerminalMaxRows = 200
)

// TerminalOpen asks an agent to start a shell and attach it to a pending
// session.
type TerminalOpen struct {
	// Session is the single-use token to present at [TerminalAgentPath].
	Session string `json:"session"`
	// Cols and Rows size the pty at creation. The browser knows its own size
	// before it asks, and creating the pty at the right size avoids the
	// reflow a shell shows when it has already drawn a prompt at 80x24.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// TerminalResult reports whether a shell started.
type TerminalResult struct {
	OK bool `json:"ok"`
	// Error is a short human-readable reason, shown to the operator verbatim.
	Error string `json:"error,omitempty"`
	// Shell is the interpreter that was started, so the operator can tell a
	// busybox `sh` from a `bash` without probing for it.
	Shell string `json:"shell,omitempty"`
}

// TerminalControl is a JSON text frame on a terminal connection.
//
// Terminal payload bytes travel as WebSocket *binary* frames and control
// messages as *text* frames. The frame type already distinguishes them, so
// there is no length prefix and no escape byte to get wrong — and payload
// bytes stay untouched, which matters because terminal output is not
// necessarily valid UTF-8 and must not be repaired into something else.
type TerminalControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// ClampTerminalSize brings a requested window size into the supported range.
//
// It clamps rather than rejects: a browser reporting an odd size should get a
// usable terminal, not an error dialog. Zero means "unspecified" and takes the
// conventional 80x24.
func ClampTerminalSize(cols, rows uint16) (uint16, uint16) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return clampU16(cols, TerminalMinCols, TerminalMaxCols),
		clampU16(rows, TerminalMinRows, TerminalMaxRows)
}

func clampU16(v, lo, hi uint16) uint16 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
