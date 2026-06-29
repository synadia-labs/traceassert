package match

import (
	"fmt"

	"github.com/synadia-labs/traceassert"
)

// atLayout is the time-of-day layout used in rate failure messages. A trace is one
// connection, so time-of-day with milliseconds is unambiguous and far more readable
// than a full RFC3339 timestamp.
const atLayout = "15:04:05.000"

// RespectRateLimit asserts that the events selected by sel, partitioned into independent
// budgets by by, never exceed the token bucket limit: at most limit.Burst back-to-back,
// then no faster than one every limit.Every. It is the generic rate-limit assertion -
// point it at info polls, publishes per subject, requests per inbox, or any repeating
// event by choosing sel and by.
//
//	Expect(trace).To(RespectRateLimit(isInfoRequest, infoAsset,
//		traceassert.RateLimit{Burst: 5, Every: time.Second}))
//
// It is a CEILING check on the recorded timestamps, not an average: choose limit.Every
// above the client's intended spacing (with headroom), or ordinary jitter will trip it.
//
// A nil by places every selected event in one global bucket (bounding aggregate load);
// a nil sel selects every event. The assertion FAILS CLOSED: if no event matches sel, or
// some selected event cannot be keyed by by, it errors rather than passing vacuously -
// so a green run always means real traffic was checked. Because that is an error (not a
// plain failure), it fails both To and NotTo.
//
// Used with NotTo it asserts the opposite - that some bucket DID exceed the limit -
// which is how a deliberately bursty capture is verified.
func RespectRateLimit(sel traceassert.Predicate, by traceassert.KeyFunc, limit traceassert.RateLimit) M {
	return wrap(&rateMatcher{sel: sel, by: by, limit: limit})
}

type rateMatcher struct {
	sel    traceassert.Predicate
	by     traceassert.KeyFunc
	limit  traceassert.RateLimit
	report traceassert.RateReport // captured during Match for the messages
}

func (m *rateMatcher) Match(actual any) (bool, error) {
	evs, err := toEvents(actual)
	if err != nil {
		return false, err
	}
	m.report = traceassert.CheckRate(evs, traceassert.RateCheck{Select: m.sel, By: m.by, Limit: m.limit})

	// Evidence problems are errors, not pass/fail: they fail both To and NotTo so a
	// mis-pathed capture or a selector/grouping mismatch can never pass silently.
	if m.report.Matched == 0 {
		return false, fmt.Errorf("no events matched the rate-limit selector - nothing was rate-checked")
	}
	if m.report.Unkeyed > 0 {
		return false, fmt.Errorf("%d selected event(s) could not be grouped by the key function - they were not rate-checked; ensure the grouping keys every event the selector accepts",
			m.report.Unkeyed)
	}

	return len(m.report.Violations) == 0, nil
}

func (m *rateMatcher) FailureMessage(any) string {
	if len(m.report.Violations) == 0 {
		return fmt.Sprintf("expected events to respect rate limit (%s)", m.limit)
	}
	v := m.report.Violations[0]
	msg := fmt.Sprintf("expected events to respect rate limit (%s) per key\n  key %q: %s at %s\n  arrived %s early (request %d for this key)",
		m.limit, v.Key, v.Event.String(), v.Event.At.Format(atLayout), v.EarlyBy, v.Count)
	if extra := len(m.report.Violations) - 1; extra > 0 {
		msg += fmt.Sprintf("\n  (+%d more violation(s) across %d key(s))", extra, distinctViolationKeys(m.report.Violations))
	}
	return msg
}

func (m *rateMatcher) NegatedFailureMessage(any) string {
	return fmt.Sprintf("expected some bucket to exceed rate limit (%s), but all %d matched event(s) across %d key(s) stayed within budget",
		m.limit, m.report.Matched, m.report.Keys)
}

func distinctViolationKeys(vs []traceassert.RateViolation) int {
	seen := make(map[string]struct{}, len(vs))
	for _, v := range vs {
		seen[v.Key] = struct{}{}
	}
	return len(seen)
}
