package match

import (
	"fmt"
	"strings"

	gtypes "github.com/onsi/gomega/types"

	"github.com/synadia-labs/traceassert"
)

// --- presence / position -------------------------------------------------------

// ContainEvent matches when at least one event satisfies m.
func ContainEvent(m gtypes.GomegaMatcher) M {
	return sliceCheck("contain a matching event", func(evs []*traceassert.Event) (bool, string) {
		if countMatching(evs, m) > 0 {
			return true, ""
		}
		return false, "no event matched"
	})
}

// HaveFirst matches when the first event satisfies m.
func HaveFirst(m gtypes.GomegaMatcher) M {
	return sliceCheck("have a first event matching", func(evs []*traceassert.Event) (bool, string) {
		if len(evs) == 0 {
			return false, "no events"
		}
		if runMatch(m, evs[0]) {
			return true, ""
		}
		return false, "first event: " + evs[0].String()
	})
}

// EndWith matches when the last event satisfies m.
func EndWith(m gtypes.GomegaMatcher) M {
	return sliceCheck("end with a matching event", func(evs []*traceassert.Event) (bool, string) {
		if len(evs) == 0 {
			return false, "no events"
		}
		last := evs[len(evs)-1]
		if runMatch(m, last) {
			return true, ""
		}
		return false, "last event: " + last.String()
	})
}

// --- quantifiers ---------------------------------------------------------------

// Each matches when every event satisfies m.
func Each(m gtypes.GomegaMatcher) M {
	return sliceCheck("each match", func(evs []*traceassert.Event) (bool, string) {
		for _, e := range evs {
			if !runMatch(m, e) {
				return false, "first non-matching: " + e.String()
			}
		}
		return true, ""
	})
}

// Exactly matches when exactly n events satisfy m.
func Exactly(n int, m gtypes.GomegaMatcher) M {
	return sliceCheck(fmt.Sprintf("contain exactly %d matching events", n), func(evs []*traceassert.Event) (bool, string) {
		got := countMatching(evs, m)
		if got == n {
			return true, ""
		}
		return false, fmt.Sprintf("got %d", got)
	})
}

// AtLeast matches when at least n events satisfy m.
func AtLeast(n int, m gtypes.GomegaMatcher) M {
	return sliceCheck(fmt.Sprintf("contain at least %d matching events", n), func(evs []*traceassert.Event) (bool, string) {
		got := countMatching(evs, m)
		if got >= n {
			return true, ""
		}
		return false, fmt.Sprintf("got %d", got)
	})
}

// Never matches when no event satisfies m.
func Never(m gtypes.GomegaMatcher) M {
	return sliceCheck("never match", func(evs []*traceassert.Event) (bool, string) {
		for _, e := range evs {
			if runMatch(m, e) {
				return false, "matched at " + e.String()
			}
		}
		return true, ""
	})
}

// ContainInOrder matches when the events contain a subsequence (not necessarily
// adjacent) satisfying the given step matchers in order.
func ContainInOrder(steps ...gtypes.GomegaMatcher) M {
	return sliceCheck("contain matching events in order", func(evs []*traceassert.Event) (bool, string) {
		i := 0
		for _, e := range evs {
			if i < len(steps) && runMatch(steps[i], e) {
				i++
			}
		}
		if i == len(steps) {
			return true, ""
		}
		return false, fmt.Sprintf("matched %d of %d ordered steps", i, len(steps))
	})
}

// --- request / reply -----------------------------------------------------------

type reqRespMatcher struct {
	reqM, respM gtypes.GomegaMatcher
	detail      string
}

// RequestReply correlates request/reply exchanges over a *Trace: every event
// matching reqM (that carries a reply) must have a server response delivered to that
// reply subject satisfying respM.
func RequestReply(reqM, respM gtypes.GomegaMatcher) M {
	return wrap(&reqRespMatcher{reqM: reqM, respM: respM})
}

func (m *reqRespMatcher) Match(actual any) (bool, error) {
	tr, ok := actual.(*traceassert.Trace)
	if !ok {
		return false, fmt.Errorf("RequestReply expects a *traceassert.Trace, got %T", actual)
	}
	pairs := tr.RequestReplies(func(e *traceassert.Event) bool { return runMatch(m.reqM, e) })
	if len(pairs) == 0 {
		m.detail = "no matching requests found"
		return false, nil
	}
	for _, p := range pairs {
		if p.Response == nil {
			m.detail = fmt.Sprintf("request at line %d has no correlated response", p.Request.Line)
			return false, nil
		}
		if !runMatch(m.respM, p.Response) {
			m.detail = fmt.Sprintf("response at line %d did not match", p.Response.Line)
			return false, nil
		}
	}
	return true, nil
}

func (m *reqRespMatcher) FailureMessage(any) string {
	return "expected every matching request to have a matching response" + suffix(m.detail)
}
func (m *reqRespMatcher) NegatedFailureMessage(any) string {
	return "expected some request/response pairing to fail" + suffix(m.detail)
}

// --- happens-before ------------------------------------------------------------

// Wait is a partial matcher; complete it with Before.
type Wait struct{ resp gtypes.GomegaMatcher }

// WaitForReply begins a happens-before assertion: the response matcher must occur
// before the event given to Before.
func WaitForReply(resp gtypes.GomegaMatcher) Wait { return Wait{resp} }

// Before completes WaitForReply: it fails if any event matching next occurs before
// the first event matching the response matcher.
func (w Wait) Before(next gtypes.GomegaMatcher) M {
	return sliceCheck("see the awaited reply before the next event", func(evs []*traceassert.Event) (bool, string) {
		firstResp := -1
		for i, e := range evs {
			if runMatch(w.resp, e) {
				firstResp = i
				break
			}
		}
		for i, e := range evs {
			if runMatch(next, e) {
				if firstResp == -1 || i < firstResp {
					return false, fmt.Sprintf("line %d occurred before the awaited reply", e.Line)
				}
				break
			}
		}
		return true, ""
	})
}

// HaveFinalReply matches when the last server→client event satisfies m.
func HaveFinalReply(m gtypes.GomegaMatcher) M {
	return sliceCheck("have a final server reply matching", func(evs []*traceassert.Event) (bool, string) {
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Dir == traceassert.FromServer {
				if runMatch(m, evs[i]) {
					return true, ""
				}
				return false, "final reply: " + evs[i].String()
			}
		}
		return false, "no server replies found"
	})
}

// nuidLength is the length of a nats-io/nuid token (preLen 12 + seqLen 10).
const nuidLength = 22

// UseOldStyleInbox asserts the client subscribed a dedicated old-style inbox of the
// form <prefix>.<nuid> (optionally <prefix>.<nuid>.> for a wildcard inbox) before
// publishing any request that replies under it. <nuid> must be a fresh nats-io/nuid
// token (22 base62 chars) — this distinguishes a real per-request / per-batch inbox
// from a shared mux inbox (<prefix>.<base>.*, reused across requests) and from a
// hand-rolled reply subject.
//
// prefix defaults to "_INBOX" when empty.
func UseOldStyleInbox(prefix string) M {
	if prefix == "" {
		prefix = "_INBOX"
	}
	prefixToks := strings.Split(prefix, ".")
	want := fmt.Sprintf("subscribe a dedicated old-style inbox %s.<nuid> before publishing", prefix)

	return sliceCheck(want, func(evs []*traceassert.Event) (bool, string) {
		type inbox struct {
			idx     int
			subject string // <prefix>.<nuid>
		}
		var inboxes []inbox
		for i, e := range evs {
			if e.Verb != "SUB" {
				continue
			}
			toks := strings.Split(e.Subject, ".")
			rest, ok := afterPrefix(toks, prefixToks)
			if !ok {
				continue
			}
			// accept <prefix>.<nuid> or <prefix>.<nuid>.> ; reject mux <prefix>.<nuid>.*
			if len(rest) == 0 || len(rest) > 2 || (len(rest) == 2 && rest[1] != ">") {
				continue
			}
			if !isNUID(rest[0]) {
				continue
			}
			inboxes = append(inboxes, inbox{idx: i, subject: prefix + "." + rest[0]})
		}
		if len(inboxes) == 0 {
			return false, fmt.Sprintf("no SUB to an old-style inbox %s.<nuid> (nuid must be %d base62 chars)", prefix, nuidLength)
		}

		for _, ib := range inboxes {
			for j, e := range evs {
				if e.Dir != traceassert.ToServer || e.Reply == "" {
					continue
				}
				if e.Reply == ib.subject || strings.HasPrefix(e.Reply, ib.subject+".") {
					if ib.idx < j {
						return true, ""
					}
					return false, fmt.Sprintf("inbox SUB (line %d) is not before the request replying under it (line %d)",
						evs[ib.idx].Line, e.Line)
				}
			}
		}
		return false, fmt.Sprintf("subscribed %s.<nuid> but no request replies under it", prefix)
	})
}

// replySuffixLen is the length of a nats.go new-style reply suffix (newRespInbox).
const replySuffixLen = 8

// UseNewStyleInbox asserts the client used a new-style mux response inbox: a single
// shared subscription <prefix>.<nuid>.* under which each request gets a distinct reply
// subject <prefix>.<nuid>.<suffix>, where <nuid> is a nats-io/nuid token (22 base62
// chars) and <suffix> is a nats.go reply suffix (8 base62 chars; see newRespInbox).
// It is the counterpart of UseOldStyleInbox.
//
// prefix defaults to "_INBOX" when empty.
func UseNewStyleInbox(prefix string) M {
	if prefix == "" {
		prefix = "_INBOX"
	}
	prefixToks := strings.Split(prefix, ".")
	want := fmt.Sprintf("use a new-style mux inbox %s.<nuid>.* before publishing", prefix)

	return sliceCheck(want, func(evs []*traceassert.Event) (bool, string) {
		type mux struct {
			idx  int
			base string // <prefix>.<nuid>
		}
		var muxes []mux
		for i, e := range evs {
			if e.Verb != "SUB" {
				continue
			}
			toks := strings.Split(e.Subject, ".")
			rest, ok := afterPrefix(toks, prefixToks)
			if !ok || len(rest) != 2 || rest[1] != "*" || !isNUID(rest[0]) {
				continue
			}
			muxes = append(muxes, mux{idx: i, base: prefix + "." + rest[0]})
		}
		if len(muxes) == 0 {
			return false, fmt.Sprintf("no SUB to a mux inbox %s.<nuid>.*", prefix)
		}

		for _, m := range muxes {
			for j, e := range evs {
				if e.Dir != traceassert.ToServer || e.Reply == "" {
					continue
				}
				suffix, ok := strings.CutPrefix(e.Reply, m.base+".")
				if !ok || strings.Contains(suffix, ".") {
					continue // not a single-token reply under this mux
				}
				if !isReplySuffix(suffix) {
					return false, fmt.Sprintf("reply %q under mux %s is not a %d-char nats.go reply suffix",
						e.Reply, m.base, replySuffixLen)
				}
				if m.idx < j {
					return true, ""
				}
				return false, fmt.Sprintf("mux SUB (line %d) is not before the request replying under it (line %d)",
					evs[m.idx].Line, e.Line)
			}
		}
		return false, fmt.Sprintf("subscribed mux %s.<nuid>.* but no request replies with a %d-char suffix under it",
			prefix, replySuffixLen)
	})
}

// afterPrefix returns the tokens following prefix, and whether toks starts with prefix.
func afterPrefix(toks, prefix []string) ([]string, bool) {
	if len(toks) < len(prefix) {
		return nil, false
	}
	for i := range prefix {
		if toks[i] != prefix[i] {
			return nil, false
		}
	}
	return toks[len(prefix):], true
}

// isNUID reports whether s is a nats-io/nuid token: nuidLength base62 characters.
func isNUID(s string) bool { return len(s) == nuidLength && isBase62(s) }

// isReplySuffix reports whether s is a nats.go new-style reply suffix: replySuffixLen
// base62 characters.
func isReplySuffix(s string) bool { return len(s) == replySuffixLen && isBase62(s) }

// isBase62 reports whether s consists solely of [0-9A-Za-z] (the nats-io nuid/reply
// alphabet).
func isBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return len(s) > 0
}
