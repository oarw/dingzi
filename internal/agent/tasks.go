package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/oarw/dingzi/internal/proto"
)

// pingCount is how many echoes a ping task sends. Four is enough to make a loss
// figure meaningful without making the task slow.
const pingCount = 4

// runTask executes one task and reports the outcome.
//
// It always answers, including on failure: silence is reserved for "the agent is
// gone", which the panel displays differently from "the check failed".
func (c *Client) runTask(ctx context.Context, id string, t proto.Task) {
	res := proto.TaskResult{MonitorID: t.MonitorID}

	if !t.Valid() {
		// Reject here rather than attempting it, so a malformed task produces a
		// clear reason instead of a confusing network error.
		res.Error = fmt.Sprintf("拒绝无效任务: type=%q target=%q timeout=%dms",
			t.Type, t.Target, t.TimeoutMS)
		c.reply(id, res)
		return
	}

	timeout := time.Duration(t.TimeoutMS) * time.Millisecond
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch t.Type {
	case proto.TaskPing:
		res = c.doPing(tctx, t, timeout)
	case proto.TaskTCP:
		res = c.doTCP(tctx, t)
	case proto.TaskHTTP:
		res = c.doHTTP(tctx, t)
	}
	res.MonitorID = t.MonitorID
	c.reply(id, res)
}

func (c *Client) reply(id string, res proto.TaskResult) {
	if err := c.send(proto.TypeTaskResult, id, res); err != nil {
		// The connection is already failing; the read loop will notice and
		// rebuild it. Nothing to retry here — the server times the task out.
		c.log.Debug("task result not delivered", slog.Any("error", err))
	}
}

// doPing sends ICMP echoes.
//
// Unprivileged ICMP needs either a raw socket (root/CAP_NET_RAW) or a datagram
// socket, and which one works varies by platform and sysctl. The failure is
// reported as itself rather than as an unreachable host, because "the agent
// lacks permission" and "the target is down" are opposite conclusions.
func (c *Client) doPing(ctx context.Context, t proto.Task, timeout time.Duration) proto.TaskResult {
	res := proto.TaskResult{}

	p, err := probing.NewPinger(t.Target)
	if err != nil {
		res.Error = "无法解析目标: " + t.Target
		return res
	}
	p.Count = pingCount
	p.Timeout = timeout
	// Unprivileged datagram-socket ICMP works on Windows and on Linux where
	// net.ipv4.ping_group_range permits it, and avoids requiring root.
	p.SetPrivileged(false)
	// Spread the echoes across the budget rather than firing them back to back,
	// so loss reflects the path rather than one instant of it.
	if timeout > time.Duration(pingCount)*200*time.Millisecond {
		p.Interval = timeout / time.Duration(pingCount+1)
	}

	runErr := p.RunWithContext(ctx)
	st := p.Statistics()

	if runErr != nil && st.PacketsRecv == 0 {
		if isPermissionError(runErr) {
			res.Error = "ICMP 权限不足(需要 CAP_NET_RAW 或放开 ping_group_range)"
		} else {
			res.Error = shortErr(runErr)
		}
		res.Loss = 100
		return res
	}
	if st.PacketsRecv == 0 {
		res.Error = "全部丢包"
		res.Loss = 100
		return res
	}
	res.OK = true
	res.LatencyMS = round2(float64(st.AvgRtt) / float64(time.Millisecond))
	res.Loss = round2(st.PacketLoss)
	return res
}

func isPermissionError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "permission") ||
		strings.Contains(s, "not permitted") ||
		strings.Contains(s, "socket: operation not permitted")
}

// doTCP measures how long a TCP handshake takes. No payload is sent, so this
// does not disturb the service being probed.
func (c *Client) doTCP(ctx context.Context, t proto.Task) proto.TaskResult {
	res := proto.TaskResult{}
	target := t.Target
	if _, _, err := net.SplitHostPort(target); err != nil {
		res.Error = "目标需要 host:port 格式: " + target
		return res
	}

	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		res.Error = shortErr(err)
		return res
	}
	res.LatencyMS = round2(float64(time.Since(start)) / float64(time.Millisecond))
	// Close immediately: holding the connection open would occupy a slot on the
	// probed service for no benefit.
	_ = conn.Close()
	res.OK = true
	return res
}

// doHTTP issues a GET and reports the status code and total time.
func (c *Client) doHTTP(ctx context.Context, t proto.Task) proto.TaskResult {
	res := proto.TaskResult{}
	u, err := url.Parse(t.Target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		res.Error = "目标需要完整 URL: " + t.Target
		return res
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		res.Error = "仅支持 http/https: " + u.Scheme
		return res
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Target, nil)
	if err != nil {
		res.Error = shortErr(err)
		return res
	}
	req.Header.Set("User-Agent", "dingzi-agent/"+c.version+" (monitor)")

	client := &http.Client{
		// The task context already bounds this; the field guards against a
		// context that somehow outlives it.
		Timeout: time.Duration(t.TimeoutMS) * time.Millisecond,
		Transport: &http.Transport{
			// A monitor that reuses connections measures a warm path, not the
			// one a real visitor takes.
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Error = shortErr(err)
		return res
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection closes cleanly and the timing
	// includes the response actually arriving, not just its headers. Capped
	// because a monitor must not pull a large body on every check.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	res.LatencyMS = round2(float64(time.Since(start)) / float64(time.Millisecond))
	res.StatusCode = resp.StatusCode
	// 2xx and 3xx are up. A 4xx or 5xx means the server answered but the
	// service is not healthy, which is a failure with a useful status code.
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !res.OK {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res
}

// shortErr trims a Go error chain down to something a panel can display.
// "dial tcp 10.0.0.1:443: connect: connection refused" is useful; the full
// wrapped chain with url.Error prefixes is not.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s)-i > 4 {
		tail := s[i+2:]
		// Only take the tail when it is a real message rather than a bare host.
		if !strings.Contains(tail, "/") && len(tail) < 60 {
			return tail
		}
	}
	const maxLen = 120
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}
