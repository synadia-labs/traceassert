// Package match is the shipped vocabulary of Gomega matchers for asserting NATS
// trace conformance. ADR authors compose these; they should rarely need to write
// their own matcher.
//
// Matchers fall into three groups:
//
//   - event predicates  — assert about a single *traceassert.Event
//   - selection/quantifiers — assert about a []*Event, *Trace, or *Conversation
//   - payload validation — decode and validate JetStream payloads via jsm.go
//
// All event predicates return M, a thin wrapper that adds fluent And/Or/Not so
// compositions read naturally: BePub().And(MatchReply(g)).
package match

import (
	"fmt"

	"github.com/onsi/gomega"
	gtypes "github.com/onsi/gomega/types"

	"github.com/synadia-labs/traceassert"
)

// M wraps a Gomega matcher with fluent And/Or/Not. It is itself a GomegaMatcher.
type M struct {
	gtypes.GomegaMatcher
}

func wrap(m gtypes.GomegaMatcher) M { return M{m} }

// And requires this matcher and all others to pass.
func (m M) And(others ...gtypes.GomegaMatcher) M {
	return wrap(gomega.And(append([]gtypes.GomegaMatcher{m.GomegaMatcher}, others...)...))
}

// Or requires this matcher or at least one other to pass.
func (m M) Or(others ...gtypes.GomegaMatcher) M {
	return wrap(gomega.Or(append([]gtypes.GomegaMatcher{m.GomegaMatcher}, others...)...))
}

// Not inverts this matcher.
func (m M) Not() M { return wrap(gomega.Not(m.GomegaMatcher)) }

// --- single-event matcher plumbing ---------------------------------------------

// eventMatcher adapts a boolean (plus optional detail) predicate over an *Event into
// a GomegaMatcher with line-numbered failure messages.
type eventMatcher struct {
	want   string
	check  func(*traceassert.Event) (bool, string)
	detail string
}

func eventPred(want string, check func(*traceassert.Event) bool) M {
	return eventDetail(want, func(e *traceassert.Event) (bool, string) { return check(e), "" })
}

func eventDetail(want string, check func(*traceassert.Event) (bool, string)) M {
	return wrap(&eventMatcher{want: want, check: check})
}

func (m *eventMatcher) Match(actual any) (bool, error) {
	e, ok := actual.(*traceassert.Event)
	if !ok {
		return false, fmt.Errorf("expected a *traceassert.Event, got %T", actual)
	}
	ok, detail := m.check(e)
	m.detail = detail
	return ok, nil
}

func (m *eventMatcher) FailureMessage(actual any) string {
	return fmt.Sprintf("expected event to %s\n  got: %s%s", m.want, describe(actual), suffix(m.detail))
}

func (m *eventMatcher) NegatedFailureMessage(actual any) string {
	return fmt.Sprintf("expected event not to %s\n  got: %s", m.want, describe(actual))
}

// --- slice/trace matcher plumbing ----------------------------------------------

// toEvents normalizes the supported actual types into an event slice so the
// selection/quantifier matchers accept a *Trace, []*Event, or *Conversation.
func toEvents(actual any) ([]*traceassert.Event, error) {
	switch v := actual.(type) {
	case *traceassert.Trace:
		return v.Events, nil
	case []*traceassert.Event:
		return v, nil
	case *traceassert.Conversation:
		return v.Events, nil
	default:
		return nil, fmt.Errorf("expected *Trace, []*Event or *Conversation, got %T", actual)
	}
}

type sliceMatcher struct {
	want   string
	check  func([]*traceassert.Event) (bool, string)
	detail string
}

func sliceCheck(want string, check func([]*traceassert.Event) (bool, string)) M {
	return wrap(&sliceMatcher{want: want, check: check})
}

func (m *sliceMatcher) Match(actual any) (bool, error) {
	evs, err := toEvents(actual)
	if err != nil {
		return false, err
	}
	ok, detail := m.check(evs)
	m.detail = detail
	return ok, nil
}

func (m *sliceMatcher) FailureMessage(any) string {
	return fmt.Sprintf("expected events to %s%s", m.want, suffix(m.detail))
}

func (m *sliceMatcher) NegatedFailureMessage(any) string {
	return fmt.Sprintf("expected events not to %s%s", m.want, suffix(m.detail))
}

// --- helpers -------------------------------------------------------------------

func runMatch(m gtypes.GomegaMatcher, e *traceassert.Event) bool {
	ok, err := m.Match(e)
	return err == nil && ok
}

func countMatching(evs []*traceassert.Event, m gtypes.GomegaMatcher) int {
	n := 0
	for _, e := range evs {
		if runMatch(m, e) {
			n++
		}
	}
	return n
}

func describe(actual any) string {
	if e, ok := actual.(*traceassert.Event); ok {
		return e.String()
	}
	return fmt.Sprintf("%v", actual)
}

func suffix(detail string) string {
	if detail == "" {
		return ""
	}
	return "\n  " + detail
}
