package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Without this a browser may sniff a JSON error body as HTML and execute
	// anything an attacker got reflected into it.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// serverRow is the flat shape the UI consumes.
//
// Flat on purpose: the panel's storage layout is not the browser's business,
// and a UI that walks a nested structure breaks every time the storage changes.
type serverRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Version string `json:"agent_version"`

	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Kernel   string `json:"kernel"`
	Virt     string `json:"virtualization"`
	CPUModel string `json:"cpu_model"`
	Cores    int    `json:"cpu_cores"`
	Threads  int    `json:"cpu_threads"`

	Uptime uint64 `json:"uptime"`

	CPU  float64 `json:"cpu"`
	Mem  float64 `json:"mem"`
	Swap float64 `json:"swap"`
	Disk float64 `json:"disk"`

	MemUsed   uint64 `json:"mem_used"`
	MemTotal  uint64 `json:"mem_total"`
	SwapUsed  uint64 `json:"swap_used"`
	SwapTotal uint64 `json:"swap_total"`
	DiskUsed  uint64 `json:"disk_used"`
	DiskTotal uint64 `json:"disk_total"`

	NetInSpeed  uint64 `json:"net_in_speed"`
	NetOutSpeed uint64 `json:"net_out_speed"`

	TrafficIn  uint64  `json:"traffic_in"`
	TrafficOut uint64  `json:"traffic_out"`
	Quota      uint64  `json:"quota"`
	QuotaUsed  float64 `json:"quota_used"`
	QuotaMode  string  `json:"quota_mode"`
	ResetDay   int     `json:"reset_day"`
	CycleStart int64   `json:"cycle_start"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	TCPConns  int `json:"tcp_conns"`
	UDPConns  int `json:"udp_conns"`
	Processes int `json:"processes"`

	Temperatures map[string]float64 `json:"temperatures,omitempty"`

	// SkewMS is only reported when it is large enough to matter, so the UI does
	// not show a warning for the few hundred milliseconds of ordinary network
	// delay that every healthy agent has.
	SkewMS int64 `json:"skew_ms,omitempty"`

	// TerminalEnabled tells the UI whether to offer a terminal for this machine.
	TerminalEnabled bool `json:"terminal_enabled"`

	LastSeen int64 `json:"last_seen"`
}

// pct returns used/total as a percentage, guarding a zero total and clamping
// the result so a bar cannot render past its track.
func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	p := float64(used) / float64(total) * 100
	if p > 100 {
		return 100
	}
	return round2(p)
}

// skewReportThreshold is how far off an agent's clock must be before the panel
// mentions it.
const skewReportThreshold = 5000 // ms

func rowFromView(v MachineView, now time.Time) serverRow {
	st := v.Latest.State
	r := serverRow{
		ID: v.ID, Name: v.Name, Online: v.Online, Version: v.Version,
		Platform: v.Host.Platform, Arch: v.Host.Arch, Kernel: v.Host.Kernel,
		Virt: v.Host.Virtualization, CPUModel: v.Host.CPUModel,
		Cores: v.Host.CPUCores, Threads: v.Host.CPUThreads,
		Uptime:          v.Host.Uptime(now.Unix()),
		MemTotal:        v.Host.MemTotal,
		SwapTotal:       v.Host.SwapTotal,
		DiskTotal:       v.Host.DiskTotal,
		TerminalEnabled: v.Host.TerminalEnabled,
		LastSeen:        v.LastSeen.Unix(),
		TrafficIn:       v.Traffic.InBytes,
		TrafficOut:      v.Traffic.OutBytes,
		Quota:           v.Traffic.Quota,
		QuotaUsed:       round2(v.Traffic.UsedPercent()),
		QuotaMode:       v.Traffic.CountMode,
		ResetDay:        v.Traffic.ResetDay,
	}
	if !v.Traffic.CycleStart.IsZero() {
		r.CycleStart = v.Traffic.CycleStart.Unix()
	}
	if !v.HasNow {
		return r
	}

	r.CPU = round2(st.CPU)
	r.MemUsed, r.SwapUsed, r.DiskUsed = st.MemUsed, st.SwapUsed, st.DiskUsed
	r.Mem = pct(st.MemUsed, v.Host.MemTotal)
	r.Swap = pct(st.SwapUsed, v.Host.SwapTotal)
	r.Disk = pct(st.DiskUsed, v.Host.DiskTotal)
	r.NetInSpeed, r.NetOutSpeed = v.Latest.NetInSpeed, v.Latest.NetOutSpeed
	r.Load1, r.Load5, r.Load15 = st.Load1, st.Load5, st.Load15
	r.TCPConns, r.UDPConns, r.Processes = st.TCPConns, st.UDPConns, st.Processes
	r.Temperatures = st.Temperatures
	if skew := v.Latest.SkewMS; skew > skewReportThreshold || skew < -skewReportThreshold {
		r.SkewMS = skew
	}
	return r
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	views := s.hub.Snapshot(now)
	rows := make([]serverRow, 0, len(views))
	for _, v := range views {
		rows = append(rows, rowFromView(v, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"servers": rows,
		"now":     now.Unix(),
		// Reported so the UI can hide the terminal control entirely on a panel
		// that has it switched off, rather than offering a button that always
		// fails.
		"terminal_enabled": s.opts.TerminalEnabled,
	})
}

// pathID reads the {id} path segment.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "机器编号不正确")
		return 0, false
	}
	return id, true
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	hours := atoiDefault(r.URL.Query().Get("hours"), 1)
	if hours < 1 {
		hours = 1
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	buckets := atoiDefault(r.URL.Query().Get("buckets"), 200)
	if buckets > 1000 {
		buckets = 1000
	}

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	points, err := s.store.History(ctx, id, time.Now().Add(-time.Duration(hours)*time.Hour), buckets)
	if err != nil {
		s.log.Error("history query failed", "id", id, "error", err)
		writeErr(w, http.StatusInternalServerError, "查询历史数据失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

// patchBody uses pointers so an absent field is distinguishable from a field
// set to zero. Without that, omitting the quota and setting it to 0 (meaning
// "no quota") are the same request.
type patchBody struct {
	Name      *string `json:"name"`
	Quota     *uint64 `json:"quota"`
	ResetDay  *int    `json:"reset_day"`
	CountMode *string `json:"count_mode"`
}

func (s *Server) handlePatchServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body patchBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	if body.Name != nil {
		name := trimSpace(*body.Name)
		if name == "" || len([]rune(name)) > 64 {
			writeErr(w, http.StatusBadRequest, "名称需要 1 到 64 个字符")
			return
		}
		if err := s.store.Rename(ctx, id, name); err != nil {
			respondStoreErr(w, s, err)
			return
		}
		s.hub.Rename(id, name)
	}

	if body.Quota != nil || body.ResetDay != nil || body.CountMode != nil {
		view, exists := s.hub.View(id, time.Now())
		if !exists {
			writeErr(w, http.StatusNotFound, "机器不存在")
			return
		}
		t := view.Traffic
		if body.Quota != nil {
			t.Quota = *body.Quota
		}
		if body.CountMode != nil {
			// Rejected rather than defaulted: silently substituting a mode would
			// measure something the operator did not choose and then alert on it.
			if !ValidCountMode(*body.CountMode) {
				writeErr(w, http.StatusBadRequest,
					"流量口径只能是 sum、out 或 max")
				return
			}
			t.CountMode = *body.CountMode
		}
		if body.ResetDay != nil {
			if *body.ResetDay < 1 || *body.ResetDay > 31 {
				writeErr(w, http.StatusBadRequest, "归零日需要在 1 到 31 之间")
				return
			}
			if *body.ResetDay != t.ResetDay {
				t.ResetDay = *body.ResetDay
				// The cycle boundary moved, so recompute it now rather than
				// waiting for the next sample to notice.
				t.CycleStart = CycleStart(time.Now(), t.ResetDay)
			}
		}
		if err := s.store.SetQuota(ctx, id, t.Quota, t.ResetDay, t.CountMode,
			t.CycleStart); err != nil {
			respondStoreErr(w, s, err)
			return
		}
		s.hub.SetTraffic(id, func(dst *Traffic) {
			dst.Quota, dst.ResetDay, dst.CountMode = t.Quota, t.ResetDay, t.CountMode
			dst.CycleStart = t.CycleStart
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	if err := s.store.DeleteMachine(ctx, id); err != nil {
		respondStoreErr(w, s, err)
		return
	}
	// Close the live connection too. Left open, the agent would keep reporting
	// and re-register on its next reconnect, so the machine would reappear and
	// look like the delete silently failed.
	if c := s.hub.Remove(id); c != nil {
		c.closeWith("this machine was removed from the panel")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func respondStoreErr(w http.ResponseWriter, s *Server, err error) {
	if errors.Is(err, ErrNoMachine) {
		writeErr(w, http.StatusNotFound, "机器不存在")
		return
	}
	s.log.Error("store write failed", "error", err)
	writeErr(w, http.StatusInternalServerError, "保存失败")
}
