package match_test

import (
	"testing"

	"github.com/onsi/gomega"
	gtypes "github.com/onsi/gomega/types"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
	"github.com/synadia-labs/traceassert/subject"
)

// A matcher must never panic on a failing assertion - it must return (false, error).
// The value-extracting matchers (PayloadJSON, SubjectCapture, ReplyCapture) used to
// hand a nil to the inner matcher when the value was absent, which Gomega's
// BeTrue/BeFalse panic on (nil-deref in isBool). These cases lock in that an absent
// value now fails cleanly, including when wrapped in a quantifier that propagates it.
func TestMatchersDoNotPanicOnAbsentValues(t *testing.T) {
	// ev.Subject "a.b" and ev.Reply "" match neither grammar; "absent" is not in the payload.
	ev := &traceassert.Event{Verb: "PUB", Subject: "a.b", Reply: "", Payload: []byte(`{"present":1}`)}
	g := subject.MustParse("{op:int}.x")
	tr := &traceassert.Trace{Events: []*traceassert.Event{ev}}

	cases := []struct {
		name   string
		m      gtypes.GomegaMatcher
		actual any
	}{
		{"PayloadJSON absent + BeTrue", PayloadJSON("absent", gomega.BeTrue()), ev},
		{"PayloadJSON absent + BeFalse", PayloadJSON("absent", gomega.BeFalse()), ev},
		{"ReplyCapture no-match + BeTrue", ReplyCapture(g, "op", gomega.BeTrue()), ev},
		{"SubjectCapture no-match + BeTrue", SubjectCapture(g, "op", gomega.BeTrue()), ev},
		{"Each(PayloadJSON absent + BeTrue)", Each(PayloadJSON("absent", gomega.BeTrue())), tr},
		{"ContainEvent(ReplyCapture no-match + BeTrue)", ContainEvent(ReplyCapture(g, "op", gomega.BeTrue())), tr},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, err := mustNotPanic(t, c.m, c.actual)
			if err != nil {
				t.Fatalf("expected a clean failure, got error: %v", err)
			}
			if ok {
				t.Fatal("expected the assertion to fail, but it matched")
			}
			// FailureMessage must also be safe to call.
			_ = c.m.FailureMessage(c.actual)
		})
	}
}

// mustNotPanic runs Match, failing the test (rather than crashing the process) if it
// panics.
func mustNotPanic(t *testing.T, m gtypes.GomegaMatcher, actual any) (ok bool, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("matcher panicked instead of failing cleanly: %v", r)
		}
	}()
	return m.Match(actual)
}
