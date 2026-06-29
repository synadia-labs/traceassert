package match_test

import (
	"strings"
	"testing"
	"time"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
)

var rateBase = time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

func pubAt(line int, subject string, offsetMillis int) *traceassert.Event {
	return &traceassert.Event{
		Line:    line,
		At:      rateBase.Add(time.Duration(offsetMillis) * time.Millisecond),
		Dir:     traceassert.ToServer,
		Verb:    "PUB",
		Subject: subject,
	}
}

func rateTrace(evs ...*traceassert.Event) *traceassert.Trace {
	return &traceassert.Trace{Events: evs, Footer: &traceassert.Footer{Duration: 1}}
}

func keyBySubject(e *traceassert.Event) (string, bool) { return e.Subject, true }

func TestRespectRateLimit_Passes(t *testing.T) {
	tr := rateTrace(
		pubAt(1, "A", 0), pubAt(2, "A", 100), pubAt(3, "A", 200), pubAt(4, "A", 300), pubAt(5, "A", 400),
		pubAt(6, "A", 1450),
	)
	ok, err := RespectRateLimit(nil, keyBySubject, traceassert.RateLimit{Burst: 5, Every: time.Second}).Match(tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected the trace to respect the limit")
	}
}

func TestRespectRateLimit_FailsWithDiagnosticMessage(t *testing.T) {
	tr := rateTrace(
		pubAt(1, "A", 0), pubAt(2, "A", 100), pubAt(3, "A", 200), pubAt(4, "A", 300), pubAt(5, "A", 400),
		pubAt(6, "A", 500),
	)
	m := RespectRateLimit(nil, keyBySubject, traceassert.RateLimit{Burst: 5, Every: time.Second})
	ok, err := m.Match(tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected the trace to exceed the limit")
	}
	msg := m.FailureMessage(tr)
	for _, want := range []string{"burst 5, then 1 every 1s", `key "A"`, "line 6", "request 6", "early"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("failure message missing %q:\n%s", want, msg)
		}
	}
}

func TestRespectRateLimit_FailsClosedOnNoMatch(t *testing.T) {
	tr := rateTrace(pubAt(1, "A", 0))
	noneMatch := func(*traceassert.Event) bool { return false }
	// Both To and NotTo must fail: Match returns an error, not a pass/fail bool.
	_, err := RespectRateLimit(noneMatch, keyBySubject, traceassert.RateLimit{Burst: 5, Every: time.Second}).Match(tr)
	if err == nil {
		t.Fatal("expected an error when no events match the selector")
	}
	if !strings.Contains(err.Error(), "no events matched") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRespectRateLimit_FailsClosedOnUnkeyed(t *testing.T) {
	tr := rateTrace(pubAt(1, "A", 0), pubAt(2, "B", 0))
	keyA := func(e *traceassert.Event) (string, bool) {
		if e.Subject == "A" {
			return "A", true
		}
		return "", false
	}
	_, err := RespectRateLimit(nil, keyA, traceassert.RateLimit{Burst: 5, Every: time.Second}).Match(tr)
	if err == nil {
		t.Fatal("expected an error when a selected event cannot be keyed")
	}
	if !strings.Contains(err.Error(), "could not be grouped") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRespectRateLimit_NegatedMessage(t *testing.T) {
	tr := rateTrace(pubAt(1, "A", 0), pubAt(2, "A", 2000))
	m := RespectRateLimit(nil, keyBySubject, traceassert.RateLimit{Burst: 5, Every: time.Second})
	ok, err := m.Match(tr)
	if err != nil || !ok {
		t.Fatalf("expected within-budget pass, ok=%v err=%v", ok, err)
	}
	msg := m.NegatedFailureMessage(tr)
	if !strings.Contains(msg, "stayed within budget") {
		t.Fatalf("unexpected negated message: %s", msg)
	}
}
