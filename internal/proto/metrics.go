package proto

// Host holds facts about a machine that change rarely or never. Sent in
// [Hello] and again only when something actually changes, so the common case
// costs one small frame per connection instead of per sample.
type Host struct {
	Platform        string `json:"platform"`         // "ubuntu", "windows"
	PlatformVersion string `json:"platform_version"` // "24.04"
	OS              string `json:"os"`               // "linux", "windows"
	Kernel          string `json:"kernel"`           // "6.8.0-31-generic"
	Arch            string `json:"arch"`             // "amd64", "arm64"
	Virtualization  string `json:"virtualization"`   // "kvm", "docker", ""
	Hostname        string `json:"hostname"`

	CPUModel   string `json:"cpu_model"`
	CPUCores   int    `json:"cpu_cores"`   // physical cores, 0 if unknown
	CPUThreads int    `json:"cpu_threads"` // logical CPUs

	MemTotal  uint64 `json:"mem_total"`  // bytes
	SwapTotal uint64 `json:"swap_total"` // bytes
	DiskTotal uint64 `json:"disk_total"` // bytes, sum of monitored mounts

	// BootTime is a unix timestamp in seconds. Uptime is derived from it by
	// the panel so a stale sample does not show a frozen uptime counter.
	BootTime uint64 `json:"boot_time"`

	// GPUs lists detected accelerator model names, empty on most machines.
	GPUs []string `json:"gpus,omitempty"`
}

// State is one metrics sample.
//
// Counters are reported as absolute values rather than deltas: an agent restart
// or a dropped frame then cannot corrupt a rate calculation beyond the single
// interval it happened in.
type State struct {
	// AgentTimeMS is the agent's own clock when the sample was taken. The
	// panel stamps its own time on arrival and uses the difference to surface
	// clock skew rather than trusting either blindly.
	AgentTimeMS int64 `json:"agent_time_ms"`

	CPU float64 `json:"cpu"` // percent 0-100, across all cores

	MemUsed  uint64 `json:"mem_used"`  // bytes
	SwapUsed uint64 `json:"swap_used"` // bytes
	DiskUsed uint64 `json:"disk_used"` // bytes

	// NetInTransfer and NetOutTransfer are the OS's raw cumulative interface
	// counters, NOT bytes since the agent started. The agent deliberately does
	// not subtract a baseline: these counters reset on agent restart, machine
	// reboot, NIC reset and 32-bit overflow, so somebody has to detect the
	// rollback, and doing it here as well as on the server would duplicate the
	// same error-prone logic in two places. The server owns it, because it
	// already needs deltas to compute speed and to accumulate monthly totals
	// for quota tracking.
	NetInTransfer  uint64 `json:"net_in_transfer"`
	NetOutTransfer uint64 `json:"net_out_transfer"`

	NetInSpeed  uint64 `json:"net_in_speed"`  // bytes/sec
	NetOutSpeed uint64 `json:"net_out_speed"` // bytes/sec

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	TCPConns  int `json:"tcp_conns"`
	UDPConns  int `json:"udp_conns"`
	Processes int `json:"processes"`

	// Temperatures maps sensor label to degrees Celsius. Absent where the
	// platform or permissions do not expose sensors.
	Temperatures map[string]float64 `json:"temperatures,omitempty"`

	// GPUUtil is per-GPU utilization percent, index-aligned with [Host.GPUs].
	GPUUtil []float64 `json:"gpu_util,omitempty"`
}

// Uptime returns seconds since boot relative to nowUnix, or 0 if BootTime is
// unset or in the future.
//
// A machine whose clock runs ahead of the panel's would otherwise produce an
// enormous number, because the subtraction underflows on an unsigned type.
func (h Host) Uptime(nowUnix int64) uint64 {
	if h.BootTime == 0 || int64(h.BootTime) > nowUnix {
		return 0
	}
	return uint64(nowUnix - int64(h.BootTime))
}

// Equal reports whether two host records describe the same facts.
//
// Host holds a slice, which makes it non-comparable with == as a matter of the
// type rather than of any particular value, so the fields are compared
// explicitly. The agent uses this to decide whether anything actually changed
// before spending a frame on an update. A field added to Host must be added
// here too; the test in this package guards that by comparing field counts.
func (h Host) Equal(o Host) bool {
	if len(h.GPUs) != len(o.GPUs) {
		return false
	}
	for i := range h.GPUs {
		if h.GPUs[i] != o.GPUs[i] {
			return false
		}
	}
	return h.Platform == o.Platform &&
		h.PlatformVersion == o.PlatformVersion &&
		h.OS == o.OS &&
		h.Kernel == o.Kernel &&
		h.Arch == o.Arch &&
		h.Virtualization == o.Virtualization &&
		h.Hostname == o.Hostname &&
		h.CPUModel == o.CPUModel &&
		h.CPUCores == o.CPUCores &&
		h.CPUThreads == o.CPUThreads &&
		h.MemTotal == o.MemTotal &&
		h.SwapTotal == o.SwapTotal &&
		h.DiskTotal == o.DiskTotal &&
		h.BootTime == o.BootTime
}
