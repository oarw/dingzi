package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type NotificationChannel struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Token      string `json:"token,omitempty"`
	ChatID     string `json:"chat_id"`
	Enabled    bool   `json:"enabled"`
	TokenSet   bool   `json:"token_set"`
	ClearToken bool   `json:"clear_token,omitempty"`
}

type AlertRule struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	ServerID        int64   `json:"server_id"`
	MonitorID       int64   `json:"monitor_id"`
	Metric          string  `json:"metric"`
	Threshold       float64 `json:"threshold"`
	DurationSeconds int     `json:"duration_seconds"`
	ChannelID       int64   `json:"channel_id"`
	Enabled         bool    `json:"enabled"`
	Revision        int64   `json:"revision"`
	Since           int64   `json:"since"`
	Active          bool    `json:"active"`
	LastEval        int64   `json:"-"`
}

type AlertEvent struct {
	ID            int64  `json:"id"`
	RuleID        *int64 `json:"rule_id"`
	At            int64  `json:"at"`
	State         string `json:"state"`
	Message       string `json:"message"`
	ChannelID     *int64 `json:"channel_id"`
	Delivery      string `json:"delivery"`
	Attempts      int    `json:"attempts"`
	DeliveryError string `json:"delivery_error"`
}

func (s *Server) alertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/channels", s.requireAuth(s.handleChannels))
	mux.HandleFunc("POST /api/v1/channels", s.requireAuth(s.handleSaveChannel))
	mux.HandleFunc("PUT /api/v1/channels/{id}", s.requireAuth(s.handleSaveChannel))
	mux.HandleFunc("DELETE /api/v1/channels/{id}", s.requireAuth(s.handleDeleteChannel))
	mux.HandleFunc("POST /api/v1/channels/{id}/test", s.requireAuth(s.handleTestChannel))
	mux.HandleFunc("GET /api/v1/alert-rules", s.requireAuth(s.handleRules))
	mux.HandleFunc("POST /api/v1/alert-rules", s.requireAuth(s.handleSaveRule))
	mux.HandleFunc("PUT /api/v1/alert-rules/{id}", s.requireAuth(s.handleSaveRule))
	mux.HandleFunc("DELETE /api/v1/alert-rules/{id}", s.requireAuth(s.handleDeleteRule))
	mux.HandleFunc("GET /api/v1/alert-events", s.requireAuth(s.handleEvents))
}

func (s *Store) channel(ctx context.Context, id int64) (NotificationChannel, error) {
	var c NotificationChannel
	err := s.db.QueryRowContext(ctx, "SELECT id,name,type,url,token,chat_id,enabled FROM notification_channels WHERE id=?", id).
		Scan(&c.ID, &c.Name, &c.Type, &c.URL, &c.Token, &c.ChatID, &c.Enabled)
	return c, err
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), "SELECT id,name,type,url,token,chat_id,enabled FROM notification_channels ORDER BY id")
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	defer rows.Close()
	items := make([]NotificationChannel, 0)
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.URL, &c.Token, &c.ChatID, &c.Enabled); err != nil {
			respondStoreErr(w, s, err)
			return
		}
		c.TokenSet = c.Token != ""
		c.Token = ""
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"channels": items})
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

func (s *Server) handleSaveChannel(w http.ResponseWriter, r *http.Request) {
	var c NotificationChannel
	if !decodeBody(w, r, &c) {
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimSpace(c.URL)
	c.ChatID = strings.TrimSpace(c.ChatID)
	if len([]rune(c.Name)) < 1 || len([]rune(c.Name)) > 64 || len(c.URL) > 2048 || len(c.Token) > 512 || len(c.ChatID) > 128 {
		writeErr(w, 400, "渠道名称或配置长度不正确")
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if r.Method == "PUT" {
		var ok bool
		c.ID, ok = pathID(w, r)
		if !ok {
			return
		}
		old, err := s.store.channel(ctx, c.ID)
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "通知渠道不存在")
			return
		}
		if err != nil {
			respondStoreErr(w, s, err)
			return
		}
		if c.Token == "" && !c.ClearToken && c.Type == old.Type {
			c.Token = old.Token
		}
	}
	if c.Type == "webhook" {
		if !validHTTPURL(c.URL, false) || strings.ContainsAny(c.Token, "\r\n") {
			writeErr(w, 400, "Webhook 地址或密钥格式不正确")
			return
		}
		c.ChatID = ""
	} else if c.Type == "telegram" {
		if !telegramTokenPattern.MatchString(c.Token) || c.ChatID == "" {
			writeErr(w, 400, "请输入有效的 Bot Token 和 Chat ID")
			return
		}
		c.URL = ""
	} else {
		writeErr(w, 400, "渠道类型只能是 webhook 或 telegram")
		return
	}
	var err error
	if r.Method == "POST" {
		var count int
		err = s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_channels").Scan(&count)
		if err == nil && count >= 50 {
			writeErr(w, 400, "最多可创建 50 个通知渠道")
			return
		}
		if err == nil {
			var res sql.Result
			res, err = s.store.db.ExecContext(ctx, "INSERT INTO notification_channels(name,type,url,token,chat_id,enabled) VALUES(?,?,?,?,?,?)", c.Name, c.Type, c.URL, c.Token, c.ChatID, c.Enabled)
			if err == nil {
				c.ID, err = res.LastInsertId()
			}
		}
	} else {
		err = s.store.affectOne(ctx, "UPDATE notification_channels SET name=?,type=?,url=?,token=?,chat_id=?,enabled=? WHERE id=?", c.Name, c.Type, c.URL, c.Token, c.ChatID, c.Enabled, c.ID)
	}
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": c.ID})
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	var count int
	if err := s.store.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM alert_rules WHERE channel_id=?", id).Scan(&count); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	if count > 0 {
		writeErr(w, 409, "该渠道仍被告警规则使用，请先修改或删除相关规则")
		return
	}
	if err := s.store.affectOne(r.Context(), "DELETE FROM notification_channels WHERE id=?", id); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Store) rules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.name,COALESCE(r.server_id,0),COALESCE(r.monitor_id,0),r.metric,r.threshold,
 r.duration_seconds,r.channel_id,r.enabled,r.revision,COALESCE(st.since,0),COALESCE(st.active,0),COALESCE(st.last_eval,0)
 FROM alert_rules r LEFT JOIN alert_states st ON st.rule_id=r.id ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AlertRule, 0)
	for rows.Next() {
		var a AlertRule
		if err := rows.Scan(&a.ID, &a.Name, &a.ServerID, &a.MonitorID, &a.Metric, &a.Threshold, &a.DurationSeconds, &a.ChannelID, &a.Enabled, &a.Revision, &a.Since, &a.Active, &a.LastEval); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.rules(r.Context())
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rules": items})
}

func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var a AlertRule
	if !decodeBody(w, r, &a) {
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	if len([]rune(a.Name)) < 1 || len([]rune(a.Name)) > 64 || a.DurationSeconds < 0 || a.DurationSeconds > 86400 || !finite(a.Threshold) || a.Threshold < 0 {
		writeErr(w, 400, "规则名称、阈值或持续时间不正确")
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	var serverID, monitorID any
	switch a.Metric {
	case "monitor":
		if _, err := s.store.monitor(ctx, a.MonitorID); err != nil {
			writeErr(w, 400, "请选择有效的服务监控")
			return
		}
		monitorID = a.MonitorID
		a.Threshold = 1
	case "cpu", "mem", "swap", "disk", "load1", "quota", "offline":
		if _, ok := s.hub.View(a.ServerID, time.Now()); !ok {
			writeErr(w, 400, "请选择有效的机器")
			return
		}
		serverID = a.ServerID
		if a.Metric == "offline" {
			a.Threshold = 1
		}
		limit := 100.0
		if a.Metric == "quota" {
			limit = 1000
		}
		if a.Metric == "load1" {
			limit = 1000000
		}
		if a.Threshold > limit {
			writeErr(w, 400, "阈值超出指标范围")
			return
		}
	default:
		writeErr(w, 400, "不支持的告警指标")
		return
	}
	if _, err := s.store.channel(ctx, a.ChannelID); err != nil {
		writeErr(w, 400, "请选择有效的通知渠道")
		return
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	defer tx.Rollback()
	if r.Method == "POST" {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_rules").Scan(&count)
		if err == nil && count >= 200 {
			writeErr(w, 400, "最多可创建 200 条告警规则")
			return
		}
		if err == nil {
			var res sql.Result
			res, err = tx.ExecContext(ctx, `INSERT INTO alert_rules(name,server_id,monitor_id,metric,threshold,duration_seconds,channel_id,enabled) VALUES(?,?,?,?,?,?,?,?)`,
				a.Name, serverID, monitorID, a.Metric, a.Threshold, a.DurationSeconds, a.ChannelID, a.Enabled)
			if err == nil {
				a.ID, err = res.LastInsertId()
			}
		}
	} else {
		var ok bool
		a.ID, ok = pathID(w, r)
		if !ok {
			return
		}
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE alert_rules SET name=?,server_id=?,monitor_id=?,metric=?,threshold=?,duration_seconds=?,channel_id=?,enabled=?,revision=revision+1 WHERE id=?`,
			a.Name, serverID, monitorID, a.Metric, a.Threshold, a.DurationSeconds, a.ChannelID, a.Enabled, a.ID)
		if err == nil {
			n, e := res.RowsAffected()
			err = e
			if err == nil && n == 0 {
				writeErr(w, 404, "规则不存在")
				return
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM alert_states WHERE rule_id=?", a.ID)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": a.ID})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if err := s.store.affectOne(r.Context(), "DELETE FROM alert_rules WHERE id=?", id); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (e *serviceEngine) observation(ctx context.Context, a AlertRule, now time.Time) (value float64, known bool) {
	if a.Metric == "monitor" {
		m, err := e.s.store.monitor(ctx, a.MonitorID)
		if err != nil || !m.Enabled {
			return 0, false
		}
		p, err := e.s.store.monitorLast(ctx, a.MonitorID)
		if err != nil || p == nil || p.Status == "unknown" || now.Unix()-p.At > int64(m.IntervalSeconds*2+60) {
			return 0, false
		}
		if p.Status == "down" {
			return 1, true
		}
		return 0, true
	}
	v, ok := e.s.hub.View(a.ServerID, now)
	if !ok {
		return 0, false
	}
	if a.Metric == "offline" {
		if v.Online {
			return 0, true
		}
		return 1, true
	}
	if !v.Online || !v.HasNow {
		return 0, false
	}
	r := rowFromView(v, now)
	switch a.Metric {
	case "cpu":
		return r.CPU, true
	case "mem":
		return r.Mem, true
	case "swap":
		return r.Swap, true
	case "disk":
		return r.Disk, true
	case "load1":
		return r.Load1, true
	case "quota":
		if v.Traffic.Quota > 0 {
			return float64(v.Traffic.Billed()) / float64(v.Traffic.Quota) * 100, true
		}
	}
	return 0, false
}

func (e *serviceEngine) evaluate(ctx context.Context, now time.Time) error {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	e.s.controlMu.Lock()
	defer e.s.controlMu.Unlock()
	rules, err := e.s.store.rules(qctx)
	if err != nil {
		return err
	}
	for _, a := range rules {
		if !a.Enabled {
			continue
		}
		value, known := e.observation(qctx, a, now)
		since, active := a.Since, a.Active
		if !active && (a.LastEval == 0 || now.Unix()-a.LastEval > 15) {
			since = 0
		}
		firing := known && value >= a.Threshold
		if !firing {
			since = 0
		} else if since == 0 {
			since = now.Unix()
		}
		state := ""
		if firing && !active && now.Unix()-since >= int64(a.DurationSeconds) {
			active = true
			state = "firing"
		}
		if known && !firing && active {
			active = false
			state = "recovered"
		}
		if since==a.Since && active==a.Active && (active || since==0) { continue }
		tx, err := e.s.store.db.BeginTx(qctx, nil)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(qctx, `INSERT INTO alert_states(rule_id,since,active,last_eval) VALUES(?,?,?,?)
 ON CONFLICT(rule_id) DO UPDATE SET since=excluded.since,active=excluded.active,last_eval=excluded.last_eval`, a.ID, since, active, now.Unix())
		if err == nil && state != "" {
			label := "告警"
			if state == "recovered" {
				label = "恢复"
			}
			message := fmt.Sprintf("%s: %s (%s = %.2f，阈值 %.2f)", label, a.Name, a.Metric, value, a.Threshold)
			var count int
			err = tx.QueryRowContext(qctx, "SELECT COUNT(*) FROM alert_events WHERE delivery='pending'").Scan(&count)
			delivery, detail := "pending", ""
			if count >= 1000 {
				delivery, detail = "failed", "待发送通知超过 1000 条"
			}
			if err == nil {
				_, err = tx.ExecContext(qctx, `INSERT INTO alert_events(rule_id,at,state,message,channel_id,delivery,delivery_error) VALUES(?,?,?,?,?,?,?)`, a.ID, now.Unix(), state, message, a.ChannelID, delivery, detail)
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

const eventColumns = `id,rule_id,at,state,message,channel_id,delivery,attempts,delivery_error`

func scanEvent(row interface{ Scan(...any) error }) (AlertEvent, error) {
	var e AlertEvent
	err := row.Scan(&e.ID, &e.RuleID, &e.At, &e.State, &e.Message, &e.ChannelID, &e.Delivery, &e.Attempts, &e.DeliveryError)
	return e, err
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), "SELECT "+eventColumns+" FROM alert_events ORDER BY id DESC LIMIT 200")
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	defer rows.Close()
	items := make([]AlertEvent, 0)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			respondStoreErr(w, s, err)
			return
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	writeJSON(w, 200, map[string]any{"events": items})
}

func (e *serviceEngine) sendNotification(ctx context.Context, c NotificationChannel, event AlertEvent) error {
	if !c.Enabled {
		return errors.New("通知渠道已停用")
	}
	endpoint := c.URL
	var body []byte
	var err error
	if c.Type == "telegram" {
		endpoint = "https://api.telegram.org/bot" + c.Token + "/sendMessage"
		body, err = json.Marshal(map[string]any{"chat_id": c.ChatID, "text": "Dingzi 钉子\n" + event.Message})
	} else {
		body, err = json.Marshal(event)
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("通知地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dingzi-server")
	if c.Type == "webhook" && c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return errors.New("通知连接失败，请检查地址或网络")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return errors.New("读取通知响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知服务返回 HTTP %d", resp.StatusCode)
	}
	if c.Type == "telegram" {
		var result struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(data, &result) != nil || !result.OK {
			return errors.New("Telegram 未接受通知，请检查 Bot Token 和 Chat ID")
		}
	}
	return nil
}

func (e *serviceEngine) notifications(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.deliverNext(ctx); err != nil && ctx.Err() == nil {
				e.s.log.Warn("notification queue failed", "error", err)
			}
		}
	}
}

func (e *serviceEngine) deliverNext(ctx context.Context) error {
	event, err := scanEvent(e.s.store.db.QueryRowContext(ctx, "SELECT "+eventColumns+" FROM alert_events WHERE delivery='pending' AND next_attempt<=? ORDER BY id LIMIT 1", time.Now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state, detail := "delivered", ""
	var c NotificationChannel
	if event.ChannelID != nil {
		c, err = e.s.store.channel(ctx, *event.ChannelID)
	} else {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !c.Enabled) {
		state, detail = "skipped", "通知渠道已删除或停用"
	} else if err != nil {
		return err
	} else {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = e.sendNotification(tctx, c, event)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			state, detail = "pending", err.Error()
			if event.Attempts >= 2 {
				state = "failed"
			}
		}
	}
	_, err = e.s.store.db.ExecContext(ctx, "UPDATE alert_events SET delivery=?,attempts=attempts+1,delivery_error=?,next_attempt=? WHERE id=?",
		state, detail, time.Now().Add(time.Duration(30*(1<<event.Attempts))*time.Second).Unix(), event.ID)
	return err
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.store.channel(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "通知渠道不存在")
		return
	}
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	if !c.Enabled {
		writeErr(w, 400, "请先启用通知渠道")
		return
	}
	event := AlertEvent{At: time.Now().Unix(), State: "test", Message: "测试通知: Dingzi 通知渠道连接正常", ChannelID: &id}
	err = s.services.sendNotification(r.Context(), c, event)
	event.Delivery = "delivered"
	if err != nil {
		event.Delivery = "failed"
		event.DeliveryError = err.Error()
	}
	_, saveErr := s.store.db.ExecContext(r.Context(), `INSERT INTO alert_events(at,state,message,channel_id,delivery,attempts,delivery_error) VALUES(?,?,?,?,?,1,?)`,
		event.At, event.State, event.Message, id, event.Delivery, event.DeliveryError)
	if saveErr != nil {
		respondStoreErr(w, s, saveErr)
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
