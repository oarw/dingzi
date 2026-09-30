package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/oarw/dingzi/internal/proto"
)

func createFeature(t *testing.T, s *Server, c *http.Cookie, path string, body any) int64 {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := requestJSON(s, c, "POST", "/api/v1/"+path, string(raw))
	if w.Code != 200 {
		t.Fatalf("create %s: %d %s", path, w.Code, w.Body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func TestMonitorCRUDAndOfflineResult(t *testing.T) {
	s, c := testPanel(t)
	testMachine(t, s)
	m := Monitor{Name: "health", ServerID: 1, Type: "http", Target: "http://localhost/health", IntervalSeconds: 10, TimeoutMS: 1000, Enabled: true}
	id := createFeature(t, s, c, "monitors", m)
	w := requestJSON(s, c, "POST", fmt.Sprintf("/api/v1/monitors/%d/run", id), "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatalf("offline result: %d %s", w.Code, w.Body)
	}
	w = requestJSON(s, c, "GET", "/api/v1/monitors", "")
	if !strings.Contains(w.Body.String(), `"uptime":null`) {
		t.Fatalf("unknown counted as service failure: %s", w.Body)
	}
	m.Target = "http://localhost/new"
	raw, _ := json.Marshal(m)
	w = requestJSON(s, c, "PUT", fmt.Sprintf("/api/v1/monitors/%d", id), string(raw))
	if w.Code != 200 {
		t.Fatalf("update: %s", w.Body)
	}
	var count int
	s.store.db.QueryRow("SELECT COUNT(*) FROM monitor_results").Scan(&count)
	if count != 0 {
		t.Fatal("changing monitor kept old target history")
	}
	if w := requestJSON(s, nil, "GET", "/api/v1/monitors", ""); w.Code != 401 {
		t.Fatal("monitor targets exposed without auth")
	}
}

func TestAlertDurationDedupRecoveryAndRetry(t *testing.T) {
	s, c := testPanel(t)
	testMachine(t, s)
	var deliveries atomic.Int32
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-test-secret" {
			t.Error("missing authorization")
		}
		var event AlertEvent
		if json.NewDecoder(r.Body).Decode(&event) != nil {
			t.Error("invalid payload")
		}
		if deliveries.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer webhook.Close()
	channel := createFeature(t, s, c, "channels", NotificationChannel{Name: "local", Type: "webhook", URL: webhook.URL, Token: "local-test-secret", Enabled: true})
	createFeature(t, s, c, "alert-rules", AlertRule{Name: "offline", ServerID: 1, Metric: "offline", DurationSeconds: 10, ChannelID: channel, Enabled: true})
	ctx := context.Background()
	now := time.Now()
	for _, offset := range []int{0, 5, 10, 15} {
		if err := s.services.evaluate(ctx, now.Add(time.Duration(offset)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	s.store.db.QueryRow("SELECT COUNT(*) FROM alert_events").Scan(&count)
	if count != 1 {
		t.Fatalf("expected one firing event, got %d", count)
	}
	if err := s.services.deliverNext(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	s.store.db.QueryRow("SELECT delivery FROM alert_events").Scan(&state)
	if state != "pending" {
		t.Fatalf("failed send: %s", state)
	}
	s.store.db.Exec("UPDATE alert_events SET next_attempt=0")
	if err := s.services.deliverNext(ctx); err != nil {
		t.Fatal(err)
	}
	s.store.db.QueryRow("SELECT delivery FROM alert_events").Scan(&state)
	if state != "delivered" {
		t.Fatalf("retry: %s", state)
	}
	s.hub.Attach(1, &agentConn{closed: make(chan struct{})})
	s.hub.Push(1, proto.State{}, now.Add(20*time.Second))
	if err := s.services.evaluate(ctx, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.services.evaluate(ctx, now.Add(25*time.Second)); err != nil {
		t.Fatal(err)
	}
	s.store.db.QueryRow("SELECT COUNT(*) FROM alert_events").Scan(&count)
	if count != 2 {
		t.Fatalf("expected recovery once, got %d events", count)
	}
	w := requestJSON(s, c, "GET", "/api/v1/channels", "")
	if strings.Contains(w.Body.String(), "local-test-secret") {
		t.Fatal("channel API exposed token")
	}
	w = requestJSON(s, c, "DELETE", fmt.Sprintf("/api/v1/channels/%d", channel), "")
	if w.Code != 409 {
		t.Fatalf("deleted channel used by rule: %d", w.Code)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramPayloadAndFailure(t *testing.T) {
	s, _ := testPanel(t)
	s.services.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot123:abc/sendMessage" {
			t.Fatalf("bad Telegram URL: %s", r.URL)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["chat_id"] != "-42" || payload["text"] != "Dingzi 钉子\nlocal test" {
			t.Fatalf("bad payload: %#v", payload)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":false}`)), Header: http.Header{}}, nil
	})}
	err := s.services.sendNotification(context.Background(), NotificationChannel{Type: "telegram", Token: "123:abc", ChatID: "-42", Enabled: true}, AlertEvent{Message: "local test"})
	if err == nil {
		t.Fatal("accepted rejected Telegram response")
	}
}

func TestCredentialBindingAndRevocation(t *testing.T) {
	s, _ := testPanel(t)
	id := uuid.NewString()
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	r := httptest.NewRequest("GET", proto.Path, nil)
	r.Header.Set(proto.AgentUUIDHeader, id)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(proto.RegistrationHeader, s.opts.AgentSecret)
	if !s.authAgent(r) {
		t.Fatal("valid enrollment rejected")
	}
	if err := s.store.bindCredential(r.Context(), id, token); err != nil {
		t.Fatal(err)
	}
	m, err := s.register(proto.Hello{UUID: id, Name: "bound"})
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Del(proto.RegistrationHeader)
	if !s.authAgent(r) {
		t.Fatal("bound token required shared key")
	}
	r.Header.Set(proto.RegistrationHeader, s.opts.AgentSecret)
	r.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("b", 32))))
	if s.authAgent(r) {
		t.Fatal("shared key hijacked bound UUID")
	}
	if err := s.store.DeleteMachine(r.Context(), m.ID); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if s.authAgent(r) {
		t.Fatal("deleted credential accepted")
	}
	if err := s.store.bindCredential(r.Context(), id, token); err == nil {
		t.Fatal("deleted UUID re-enrolled")
	}
}

func TestMonitorDispatchAndRevisionFence(t *testing.T) {
	s, cookie := testPanel(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	id := uuid.NewString()
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	header := http.Header{}
	header.Set(proto.AgentUUIDHeader, id)
	header.Set("Authorization", "Bearer "+token)
	header.Set(proto.RegistrationHeader, s.opts.AgentSecret)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+proto.Path, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, _ := proto.Encode(proto.TypeHello, "", proto.Hello{UUID: id, Name: "fixture"})
	conn.WriteMessage(websocket.TextMessage, hello)
	var envelope proto.Envelope
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&envelope); err != nil {
		t.Fatal(err)
	}
	var welcome proto.Welcome
	proto.Decode(&envelope, &welcome)
	mid := createFeature(t, s, cookie, "monitors", Monitor{Name: "check", ServerID: welcome.ServerID, Type: "tcp", Target: "localhost:80", IntervalSeconds: 10, TimeoutMS: 1000, Enabled: true})
	done := make(chan error, 1)
	go func() {
		for {
			var env proto.Envelope
			if err := conn.ReadJSON(&env); err != nil {
				done <- err
				return
			}
			if env.Type != proto.TypeTask {
				continue
			}
			raw, _ := proto.Encode(proto.TypeTaskResult, env.ID, proto.TaskResult{MonitorID: mid, OK: true, LatencyMS: 12.5})
			done <- conn.WriteMessage(websocket.TextMessage, raw)
			return
		}
	}()
	m, _ := s.store.monitor(context.Background(), mid)
	p, err := s.services.check(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "up" || p.LatencyMS != 12.5 {
		t.Fatalf("bad result: %+v", p)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	attached, _ := s.hub.Conn(welcome.ServerID)
	conn.Close()
	s.store.db.Exec("UPDATE monitors SET revision=revision+1 WHERE id=?", mid)
	s.hub.Detach(welcome.ServerID, attached)
	if _, err := s.services.check(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	var count int
	s.store.db.QueryRow("SELECT COUNT(*) FROM monitor_results").Scan(&count)
	if count != 1 {
		t.Fatal("old monitor revision stored a new result")
	}
}

func TestPruneAllHistoriesAndBoundedState(t *testing.T) {
	s, c := testPanel(t)
	m := testMachine(t, s)
	mid := createFeature(t, s, c, "monitors", Monitor{Name: "check", ServerID: m.ID, Type: "tcp", Target: "localhost:80", IntervalSeconds: 10, TimeoutMS: 1000})
	old := time.Now().Add(-48 * time.Hour).Unix()
	s.store.db.Exec("INSERT INTO monitor_results(monitor_id,at,status,latency_ms,loss,status_code,error) VALUES(?,?,'up',0,0,0,'')", mid, old)
	s.store.db.Exec("INSERT INTO alert_events(at,state,message) VALUES(?,'test','old')", old)
	s.store.insertBatch([]queued{{id: m.ID, sample: Sample{At: time.Unix(old, 0)}}})
	if n, err := s.store.Prune(context.Background(), 24*time.Hour); err != nil || n != 3 {
		t.Fatalf("prune %d: %v", n, err)
	}
	for i := 0; i < 10000; i++ {
		s.hub.Push(m.ID, proto.State{CPU: float64(i % 100)}, time.Now())
	}
	if len(s.hub.Samples(m.ID)) != RingSize {
		t.Fatal("history ring grew")
	}
	for i := 0; i < 5000; i++ {
		s.sessions.throttle(fmt.Sprint(i))
	}
	if len(s.sessions.lastTry) > 1024 {
		t.Fatal("login throttle unbounded")
	}
}
