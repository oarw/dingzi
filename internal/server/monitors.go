package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

const maxMonitors = 200

type Monitor struct {
	ID              int64          `json:"id"`
	Name            string         `json:"name"`
	ServerID        int64          `json:"server_id"`
	Type            string         `json:"type"`
	Target          string         `json:"target"`
	IntervalSeconds int            `json:"interval_seconds"`
	TimeoutMS       int            `json:"timeout_ms"`
	Enabled         bool           `json:"enabled"`
	Revision        int64          `json:"revision"`
	Last            *MonitorResult `json:"last"`
	Uptime          *float64       `json:"uptime"`
	Checks          int64          `json:"checks"`
}

type MonitorResult struct {
	At         int64   `json:"at"`
	Status     string  `json:"status"`
	LatencyMS  float64 `json:"latency_ms"`
	Loss       float64 `json:"loss"`
	StatusCode int     `json:"status_code"`
	Error      string  `json:"error"`
}

const monitorColumns = `id,name,server_id,type,target,interval_seconds,timeout_ms,enabled,revision`

func scanMonitor(row interface{ Scan(...any) error }) (Monitor, error) {
	var m Monitor
	err := row.Scan(&m.ID, &m.Name, &m.ServerID, &m.Type, &m.Target, &m.IntervalSeconds, &m.TimeoutMS, &m.Enabled, &m.Revision)
	return m, err
}

func (s *Store) monitors(ctx context.Context) ([]Monitor, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+monitorColumns+" FROM monitors ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Monitor, 0)
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) monitor(ctx context.Context, id int64) (Monitor, error) {
	return scanMonitor(s.db.QueryRowContext(ctx, "SELECT "+monitorColumns+" FROM monitors WHERE id=?", id))
}

func (s *Store) monitorLast(ctx context.Context, id int64) (*MonitorResult, error) {
	var p MonitorResult
	err := s.db.QueryRowContext(ctx, `SELECT at,status,latency_ms,loss,status_code,error
 FROM monitor_results WHERE monitor_id=? ORDER BY at DESC,id DESC LIMIT 1`, id).
		Scan(&p.At, &p.Status, &p.LatencyMS, &p.Loss, &p.StatusCode, &p.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func validHTTPURL(raw string, httpsOnly bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == "" &&
		(u.Scheme == "https" || (!httpsOnly && u.Scheme == "http"))
}

func (m *Monitor) validate() error {
	m.Name, m.Target = strings.TrimSpace(m.Name), strings.TrimSpace(m.Target)
	if len([]rune(m.Name)) < 1 || len([]rune(m.Name)) > 64 {
		return errors.New("名称需要 1 到 64 个字符")
	}
	if m.ServerID <= 0 {
		return errors.New("请选择执行探针")
	}
	if len(m.Target) == 0 || len(m.Target) > 2048 {
		return errors.New("目标长度需要 1 到 2048 字节")
	}
	if m.IntervalSeconds < 10 || m.IntervalSeconds > 86400 {
		return errors.New("检查间隔需要 10 到 86400 秒")
	}
	if m.TimeoutMS < 100 || m.TimeoutMS > 60000 || m.TimeoutMS > m.IntervalSeconds*1000 {
		return errors.New("超时需要 100 到 60000 毫秒，且不超过检查间隔")
	}
	switch m.Type {
	case proto.TaskHTTP:
		if !validHTTPURL(m.Target, false) {
			return errors.New("请输入完整的 HTTP 或 HTTPS 地址，不含账号密码")
		}
	case proto.TaskTCP:
		host, port, err := net.SplitHostPort(m.Target)
		n, conv := strconv.Atoi(port)
		if err != nil || host == "" || conv != nil || n < 1 || n > 65535 {
			return errors.New("TCP 目标需要主机名:端口，IPv6 地址用方括号包围")
		}
	case proto.TaskPing:
		if strings.ContainsAny(m.Target, "/ \\?#\t\r\n") {
			return errors.New("Ping 目标需要主机名或 IP")
		}
	default:
		return errors.New("检查类型只能是 http、tcp 或 ping")
	}
	return nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		writeErr(w, 400, "请求格式不正确")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeErr(w, 400, "请求只允许一个 JSON 对象")
		return false
	}
	return true
}

func (s *Server) featureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/monitors", s.requireAuth(s.handleMonitors))
	mux.HandleFunc("POST /api/v1/monitors", s.requireAuth(s.handleSaveMonitor))
	mux.HandleFunc("PUT /api/v1/monitors/{id}", s.requireAuth(s.handleSaveMonitor))
	mux.HandleFunc("DELETE /api/v1/monitors/{id}", s.requireAuth(s.handleDeleteMonitor))
	mux.HandleFunc("POST /api/v1/monitors/{id}/run", s.requireAuth(s.handleRunMonitor))
	mux.HandleFunc("GET /api/v1/monitors/{id}/history", s.requireAuth(s.handleMonitorHistory))
	s.alertRoutes(mux)
}

func (s *Server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	items, err := s.store.monitors(ctx)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	for i := range items {
		m := &items[i]
		m.Last, err = s.store.monitorLast(ctx, m.ID)
		if err == nil {
			err = s.store.db.QueryRowContext(ctx, `SELECT COUNT(*),
 AVG(CASE WHEN status='up' THEN 100.0 WHEN status='down' THEN 0.0 END)
 FROM monitor_results WHERE monitor_id=? AND at>=?`, m.ID, time.Now().Add(-24*time.Hour).Unix()).Scan(&m.Checks, &m.Uptime)
		}
		if err != nil {
			respondStoreErr(w, s, err)
			return
		}
		if m.Last != nil && time.Now().Unix()-m.Last.At > int64(m.IntervalSeconds*2+60) {
			m.Last.Status = "unknown"
			m.Last.Error = "尚无近期检查结果"
		}
	}
	writeJSON(w, 200, map[string]any{"monitors": items})
}

func (s *Server) handleSaveMonitor(w http.ResponseWriter, r *http.Request) {
	var m Monitor
	if !decodeBody(w, r, &m) {
		return
	}
	if err := m.validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, exists := s.hub.View(m.ServerID, time.Now()); !exists {
		writeErr(w, 400, "执行探针不存在")
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	defer tx.Rollback()
	if r.Method == "POST" {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM monitors").Scan(&count)
		if err == nil && count >= maxMonitors {
			writeErr(w, 400, "最多可创建 200 条服务监控")
			return
		}
		if err == nil {
			var res sql.Result
			res, err = tx.ExecContext(ctx, `INSERT INTO monitors(name,server_id,type,target,interval_seconds,timeout_ms,enabled) VALUES(?,?,?,?,?,?,?)`,
				m.Name, m.ServerID, m.Type, m.Target, m.IntervalSeconds, m.TimeoutMS, m.Enabled)
			if err == nil {
				m.ID, err = res.LastInsertId()
			}
		}
	} else {
		var ok bool
		m.ID, ok = pathID(w, r)
		if !ok {
			return
		}
		old, e := scanMonitor(tx.QueryRowContext(ctx, "SELECT "+monitorColumns+" FROM monitors WHERE id=?", m.ID))
		if errors.Is(e, sql.ErrNoRows) {
			writeErr(w, 404, "监控不存在")
			return
		}
		err = e
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE monitors SET name=?,server_id=?,type=?,target=?,interval_seconds=?,timeout_ms=?,enabled=?,revision=revision+1 WHERE id=?`,
				m.Name, m.ServerID, m.Type, m.Target, m.IntervalSeconds, m.TimeoutMS, m.Enabled, m.ID)
		}
		if err == nil && (old.ServerID != m.ServerID || old.Target != m.Target || old.Type != m.Type) {
			_, err = tx.ExecContext(ctx, "DELETE FROM monitor_results WHERE monitor_id=?", m.ID)
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM alert_states WHERE rule_id IN (SELECT id FROM alert_rules WHERE monitor_id=?)", m.ID)
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": m.ID})
}

func (s *Server) handleDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	if err := s.store.affectOne(ctx, "DELETE FROM monitors WHERE id=?", id); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMonitorHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	if _, err := s.store.monitor(ctx, id); errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "监控不存在")
		return
	} else if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT at,status,latency_ms,loss,status_code,error
 FROM monitor_results WHERE monitor_id=? ORDER BY at DESC,id DESC LIMIT 500`, id)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	defer rows.Close()
	points := make([]MonitorResult, 0)
	for rows.Next() {
		var p MonitorResult
		if err := rows.Scan(&p.At, &p.Status, &p.LatencyMS, &p.Loss, &p.StatusCode, &p.Error); err != nil {
			respondStoreErr(w, s, err)
			return
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"points": points})
}

type scheduledMonitor struct {
	revision int64
	at       time.Time
}

type serviceEngine struct {
	s       *Server
	mu      sync.Mutex
	running map[int64]bool
	next    map[int64]scheduledMonitor
	slots   chan struct{}
	wg      sync.WaitGroup
	client  *http.Client
}

func newServiceEngine(s *Server) *serviceEngine {
	return &serviceEngine{s: s, running: map[int64]bool{}, next: map[int64]scheduledMonitor{}, slots: make(chan struct{}, 16),
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (e *serviceEngine) acquire(id int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[id] {
		return false
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return false
	}
	e.running[id] = true
	return true
}

func (e *serviceEngine) release(id int64) {
	e.mu.Lock()
	delete(e.running, id)
	e.mu.Unlock()
	<-e.slots
}

func (e *serviceEngine) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer e.wg.Wait()
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.notifications(ctx) }()
	var lastEvaluation time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e.schedule(ctx, now)
			if now.Sub(lastEvaluation) >= 5*time.Second {
				lastEvaluation = now
				if err := e.evaluate(ctx, now); err != nil && ctx.Err() == nil {
					e.s.log.Warn("evaluating alerts failed", "error", err)
				}
			}
		}
	}
}

func (e *serviceEngine) schedule(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	items, err := e.s.store.monitors(qctx)
	if err != nil {
		if ctx.Err() == nil {
			e.s.log.Warn("loading monitors failed", "error", err)
		}
		return
	}
	known := map[int64]bool{}
	for _, m := range items {
		known[m.ID] = true
		due := e.next[m.ID]
		if !m.Enabled || (due.revision == m.Revision && now.Before(due.at)) {
			continue
		}
		if !e.acquire(m.ID) {
			continue
		}
		e.next[m.ID] = scheduledMonitor{revision: m.Revision, at: now.Add(time.Duration(m.IntervalSeconds) * time.Second)}
		e.wg.Add(1)
		go func(m Monitor) {
			defer e.wg.Done()
			defer e.release(m.ID)
			if _, err := e.check(ctx, m); err != nil && ctx.Err() == nil {
				e.s.log.Warn("saving monitor result failed", "monitor", m.ID, "error", err)
			}
		}(m)
	}
	for id := range e.next {
		if !known[id] {
			delete(e.next, id)
		}
	}
}

func (e *serviceEngine) check(ctx context.Context, m Monitor) (MonitorResult, error) {
	p := MonitorResult{At: time.Now().Unix(), Status: "unknown", Error: "执行探针离线"}
	v, exists := e.s.hub.View(m.ServerID, time.Now())
	if c, ok := e.s.hub.Conn(m.ServerID); exists && v.Online && ok {
		tctx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutMS)*time.Millisecond+5*time.Second)
		res, err := c.Dispatch(tctx, proto.Task{MonitorID: m.ID, Type: m.Type, Target: m.Target, TimeoutMS: m.TimeoutMS})
		cancel()
		if err != nil {
			p.Error = "探针未返回检查结果"
		} else {
			p.Status = "down"
			if res.OK {
				p.Status = "up"
			}
			p.LatencyMS, p.Loss, p.StatusCode, p.Error = res.LatencyMS, res.Loss, res.StatusCode, res.Error
			if !finite(p.LatencyMS) || !finite(p.Loss) || p.LatencyMS < 0 || p.Loss < 0 || p.Loss > 100 {
				p = MonitorResult{At: p.At, Status: "unknown", Error: "探针返回无效数据"}
			}
			if strings.Contains(p.Error, "权限不足") || strings.Contains(p.Error, "并发已满") {
				p.Status = "unknown"
			}
		}
	}
	if ctx.Err() != nil {
		return p, ctx.Err()
	}
	p.Error = truncate(p.Error, 512)
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := e.s.store.db.ExecContext(qctx, `INSERT INTO monitor_results(monitor_id,at,status,latency_ms,loss,status_code,error)
 SELECT id,?,?,?,?,?,? FROM monitors WHERE id=? AND revision=?`, p.At, p.Status, p.LatencyMS, p.Loss, p.StatusCode, p.Error, m.ID, m.Revision)
	return p, err
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Server) handleRunMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.store.monitor(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "监控不存在")
		return
	}
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	if !s.services.acquire(id) {
		writeErr(w, 409, "检查正在运行，请稍后重试")
		return
	}
	defer s.services.release(id)
	p, err := s.services.check(r.Context(), m)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, p)
}
