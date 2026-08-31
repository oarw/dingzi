package server

import (
	"sync"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

// RingSize is how many recent samples are kept in memory per machine.
//
// This is the memory bound, and it is a bound by construction rather than by
// hope: the ring overwrites its oldest entry, so resident memory scales with
// machine count and nothing else. Not uptime, not traffic, not how long a
// browser has been left open. At roughly 200 bytes a sample that is ~24KiB per
// machine, so a 50-machine fleet holds ~1.2MiB of live history. The database
// file grows; the process does not.
//
// 120 samples is two minutes at the default one-second interval, which is what
// the live view needs. Anything longer is a database query, not a cache.
const RingSize = 120

// Sample is one stored observation.
type Sample struct {
	// At is the panel's clock when the sample arrived. The panel's own time is
	// authoritative for ordering, because a machine with a broken clock would
	// otherwise scatter points across a chart or hide them in the future.
	At time.Time
	// SkewMS is the agent's clock minus the panel's, kept so the skew can be
	// surfaced instead of silently distorting the data.
	SkewMS int64
	State  proto.State
	// NetInSpeed and NetOutSpeed are bytes/sec derived from the counter delta
	// between this sample and the previous one.
	NetInSpeed  uint64
	NetOutSpeed uint64
}

// ring is a fixed-size circular buffer of samples.
type ring struct {
	buf  [RingSize]Sample
	next int
	n    int
}

func (r *ring) push(s Sample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % RingSize
	if r.n < RingSize {
		r.n++
	}
}

// snapshot returns the samples oldest-first, as a copy the caller may keep.
func (r *ring) snapshot() []Sample {
	out := make([]Sample, 0, r.n)
	start := (r.next - r.n + RingSize) % RingSize
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%RingSize])
	}
	return out
}

func (r *ring) latest() (Sample, bool) {
	if r.n == 0 {
		return Sample{}, false
	}
	return r.buf[(r.next-1+RingSize)%RingSize], true
}

// Machine is the panel's live view of one monitored host.
type Machine struct {
	ID        int64
	UUID      string
	Name      string
	Host      proto.Host
	Version   string
	FirstSeen time.Time
	LastSeen  time.Time

	// conn is the live agent connection, nil when offline.
	conn *agentConn

	samples ring
	Traffic Traffic
}

// Hub holds the live fleet.
//
// One RWMutex guards everything. That is justified rather than lazy: every
// critical section here is a map lookup or a struct copy measured in
// nanoseconds, and 50 agents reporting once a second is 50 lock acquisitions a
// second. Sharding would add failure modes to buy throughput nobody needs.
type Hub struct {
	mu     sync.RWMutex
	byID   map[int64]*Machine
	byUUID map[string]*Machine
}

func NewHub() *Hub {
	return &Hub{byID: map[int64]*Machine{}, byUUID: map[string]*Machine{}}
}

// Load seeds the hub from storage so a panel restart does not appear to lose
// the fleet until every agent happens to reconnect.
func (h *Hub) Load(machines []*Machine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range machines {
		h.byID[m.ID] = m
		h.byUUID[m.UUID] = m
	}
}

// Get returns the machine with this id.
func (h *Hub) Get(id int64) (*Machine, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.byID[id]
	return m, ok
}

// GetByUUID returns the machine with this agent uuid.
func (h *Hub) GetByUUID(uuid string) (*Machine, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.byUUID[uuid]
	return m, ok
}

// Put installs a machine, preserving the live state of an existing entry.
//
// A reconnecting agent must not reset its own history: the ring buffer, the
// traffic accumulator and the first-seen time all belong to the machine, not to
// the connection.
func (h *Hub) Put(m *Machine) *Machine {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.byID[m.ID]; ok {
		existing.Name = m.Name
		existing.Host = m.Host
		existing.Version = m.Version
		return existing
	}
	h.byID[m.ID] = m
	h.byUUID[m.UUID] = m
	return m
}

// Attach marks a machine online. A duplicate connection for the same machine
// closes the older one: two agents sharing a uuid is a misconfiguration (a
// cloned VM image), and interleaving their samples would produce a chart that
// looks like one machine behaving impossibly.
func (h *Hub) Attach(id int64, c *agentConn) {
	h.mu.Lock()
	m, ok := h.byID[id]
	var replaced *agentConn
	if ok {
		replaced = m.conn
		m.conn = c
		m.LastSeen = time.Now()
	}
	h.mu.Unlock()

	if replaced != nil && replaced != c {
		replaced.closeWith("replaced by a newer connection for the same machine")
	}
}

// Detach marks a machine offline.
//
// The identity check matters: a stale goroutine finishing its teardown after
// the agent already reconnected would otherwise mark a live machine offline,
// which shows up as a machine that flaps every time its network hiccups.
func (h *Hub) Detach(id int64, c *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.byID[id]; ok && m.conn == c {
		m.conn = nil
	}
}

// Conn returns the live agent connection for a machine.
func (h *Hub) Conn(id int64) (*agentConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.byID[id]
	if !ok || m.conn == nil {
		return nil, false
	}
	return m.conn, true
}

// Remove drops a machine from the live fleet and returns its connection so the
// caller can close it.
func (h *Hub) Remove(id int64) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.byID[id]
	if !ok {
		return nil
	}
	delete(h.byID, id)
	delete(h.byUUID, m.UUID)
	return m.conn
}

// Push records a sample and returns it with derived speeds filled in.
func (h *Hub) Push(id int64, st proto.State, at time.Time) (Sample, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	m, ok := h.byID[id]
	if !ok {
		return Sample{}, false
	}

	s := Sample{At: at, State: st, SkewMS: st.AgentTimeMS - at.UnixMilli()}

	// Speed comes from the counter delta rather than the agent's own rate: the
	// agent cannot know how long the frame spent in flight, and the panel can.
	if prev, had := m.samples.latest(); had {
		if elapsed := at.Sub(prev.At).Seconds(); elapsed > 0 {
			s.NetInSpeed = rate(prev.State.NetInTransfer, st.NetInTransfer, elapsed)
			s.NetOutSpeed = rate(prev.State.NetOutTransfer, st.NetOutTransfer, elapsed)
		}
	}

	m.samples.push(s)
	m.LastSeen = at
	m.Traffic.Accumulate(st.NetInTransfer, st.NetOutTransfer, at)
	return s, true
}

// rate converts a counter delta to bytes/sec, treating a counter that went
// backwards as unmeasurable rather than as an enormous negative rate. The
// counters reset on reboot and NIC reset, and a 32-bit counter wraps.
func rate(prev, cur uint64, elapsed float64) uint64 {
	if cur < prev {
		return 0
	}
	return uint64(float64(cur-prev) / elapsed)
}

// Samples returns the in-memory history for one machine.
func (h *Hub) Samples(id int64) []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.byID[id]
	if !ok {
		return nil
	}
	return m.samples.snapshot()
}

// MachineView is a consistent copy of one machine's state.
type MachineView struct {
	Machine
	Online bool
	Latest Sample
	HasNow bool
}

// staleAfter is how long without a sample before a connected machine is
// reported offline. A connection that is up while samples have stopped is not a
// working machine, and reporting it online is the specific lie that makes an
// operator trust a green dot that means nothing.
const staleAfter = 30 * time.Second

// Snapshot returns every machine, ordered by id.
func (h *Hub) Snapshot(now time.Time) []MachineView {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]MachineView, 0, len(h.byID))
	for _, m := range h.byID {
		v := MachineView{Machine: *m, Online: m.conn != nil}
		// The copy shares Host.GPUs with the original. Host is only ever
		// replaced wholesale, never mutated in place, so the shared backing
		// array is never written after publication.
		v.Machine.samples = ring{}
		v.Latest, v.HasNow = m.samples.latest()
		if v.Online && v.HasNow && now.Sub(v.Latest.At) > staleAfter {
			v.Online = false
		}
		out = append(out, v)
	}
	sortViews(out)
	return out
}

// View returns one machine's state.
func (h *Hub) View(id int64, now time.Time) (MachineView, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.byID[id]
	if !ok {
		return MachineView{}, false
	}
	v := MachineView{Machine: *m, Online: m.conn != nil}
	v.Machine.samples = ring{}
	v.Latest, v.HasNow = m.samples.latest()
	if v.Online && v.HasNow && now.Sub(v.Latest.At) > staleAfter {
		v.Online = false
	}
	return v, true
}

// TrafficSnapshot copies every machine's traffic counters for persistence,
// under the lock, so the caller can write to disk outside it.
func (h *Hub) TrafficSnapshot() map[int64]Traffic {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[int64]Traffic, len(h.byID))
	for id, m := range h.byID {
		out[id] = m.Traffic
	}
	return out
}

// SetTraffic replaces a machine's traffic configuration.
func (h *Hub) SetTraffic(id int64, fn func(*Traffic)) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.byID[id]
	if !ok {
		return false
	}
	fn(&m.Traffic)
	return true
}

// Rename changes a machine's display name.
func (h *Hub) Rename(id int64, name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.byID[id]
	if !ok {
		return false
	}
	m.Name = name
	return true
}

// SetHost replaces a machine's static facts.
func (h *Hub) SetHost(id int64, host proto.Host) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.byID[id]
	if !ok {
		return false
	}
	m.Host = host
	return true
}
