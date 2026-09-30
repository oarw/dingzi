package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPublicAndAdminBoundaries(t *testing.T) {
	s, cookie := testPanel(t)
	testMachine(t, s)
	for _, path := range []string{"/api/v1/servers", "/api/v1/servers/1/history", "/api/v1/settings", "/api/v1/monitors", "/api/v1/channels", "/api/v1/alert-rules", "/api/v1/alert-events", "/admin/board.js", "/admin/app.js"} {
		if w := requestJSON(s, nil, "GET", path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("visitor GET %s returned %d", path, w.Code)
		}
	}
	for _, path := range []string{"/admin.html", "/web/admin.html", "/board.js", "/app.js", "/settings.go"} {
		if w := requestJSON(s, nil, "GET", path, ""); w.Code != 404 {
			t.Errorf("unexpected public asset %s: %d", path, w.Code)
		}
	}
	w := requestJSON(s, nil, "GET", "/admin/", "")
	if w.Code != 302 || w.Header().Get("Location") != "/admin/login" || strings.Contains(w.Body.String(), "后台导航") {
		t.Fatal("unauthenticated admin page was not redirected to login")
	}
	w = requestJSON(s, cookie, "GET", "/admin/", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "后台导航") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated admin page is unavailable or cacheable")
	}
	for _, auth := range []*http.Cookie{nil, cookie} {
		w = requestJSON(s, auth, "GET", "/", "")
		for _, forbidden := range []string{"服务监控", "面板设置", "通知渠道", "xterm", "term-wrap", "editor", "admin/app.js"} {
			if strings.Contains(w.Body.String(), forbidden) {
				t.Errorf("public page contains admin feature %q", forbidden)
			}
		}
		w = requestJSON(s, auth, "GET", "/api/v1/public/servers", "")
		var body struct {
			Servers []map[string]any `json:"servers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Servers) != 1 {
			t.Fatalf("public overview: %s", w.Body)
		}
		allowed := map[string]bool{"name": true, "online": true, "cpu": true, "mem": true, "disk": true}
		if len(body.Servers[0]) != len(allowed) {
			t.Fatalf("unexpected public fields: %v", body.Servers[0])
		}
		for key := range body.Servers[0] {
			if !allowed[key] {
				t.Errorf("private field %s was exposed", key)
			}
		}
		if strings.Contains(w.Body.String(), s.opts.AgentSecret) {
			t.Fatal("public overview exposed enrollment key")
		}
	}
	if w := requestJSON(s, cookie, "GET", "/api/v1/servers", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "cpu_model") {
		t.Fatal("authenticated fleet lost full machine details")
	}
	s.sessions.revoke(cookie.Value)
	if w := requestJSON(s, cookie, "GET", "/api/v1/servers", ""); w.Code != 401 {
		t.Fatal("logged-out session can still read machine details")
	}
}

func TestPanelSettingsPersistenceAndAuthorization(t *testing.T) {
	s, cookie := testPanel(t)
	body := `{"site_name":"我的监控","description":"服务运行情况","terminal_enabled":true}`
	if w := requestJSON(s, nil, "PUT", "/api/v1/settings", body); w.Code != 401 {
		t.Fatal("visitor changed settings")
	}
	if w := requestJSON(s, cookie, "PUT", "/api/v1/settings", body); w.Code != 200 {
		t.Fatalf("save settings: %s", w.Body)
	}
	if s.terminalAllowed() {
		t.Fatal("web settings bypassed the command-line terminal prohibition")
	}
	again, err := New(s.opts, s.store, s.log)
	if err != nil {
		t.Fatal(err)
	}
	if again.settings.Load().SiteName != "我的监控" || again.settings.Load().Description != "服务运行情况" {
		t.Fatal("settings were not persisted across restart")
	}
	w := requestJSON(s, nil, "GET", "/api/v1/public/servers", "")
	if !strings.Contains(w.Body.String(), "我的监控") || strings.Contains(w.Body.String(), "terminal_enabled") {
		t.Fatal("public projection omitted site name or exposed private settings")
	}
	if w := requestJSON(s, cookie, "PUT", "/api/v1/settings", `{"site_name":"","description":"","terminal_enabled":false}`); w.Code != 400 {
		t.Fatal("invalid settings were accepted")
	}
	if s.settings.Load().SiteName != "我的监控" {
		t.Fatal("invalid request partly changed settings")
	}
}
