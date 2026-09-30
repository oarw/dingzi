package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type panelSettings struct {
	SiteName        string `json:"site_name"`
	Description     string `json:"description"`
	TerminalEnabled bool   `json:"terminal_enabled"`
}

func (s *Server) loadSettings() error {
	if _, err := s.store.db.Exec(`CREATE TABLE IF NOT EXISTS panel_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create panel settings: %w", err)
	}
	cfg := panelSettings{SiteName: "Dingzi 钉子", Description: "服务器运行概览", TerminalEnabled: true}
	var raw string
	err := s.store.db.QueryRow(`SELECT value FROM panel_settings WHERE id=1`).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return fmt.Errorf("read panel settings: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.settings.Store(&cfg)
	return nil
}

func (s *Server) terminalAllowed() bool {
	return s.opts.TerminalEnabled && s.settings.Load().TerminalEnabled
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": s.settings.Load(), "terminal_available": s.opts.TerminalEnabled,
		"retention_days":  int(s.opts.Retention / (24 * time.Hour)),
		"sample_interval": s.opts.Interval,
	})
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var cfg panelSettings
	if !decodeBody(w, r, &cfg) {
		return
	}
	cfg.SiteName, cfg.Description = strings.TrimSpace(cfg.SiteName), strings.TrimSpace(cfg.Description)
	if cfg.SiteName == "" || utf8.RuneCountInString(cfg.SiteName) > 80 || utf8.RuneCountInString(cfg.Description) > 240 {
		writeErr(w, http.StatusBadRequest, "站点名称需为 1–80 字，首页说明最多 240 字")
		return
	}
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	raw, err := json.Marshal(cfg)
	if err != nil {
		respondStoreErr(w, s, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO panel_settings(id,value) VALUES(1,?)
 ON CONFLICT(id) DO UPDATE SET value=excluded.value`, string(raw)); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	s.settings.Store(&cfg)
	if !cfg.TerminalEnabled {
		s.terminals.closeMachine(0)
	}
	s.handleSettings(w, r)
}

// publicServerRow is intentionally independent of serverRow. New admin fields
// cannot become public merely by being added to the full machine response.
type publicServerRow struct {
	id     int64
	Name   string   `json:"name"`
	Online bool     `json:"online"`
	CPU    *float64 `json:"cpu"`
	Mem    *float64 `json:"mem"`
	Disk   *float64 `json:"disk"`
}

func (s *Server) handlePublicServers(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	rows := s.hub.publicSnapshot(now)
	cfg := s.settings.Load()
	writeJSON(w, http.StatusOK, map[string]any{
		"site_name": cfg.SiteName, "description": cfg.Description,
		"servers": rows, "now": now.Unix(),
	})
}

// Copy only the public fields, without duplicating each machine's history ring.
// Sorting happens after unlocking so anonymous readers hold the lock briefly.
func (h *Hub) publicSnapshot(now time.Time) []publicServerRow {
	h.mu.RLock()
	rows := make([]publicServerRow, 0, len(h.byID))
	for _, m := range h.byID {
		latest, hasNow := m.samples.latest()
		online := m.conn != nil && now.Sub(m.LastSeen) <= h.sampleTimeout &&
			(!hasNow || now.Sub(latest.At) <= h.sampleTimeout)
		row := publicServerRow{id: m.ID, Name: m.Name, Online: online}
		if online && hasNow {
			cpu, mem, disk := round2(latest.State.CPU), pct(latest.State.MemUsed, m.Host.MemTotal), pct(latest.State.DiskUsed, m.Host.DiskTotal)
			row.CPU, row.Mem, row.Disk = &cpu, &mem, &disk
		}
		rows = append(rows, row)
	}
	h.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	return rows
}
