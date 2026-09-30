package agent

import (
	"context"
	"testing"
	"time"
)

// The collectors run against the real machine. These assertions are deliberately
// loose — they check that values are plausible rather than exact, because the
// point is to catch a collector that returns nothing at all, which is the
// failure that actually happens across platforms.
func TestCollectHost(t *testing.T) {
	c, err := NewCollector(nil, nil)
	if err != nil {
		t.Fatalf("NewCollector: %v", err)
	}
	h, err := c.Host(context.Background())
	if err != nil {
		t.Logf("host info partially unavailable: %v", err)
	}
	if h.CPUThreads < 1 {
		t.Errorf("CPUThreads = %d, want >= 1", h.CPUThreads)
	}
	if h.MemTotal == 0 {
		t.Error("MemTotal = 0, want the machine's real memory")
	}
	if h.DiskTotal == 0 {
		t.Error("DiskTotal = 0; mount resolution found no usable filesystem")
	}
	if h.OS == "" || h.Arch == "" {
		t.Errorf("OS/Arch = %q/%q, want both set", h.OS, h.Arch)
	}
	if h.BootTime == 0 {
		t.Error("BootTime = 0, so uptime cannot be derived")
	}
	t.Logf("host: %s %s / %s / %d cores %d threads / mem %d / disk %d",
		h.Platform, h.PlatformVersion, h.Arch, h.CPUCores, h.CPUThreads,
		h.MemTotal, h.DiskTotal)
}

func TestCollectState(t *testing.T) {
	c, err := NewCollector(nil, nil)
	if err != nil {
		t.Fatalf("NewCollector: %v", err)
	}
	ctx := context.Background()

	// First sample seeds the rate baseline and reports no speed.
	first := c.State(ctx)
	if first.NetInSpeed != 0 || first.NetOutSpeed != 0 {
		t.Errorf("first sample reported speed %d/%d, want 0 with no baseline",
			first.NetInSpeed, first.NetOutSpeed)
	}
	if first.NetInTransfer == 0 {
		t.Error("NetInTransfer = 0; no interface survived the ignore list")
	}

	time.Sleep(1100 * time.Millisecond)
	s := c.State(ctx)

	if s.CPU < 0 || s.CPU > 100 {
		t.Errorf("CPU = %v, want 0-100", s.CPU)
	}
	if s.MemUsed == 0 {
		t.Error("MemUsed = 0")
	}
	if s.DiskUsed == 0 {
		t.Error("DiskUsed = 0")
	}
	if s.AgentTimeMS == 0 {
		t.Error("AgentTimeMS unset")
	}
	// Counters are absolute, so the second sample cannot be below the first
	// unless the interface reset mid-test.
	if s.NetInTransfer < first.NetInTransfer {
		t.Errorf("NetInTransfer went backwards: %d then %d",
			first.NetInTransfer, s.NetInTransfer)
	}
	t.Logf("state: cpu %.1f%% mem %d swap %d disk %d load %.2f/%.2f/%.2f",
		s.CPU, s.MemUsed, s.SwapUsed, s.DiskUsed, s.Load1, s.Load5, s.Load15)
	t.Logf("net: in %d out %d (%d B/s down, %d B/s up) tcp %d udp %d temps %d",
		s.NetInTransfer, s.NetOutTransfer, s.NetInSpeed, s.NetOutSpeed,
		s.TCPConns, s.UDPConns, len(s.Temperatures))
}

func TestSkipFS(t *testing.T) {
	for _, fs := range []string{"tmpfs", "devtmpfs", "overlay", "proc", "TMPFS"} {
		if !skipFS(fs) {
			t.Errorf("skipFS(%q) = false, want true", fs)
		}
	}
	for _, fs := range []string{"ext4", "xfs", "btrfs", "NTFS", "apfs", "zfs"} {
		if skipFS(fs) {
			t.Errorf("skipFS(%q) = true, want false", fs)
		}
	}
}

func TestSkipIface(t *testing.T) {
	c, err := NewCollector(nil, nil)
	if err != nil {
		t.Fatalf("NewCollector: %v", err)
	}
	// A veth-heavy container host double-counts without these exclusions.
	for _, n := range []string{"lo", "docker0", "veth1a2b", "br-abc", "virbr0"} {
		if !c.skipIface(n) {
			t.Errorf("skipIface(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"eth0", "ens3", "enp0s3", "wlan0", "Ethernet"} {
		if c.skipIface(n) {
			t.Errorf("skipIface(%q) = true, want false", n)
		}
	}
}

// An explicit mount list must be honoured verbatim; operators use it to exclude
// a backup volume from the disk figure.
func TestResolveMountsHonoursHints(t *testing.T) {
	c := &Collector{}
	got := c.resolveMounts([]string{"/data", "/var/log"})
	if len(got) != 2 || got[0] != "/data" || got[1] != "/var/log" {
		t.Errorf("resolveMounts(hints) = %v, want the hints unchanged", got)
	}
}
