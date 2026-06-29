package traceassert

import (
	"fmt"
	"sort"
	"time"

	"golang.org/x/time/rate"
)

// This file is the generic rate-limit analyzer. It replays a class of repeating
// events through a token bucket and reports the ones a limiter would have rejected,
// so a suite can assert "the client never polled this asset faster than N/sec" without
// any bespoke timing code.
//
// It is a CEILING check on the timestamps the trace actually recorded, not an averaged
// or smoothed rate: it answers "would a token bucket of (Burst, Every) have admitted
// every one of these events?". A client riding the exact limit passes or fails by the
// nanosecond, so callers express the ceiling they care about (with headroom over the
// client's intended rate) rather than the client's exact target. See RateLimit.
//
// The token-bucket arithmetic (fractional refill, the monotonic-time clamp) is handled
// by golang.org/x/time/rate, fed the recorded event timestamps via AllowN(at, 1) rather
// than a wall clock. That is the one piece of non-stdlib machinery the otherwise
// dependency-light core reaches for, and it is a golang.org/x module, not gomega.

// RateLimit is a token bucket: Burst events may occur back-to-back, after which one
// further event is admitted every Every (the sustained spacing - one token regenerates
// per Every, NOT a window of Burst events per Every). The bucket starts full, so the
// opening Burst events are always admitted and an event arriving Every after the bucket
// drains is admitted again.
//
// It is a ceiling, not an average: set Every ABOVE the client's intended spacing (with
// headroom), because a limit set to the client's exact target will trip on ordinary
// scheduling and capture-timestamp jitter.
type RateLimit struct {
	// Burst is the bucket depth: how many events may occur back-to-back before the
	// sustained limit applies. Must be >= 1.
	Burst int
	// Every is the sustained spacing: one token is added to the bucket every Every.
	// Must be > 0.
	Every time.Duration
}

// String renders the limit the way the failure messages and docs phrase it, e.g.
// "burst 5, then 1 every 1s".
func (r RateLimit) String() string {
	return fmt.Sprintf("burst %d, then 1 every %s", r.Burst, r.Every)
}

// valid reports whether the limit is usable; CheckRate panics on an invalid one.
func (r RateLimit) valid() error {
	if r.Burst < 1 {
		return fmt.Errorf("RateLimit.Burst must be >= 1, got %d", r.Burst)
	}
	if r.Every <= 0 {
		return fmt.Errorf("RateLimit.Every must be > 0, got %s", r.Every)
	}
	return nil
}

// RateCheck describes one rate-limit assertion: which events participate (Select),
// how they are partitioned into independent budgets (By), and the budget (Limit).
type RateCheck struct {
	// Select chooses the events the limit applies to. A nil Select means every event.
	Select Predicate
	// By partitions selected events into independent buckets - each key gets its own
	// token bucket. A nil By puts every selected event in a single global bucket
	// (keyed ""). An event for which By reports ok=false is counted as Unkeyed and is
	// NOT placed in any bucket; callers should ensure By keys every event Select admits.
	By KeyFunc
	// Limit is the token bucket every bucket is held to.
	Limit RateLimit
}

// RateViolation is one event a RateCheck's token bucket would have rejected: it arrived
// while its bucket was empty.
type RateViolation struct {
	// Key is the bucket the event belongs to (the By result, or "" when ungrouped).
	Key string
	// Event is the offending frame.
	Event *Event
	// EarlyBy is how long before the bucket would have admitted the event it actually
	// arrived - i.e. the remaining refill time for the token it needed. Always > 0.
	EarlyBy time.Duration
	// Count is the number of selected events for Key up to and including this one (so
	// the first violation in a back-to-back burst of six past a burst of five reports
	// Count == 6).
	Count int
}

// RateReport is the result of CheckRate. It carries the evidence counts (Matched,
// Unkeyed) alongside the violations so a caller can refuse a vacuous pass: an empty
// Violations slice means nothing exceeded the budget only when Matched > 0.
type RateReport struct {
	// Violations are the rejected events, ordered by trace line.
	Violations []RateViolation
	// Matched is how many events Select accepted (the events actually rate-checked,
	// including any that could not be keyed).
	Matched int
	// Unkeyed is how many matched events By could not key (ok=false) and so were left
	// out of every bucket. A non-zero Unkeyed means Select and By disagree and some
	// selected traffic escaped the check.
	Unkeyed int
	// Keys is the number of distinct buckets that held at least one event.
	Keys int
}

// CheckRate replays each bucket of selected events through a token bucket and reports
// every event the bucket would have rejected. Events are grouped by c.By (preserving
// first-seen key order), each bucket is replayed in timestamp order, and the returned
// violations are ordered by trace line.
//
// It panics if c.Limit is invalid (Burst < 1 or Every <= 0): an unusable limit is a
// programming error, and degrading it to "allow everything" would make a misconfigured
// assertion pass silently.
func CheckRate(events []*Event, c RateCheck) RateReport {
	if err := c.Limit.valid(); err != nil {
		panic("traceassert.CheckRate: " + err.Error())
	}

	sel := c.Select
	if sel == nil {
		sel = func(*Event) bool { return true }
	}
	key := c.By
	if key == nil {
		key = func(*Event) (string, bool) { return "", true }
	}

	// Group selected events by key, preserving first-seen key order so the result is
	// deterministic (the same discipline GroupBy uses).
	var order []string
	buckets := map[string][]*Event{}
	report := RateReport{}
	for _, e := range events {
		if !sel(e) {
			continue
		}
		report.Matched++
		k, ok := key(e)
		if !ok {
			report.Unkeyed++
			continue
		}
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], e)
	}
	report.Keys = len(order)

	// limit is one token per Every; rate.Every turns the spacing into a token rate.
	limit := rate.Every(c.Limit.Every)
	for _, k := range order {
		bucket := buckets[k]

		// Replay in timestamp order. Within one connection trace order is already
		// arrival order, but a bucket can interleave frames whose timestamps tie or
		// (rarely) invert under capture buffering; sorting makes the replay a faithful
		// statement about when the events occurred rather than when they were written.
		sort.SliceStable(bucket, func(i, j int) bool { return bucket[i].At.Before(bucket[j].At) })

		lim := rate.NewLimiter(limit, c.Limit.Burst)
		for i, e := range bucket {
			have := lim.TokensAt(e.At)
			if lim.AllowN(e.At, 1) {
				continue
			}
			// Rejected: it needed one whole token but had only `have`. The shortfall
			// refills at one token per Every, so it arrived that much too early.
			early := time.Duration((1 - have) * float64(c.Limit.Every))
			report.Violations = append(report.Violations, RateViolation{
				Key:     k,
				Event:   e,
				EarlyBy: early,
				Count:   i + 1,
			})
		}
	}

	// Order violations by trace line for a stable, trace-ordered result.
	sort.SliceStable(report.Violations, func(i, j int) bool {
		return report.Violations[i].Event.Line < report.Violations[j].Event.Line
	})
	return report
}
