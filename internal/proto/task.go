package proto

// Task types.
//
// There is deliberately no shell-execution task. A monitoring panel that can
// run arbitrary commands on every machine it watches turns one panel
// compromise into fleet-wide RCE, and reachability checks cover the actual
// monitoring need. If it is ever added it must be opt-in per agent.
const (
	// TaskPing is an ICMP echo. Reports loss and round-trip time.
	TaskPing = "ping"
	// TaskTCP is a TCP connect. Reports whether the handshake completed and
	// how long it took — no payload is sent.
	TaskTCP = "tcp"
	// TaskHTTP is an HTTP(S) GET. Reports status code and total time.
	TaskHTTP = "http"
)

// Task is work the server asks an agent to perform. The agent answers with a
// [TaskResult] carrying the same [Envelope.ID].
//
// Tasks are stateless and self-contained: an agent that reconnects mid-task
// simply never answers, and the server times the request out. Nothing has to be
// resumed, which is why the agent needs no task persistence.
type Task struct {
	// MonitorID identifies the monitor this task belongs to, so the server can
	// attribute the result after a restart.
	MonitorID int64 `json:"monitor_id"`
	// Type is one of the Task* constants.
	Type string `json:"type"`
	// Target is a hostname or IP for ping, host:port for tcp, a URL for http.
	Target string `json:"target"`
	// TimeoutMS bounds the check. The agent must answer within it, with a
	// failed result if necessary, rather than going silent.
	TimeoutMS int `json:"timeout_ms"`
}

// TaskResult is an agent's answer to a [Task].
//
// Failure is reported in-band rather than by silence: an agent that cannot
// reach a target still answers, with OK false and Error set. Silence means the
// agent itself is gone, which is a different condition and shown differently.
type TaskResult struct {
	MonitorID int64 `json:"monitor_id"`
	// OK is whether the check succeeded, not whether the task ran.
	OK bool `json:"ok"`
	// LatencyMS is round-trip time for ping, connect time for tcp, total time
	// for http. Zero when OK is false.
	LatencyMS float64 `json:"latency_ms"`
	// Loss is packet loss percent, ping only.
	Loss float64 `json:"loss,omitempty"`
	// StatusCode is the HTTP response code, http only.
	StatusCode int `json:"status_code,omitempty"`
	// Error is a short human-readable reason when OK is false. It is shown in
	// the panel, so it names the problem rather than dumping a Go error chain.
	Error string `json:"error,omitempty"`
}

// Valid reports whether the task is well-formed enough to attempt. The agent
// checks this before dispatching so a malformed task produces a clear result
// instead of a confusing network error.
func (t Task) Valid() bool {
	if t.Target == "" || t.TimeoutMS <= 0 {
		return false
	}
	switch t.Type {
	case TaskPing, TaskTCP, TaskHTTP:
		return true
	default:
		return false
	}
}
