// Package agent implements the Dingzi probe: it collects metrics, keeps a
// WebSocket connection to the panel, and answers reachability tasks.
package agent

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/oarw/dingzi/internal/proto"
)

// Collector gathers metrics. It holds the small amount of state needed to turn
// the OS's cumulative counters into rates.
//
// Every collector method tolerates failure: a machine where sensors are
// unreadable, or a container where load average is meaningless, reports zero for
// that field and full values for the rest. A monitoring agent that refuses to
// report anything because one field is unavailable is worse than one that
// reports what it can.
type Collector struct {
	// mounts are the filesystem mountpoints to sum for disk totals, resolved
	// once at construction.
	mounts []string

	// Previous network sample, for speed. Absolute totals are reported straight
	// from the OS; only the rate needs history.
	lastNetAt  time.Time
	lastNetIn  uint64
	lastNetOut uint64

	// ignoreIfaces holds interface name prefixes excluded from network totals.
	ignoreIfaces []string
}

// NewCollector builds a collector and resolves which mounts and interfaces to
// watch. mountHints and ifaceIgnores may be empty, in which case sensible
// defaults are used.
func NewCollector(mountHints, ifaceIgnores []string) (*Collector, error) {
	c := &Collector{ignoreIfaces: ifaceIgnores}
	if len(c.ignoreIfaces) == 0 {
		// Loopback and virtual interfaces double-count or report traffic that
		// never left the machine. A veth-heavy Docker host otherwise shows
		// wildly inflated throughput.
		c.ignoreIfaces = []string{
			"lo", "veth", "docker", "br-", "virbr", "vmnet", "tun", "tap",
			"cni", "flannel", "kube", "Loopback",
		}
	}
	c.mounts = c.resolveMounts(mountHints)
	return c, nil
}

// resolveMounts picks the mountpoints whose usage is summed into disk totals.
// Explicit hints win. Otherwise every physical filesystem is included, which
// matches what an operator means by "disk" on a machine with a separate /data.
func (c *Collector) resolveMounts(hints []string) []string {
	if len(hints) > 0 {
		return hints
	}
	parts, err := disk.Partitions(false)
	if err != nil {
		return []string{defaultMount()}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if skipFS(p.Fstype) || seen[p.Mountpoint] {
			continue
		}
		// A mount that cannot be stat'd (a stale NFS handle, a permission
		// boundary) would otherwise make every later sample fail.
		if _, err := disk.Usage(p.Mountpoint); err != nil {
			continue
		}
		seen[p.Mountpoint] = true
		out = append(out, p.Mountpoint)
	}
	if len(out) == 0 {
		return []string{defaultMount()}
	}
	sort.Strings(out)
	return out
}

func defaultMount() string {
	if runtime.GOOS == "windows" {
		return "C:\\"
	}
	return "/"
}

// skipFS reports whether a filesystem type is virtual, i.e. its "usage" is not
// disk space anyone can fill.
func skipFS(fstype string) bool {
	switch strings.ToLower(fstype) {
	case "tmpfs", "devtmpfs", "devfs", "overlay", "squashfs", "aufs", "iso9660",
		"proc", "sysfs", "cgroup", "cgroup2", "ramfs", "fuse.gvfsd-fuse",
		"autofs", "binfmt_misc", "debugfs", "tracefs", "securityfs", "pstore",
		"bpf", "configfs", "fusectl", "hugetlbfs", "mqueue", "nsfs":
		return true
	}
	return false
}

// Host collects the static facts. Called on connect and rarely after.
func (c *Collector) Host(ctx context.Context) (proto.Host, error) {
	h := proto.Host{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPUThreads: runtime.NumCPU(),
	}

	info, err := host.InfoWithContext(ctx)
	if err != nil {
		// Without host info there is still a usable report; the panel shows
		// blanks for platform rather than dropping the machine.
		return h, fmt.Errorf("host info: %w", err)
	}
	h.Hostname = info.Hostname
	h.Platform = info.Platform
	h.PlatformVersion = info.PlatformVersion
	h.Kernel = info.KernelVersion
	h.Virtualization = info.VirtualizationSystem
	h.BootTime = info.BootTime

	if cpus, err := cpu.InfoWithContext(ctx); err == nil && len(cpus) > 0 {
		h.CPUModel = strings.TrimSpace(cpus[0].ModelName)
		// Physical core count: gopsutil reports cores per socket, so sum
		// across entries rather than trusting the first.
		total := 0
		for _, ci := range cpus {
			total += int(ci.Cores)
		}
		h.CPUCores = total
	}
	if phys, err := cpu.CountsWithContext(ctx, false); err == nil && phys > 0 {
		h.CPUCores = phys
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		h.MemTotal = vm.Total
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		h.SwapTotal = sw.Total
	}
	total, _ := c.diskUsage()
	h.DiskTotal = total
	return h, nil
}

// diskUsage sums used and total bytes across the monitored mounts.
func (c *Collector) diskUsage() (total, used uint64) {
	for _, m := range c.mounts {
		u, err := disk.Usage(m)
		if err != nil {
			continue
		}
		total += u.Total
		used += u.Used
	}
	return total, used
}
