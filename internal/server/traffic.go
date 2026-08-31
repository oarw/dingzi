package server

import (
	"sort"
	"time"
)

// How a quota counts traffic. Providers disagree, so this is the operator's to
// declare rather than the panel's to assume: billing an outbound-only provider
// as sum reports twice the real usage and triggers an alert that is simply
// wrong.
const (
	CountSum = "sum" // inbound + outbound
	CountOut = "out" // outbound only
	CountMax = "max" // whichever direction is larger
)

// ValidCountMode reports whether mode is a supported counting rule.
func ValidCountMode(mode string) bool {
	switch mode {
	case CountSum, CountOut, CountMax:
		return true
	}
	return false
}

// Traffic accumulates a machine's billing-cycle transfer.
//
// The agent reports raw OS counters, so this type owns rollback detection:
// counters reset on agent restart, machine reboot and NIC reset, and a 32-bit
// counter wraps. See proto.State for why that job lives on this side.
type Traffic struct {
	// CycleStart is when the current billing cycle began.
	CycleStart time.Time
	// InBytes and OutBytes are this cycle's accumulated transfer.
	InBytes  uint64
	OutBytes uint64

	// LastRawIn and LastRawOut are the previous raw counter readings, the
	// baseline the next delta is measured against.
	LastRawIn  uint64
	LastRawOut uint64
	// HasRaw distinguishes "no reading yet" from "a reading of zero", which
	// matters because a fresh agent legitimately reports zero and must not have
	// its whole counter billed as a first delta.
	HasRaw bool

	// Quota is the cycle allowance in bytes, 0 meaning unset.
	Quota uint64
	// ResetDay is the day of month the cycle turns over, 1-31.
	ResetDay int
	// CountMode is one of CountSum, CountOut, CountMax.
	CountMode string
}

// Accumulate folds a raw counter reading into the cycle totals.
func (t *Traffic) Accumulate(rawIn, rawOut uint64, now time.Time) {
	if t.ResetDay == 0 {
		t.ResetDay = 1
	}
	if t.CountMode == "" {
		t.CountMode = CountSum
	}
	if t.CycleStart.IsZero() {
		t.CycleStart = CycleStart(now, t.ResetDay)
	}
	t.rollCycle(now)

	if !t.HasRaw {
		// First reading is a baseline, not a delta. Billing it would charge the
		// machine for every byte since it booted the moment the panel restarts.
		t.LastRawIn, t.LastRawOut, t.HasRaw = rawIn, rawOut, true
		return
	}

	t.InBytes += delta(t.LastRawIn, rawIn)
	t.OutBytes += delta(t.LastRawOut, rawOut)
	t.LastRawIn, t.LastRawOut = rawIn, rawOut
}

// delta returns the traffic between two raw counter readings.
//
// A counter that went backwards means it was reset, so the new value is itself
// the traffic since the reset. That is an approximation — it loses whatever was
// transferred between the last reading and the reset — but the error is bounded
// by one report interval, and the alternative of dropping the sample loses more.
func delta(prev, cur uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

// rollCycle resets the totals when the billing cycle turns over.
func (t *Traffic) rollCycle(now time.Time) {
	start := CycleStart(now, t.ResetDay)
	if !start.After(t.CycleStart) {
		return
	}
	t.CycleStart = start
	t.InBytes, t.OutBytes = 0, 0
	// The raw baseline deliberately survives the cycle turn. Clearing HasRaw
	// would discard the next interval's traffic, so every cycle would silently
	// start a report short.
}

// CycleStart returns the most recent cycle boundary at or before now.
func CycleStart(now time.Time, resetDay int) time.Time {
	day := clampDay(resetDay, now.Year(), now.Month())
	start := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, now.Location())
	if start.After(now) {
		prev := now.AddDate(0, -1, 0)
		// AddDate on the 31st of a 31-day month can land in the wrong month
		// (March 31 minus one month is March 3), so normalise from the first.
		prev = time.Date(prev.Year(), prev.Month(), 1, 0, 0, 0, 0, now.Location())
		day = clampDay(resetDay, prev.Year(), prev.Month())
		start = time.Date(prev.Year(), prev.Month(), day, 0, 0, 0, 0, now.Location())
	}
	return start
}

// clampDay pins a reset day to a day that exists in the given month.
//
// A quota that resets on the 31st has no 31st in February. Letting time.Date
// normalise it would move the boundary into March and shift the whole cycle,
// so the last day of the month is used instead — which is what "the 31st"
// means to the person who chose it.
func clampDay(day, year int, month time.Month) int {
	if day < 1 {
		return 1
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}

// Billed returns the bytes counted against the quota under the active mode.
func (t Traffic) Billed() uint64 {
	switch t.CountMode {
	case CountOut:
		return t.OutBytes
	case CountMax:
		if t.OutBytes > t.InBytes {
			return t.OutBytes
		}
		return t.InBytes
	default:
		return t.InBytes + t.OutBytes
	}
}

// UsedPercent returns quota consumption 0-100, or 0 when no quota is set.
//
// Capped at 100 so a progress bar cannot overflow its track. Billed still
// reports the real figure, so an overage is visible as a number even though the
// bar is full.
func (t Traffic) UsedPercent() float64 {
	if t.Quota == 0 {
		return 0
	}
	p := float64(t.Billed()) / float64(t.Quota) * 100
	if p > 100 {
		return 100
	}
	return p
}

// sortViews orders machines by id so the list does not reshuffle between
// polls. A list whose rows move while being read is unusable for the one job
// this panel has.
func sortViews(v []MachineView) {
	sort.Slice(v, func(i, j int) bool { return v[i].ID < v[j].ID })
}
