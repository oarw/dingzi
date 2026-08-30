package agent

import (
	"context"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/oarw/dingzi/internal/proto"
)

// State collects one metrics sample.
//
// The CPU figure comes from the interval since the previous call rather than a
// blocking sample: cpu.Percent with a duration sleeps for that duration, which
// on a one-second report interval would mean the agent spends most of its life
// blocked inside the collector.
func (c *Collector) State(ctx context.Context) proto.State {
	s := proto.State{AgentTimeMS: time.Now().UnixMilli()}

	if pcts, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pcts) > 0 {
		s.CPU = round2(pcts[0])
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		// Total minus Available, not vm.Used: on Linux, Used excludes cache and
		// buffers that the kernel will surrender under pressure, so it
		// understates what an operator would call "memory in use". Available is
		// the kernel's own estimate of what a new process could get.
		if vm.Available <= vm.Total {
			s.MemUsed = vm.Total - vm.Available
		} else {
			s.MemUsed = vm.Used
		}
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.SwapUsed = sw.Used
	}

	_, used := c.diskUsage()
	s.DiskUsed = used

	if avg, err := load.AvgWithContext(ctx); err == nil {
		s.Load1, s.Load5, s.Load15 = round2(avg.Load1), round2(avg.Load5), round2(avg.Load15)
	}

	c.collectNet(ctx, &s)
	c.collectConns(ctx, &s)
	c.collectSensors(ctx, &s)
	return s
}

// collectNet fills the network counters and derives speed.
//
// The totals are the OS's own cumulative values, deliberately not rebased to
// agent start: the server owns rollback detection because it needs deltas
// anyway, and duplicating that logic here would put the same easy-to-get-wrong
// code in two places. See proto.State's field comment.
func (c *Collector) collectNet(ctx context.Context, s *proto.State) {
	counters, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return
	}
	var in, out uint64
	for _, ct := range counters {
		if c.skipIface(ct.Name) {
			continue
		}
		in += ct.BytesRecv
		out += ct.BytesSent
	}
	s.NetInTransfer, s.NetOutTransfer = in, out

	now := time.Now()
	if !c.lastNetAt.IsZero() {
		elapsed := now.Sub(c.lastNetAt).Seconds()
		// A clock that jumped backwards, or two samples in the same
		// millisecond, would divide by ~0 and report an absurd speed.
		if elapsed >= 0.1 {
			// Only count forward movement: an interface reset or a counter
			// wrap makes the delta negative, and a negative delta on unsigned
			// arithmetic becomes an enormous positive one.
			if in >= c.lastNetIn {
				s.NetInSpeed = uint64(float64(in-c.lastNetIn) / elapsed)
			}
			if out >= c.lastNetOut {
				s.NetOutSpeed = uint64(float64(out-c.lastNetOut) / elapsed)
			}
		}
	}
	c.lastNetAt, c.lastNetIn, c.lastNetOut = now, in, out
}

func (c *Collector) skipIface(name string) bool {
	for _, p := range c.ignoreIfaces {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// collectConns counts TCP and UDP connections.
//
// This is the most expensive call in the sample on a busy machine, because it
// enumerates every socket. It is bounded to the two counts the panel shows
// rather than gathering per-connection detail nobody displays.
func (c *Collector) collectConns(ctx context.Context, s *proto.State) {
	if conns, err := net.ConnectionsWithContext(ctx, "tcp"); err == nil {
		s.TCPConns = len(conns)
	}
	if conns, err := net.ConnectionsWithContext(ctx, "udp"); err == nil {
		s.UDPConns = len(conns)
	}
}

// collectSensors reads temperatures where the platform exposes them. Most VPS
// guests expose none, so absence is normal and not worth logging.
func (c *Collector) collectSensors(ctx context.Context, s *proto.State) {
	temps, err := sensors.TemperaturesWithContext(ctx)
	if err != nil && len(temps) == 0 {
		return
	}
	for _, t := range temps {
		// A sensor reading exactly zero is almost always an unpopulated slot
		// rather than a machine at freezing point.
		if t.Temperature == 0 || t.SensorKey == "" {
			continue
		}
		if s.Temperatures == nil {
			s.Temperatures = make(map[string]float64, len(temps))
		}
		s.Temperatures[t.SensorKey] = round2(t.Temperature)
	}
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
