package traceassert

import (
	"testing"
	"time"
)

// base is a fixed instant the rate tests offset from; the analyzer only ever looks at
// differences between event timestamps, so the absolute value is arbitrary.
var base = time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

// at builds a minimal client publish to subject at base+offset. Line is derived from the
// offset order the caller passes, set explicitly by the caller where it matters.
func at(line int, subject string, offsetMillis int) *Event {
	return &Event{
		Line:    line,
		At:      base.Add(time.Duration(offsetMillis) * time.Millisecond),
		Dir:     ToServer,
		Verb:    "PUB",
		Subject: subject,
	}
}

// bySubject keys every event on its full subject - one bucket per subject.
func bySubject() KeyFunc {
	return func(e *Event) (string, bool) { return e.Subject, true }
}

func secondLimit(burst int) RateLimit { return RateLimit{Burst: burst, Every: time.Second} }

func TestCheckRate_BurstThenSustainedPasses(t *testing.T) {
	// Five back-to-back (burst 5) then one every ~1.05s: never exceeds the bucket.
	evs := []*Event{
		at(1, "A", 0), at(2, "A", 100), at(3, "A", 200), at(4, "A", 300), at(5, "A", 400),
		at(6, "A", 1450), at(7, "A", 2500), at(8, "A", 3550),
	}
	r := CheckRate(evs, RateCheck{By: bySubject(), Limit: secondLimit(5)})
	if len(r.Violations) != 0 {
		t.Fatalf("expected no violations, got %d: %+v", len(r.Violations), r.Violations)
	}
	if r.Matched != 8 || r.Keys != 1 || r.Unkeyed != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
}

func TestCheckRate_SixthInBurstViolates(t *testing.T) {
	// Six within half a second, burst 5: the sixth finds the bucket empty.
	evs := []*Event{
		at(1, "A", 0), at(2, "A", 100), at(3, "A", 200), at(4, "A", 300), at(5, "A", 400),
		at(6, "A", 500),
	}
	r := CheckRate(evs, RateCheck{By: bySubject(), Limit: secondLimit(5)})
	if len(r.Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(r.Violations))
	}
	v := r.Violations[0]
	if v.Event.Line != 6 {
		t.Fatalf("expected violation on line 6, got %d", v.Event.Line)
	}
	if v.Count != 6 {
		t.Fatalf("expected Count 6, got %d", v.Count)
	}
	// At t=500ms the bucket held 0.5 tokens, so it arrived ~0.5s early.
	if v.EarlyBy < 400*time.Millisecond || v.EarlyBy > 600*time.Millisecond {
		t.Fatalf("expected EarlyBy ~500ms, got %s", v.EarlyBy)
	}
}

func TestCheckRate_FloodCountsEveryRejectedEvent(t *testing.T) {
	// 100 events at the same instant, burst 1: the first is admitted, the other 99 are
	// each rejected (a rejected event does not consume, so it does not free a token).
	var evs []*Event
	for i := range 100 {
		evs = append(evs, at(i+1, "A", 0))
	}
	r := CheckRate(evs, RateCheck{By: bySubject(), Limit: secondLimit(1)})
	if len(r.Violations) != 99 {
		t.Fatalf("expected 99 violations, got %d", len(r.Violations))
	}
}

func TestCheckRate_PerKeyIndependence(t *testing.T) {
	// Two subjects, each a burst of 5 interleaved in the same half second. Per-subject
	// each is within budget even though the aggregate is 10 in 0.5s.
	var evs []*Event
	line := 1
	for i := range 5 {
		evs = append(evs, at(line, "A", i*50))
		line++
		evs = append(evs, at(line, "B", i*50+25))
		line++
	}
	perKey := CheckRate(evs, RateCheck{By: bySubject(), Limit: secondLimit(5)})
	if len(perKey.Violations) != 0 {
		t.Fatalf("per-key expected no violations, got %d", len(perKey.Violations))
	}
	if perKey.Keys != 2 {
		t.Fatalf("expected 2 keys, got %d", perKey.Keys)
	}
	// A single global bucket (By nil) sees all 10 in 0.5s and rejects past the burst.
	global := CheckRate(evs, RateCheck{Limit: secondLimit(5)})
	if len(global.Violations) == 0 {
		t.Fatalf("global bucket expected violations, got none")
	}
	if global.Keys != 1 {
		t.Fatalf("expected 1 global key, got %d", global.Keys)
	}
}

func TestCheckRate_OutOfOrderAndEqualTimestamps(t *testing.T) {
	// Trace order is not timestamp order: the analyzer sorts each bucket by At before
	// replay. These three are within 2ms (a real burst); burst 1 flags two of them
	// regardless of the order they appear in the slice.
	evs := []*Event{
		at(1, "A", 2), // arrives first in trace order, latest in time
		at(2, "A", 0),
		at(3, "A", 1),
	}
	r := CheckRate(evs, RateCheck{By: bySubject(), Limit: RateLimit{Burst: 1, Every: time.Second}})
	if len(r.Violations) != 2 {
		t.Fatalf("expected 2 violations for a 3-event burst at burst 1, got %d", len(r.Violations))
	}
	// Equal timestamps: two simultaneous events, burst 1 -> the second is a violation.
	eq := []*Event{at(1, "A", 0), at(2, "A", 0)}
	r2 := CheckRate(eq, RateCheck{By: bySubject(), Limit: RateLimit{Burst: 1, Every: time.Second}})
	if len(r2.Violations) != 1 {
		t.Fatalf("expected 1 violation for two simultaneous events, got %d", len(r2.Violations))
	}
}

func TestCheckRate_NilSelectMatchesAll(t *testing.T) {
	evs := []*Event{at(1, "A", 0), at(2, "B", 0)}
	r := CheckRate(evs, RateCheck{Limit: secondLimit(5)})
	if r.Matched != 2 {
		t.Fatalf("nil Select should match all, got Matched %d", r.Matched)
	}
}

func TestCheckRate_SelectFiltersAndCounts(t *testing.T) {
	evs := []*Event{at(1, "A", 0), at(2, "B", 0), at(3, "A", 0)}
	onlyA := func(e *Event) bool { return e.Subject == "A" }
	r := CheckRate(evs, RateCheck{Select: onlyA, By: bySubject(), Limit: secondLimit(5)})
	if r.Matched != 2 {
		t.Fatalf("expected Matched 2 (only A), got %d", r.Matched)
	}
}

func TestCheckRate_UnkeyedCounted(t *testing.T) {
	// By keys A but not B; B is matched (Select is nil = all) yet left out of every
	// bucket, so it is reported as Unkeyed rather than silently dropped.
	evs := []*Event{at(1, "A", 0), at(2, "B", 0)}
	keyA := func(e *Event) (string, bool) {
		if e.Subject == "A" {
			return "A", true
		}
		return "", false
	}
	r := CheckRate(evs, RateCheck{By: keyA, Limit: secondLimit(5)})
	if r.Matched != 2 || r.Unkeyed != 1 || r.Keys != 1 {
		t.Fatalf("expected Matched 2, Unkeyed 1, Keys 1, got %+v", r)
	}
}

func TestCheckRate_PanicsOnInvalidLimit(t *testing.T) {
	for _, bad := range []RateLimit{
		{Burst: 0, Every: time.Second},
		{Burst: 5, Every: 0},
		{Burst: 5, Every: -time.Second},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected panic for invalid limit %+v", bad)
				}
			}()
			CheckRate([]*Event{at(1, "A", 0)}, RateCheck{By: bySubject(), Limit: bad})
		}()
	}
}

func TestRateLimitString(t *testing.T) {
	got := RateLimit{Burst: 5, Every: time.Second}.String()
	if got != "burst 5, then 1 every 1s" {
		t.Fatalf("unexpected String(): %q", got)
	}
}
