package match

import (
	"fmt"
	"strconv"

	"github.com/onsi/gomega"
	gtypes "github.com/onsi/gomega/types"
	"github.com/tidwall/gjson"

	"github.com/synadia-labs/traceassert"
	"github.com/synadia-labs/traceassert/subject"
)

// --- direction -----------------------------------------------------------------

// ToServer matches a frame the client sent to the server.
func ToServer() M {
	return eventPred("be sent to the server", func(e *traceassert.Event) bool { return e.Dir == traceassert.ToServer })
}

// FromServer matches a frame the server delivered to the client.
func FromServer() M {
	return eventPred("be delivered from the server", func(e *traceassert.Event) bool { return e.Dir == traceassert.FromServer })
}

// --- verb ----------------------------------------------------------------------

// BeVerb matches a specific protocol verb (PUB, HPUB, SUB, ...).
func BeVerb(verb string) M {
	return eventPred(fmt.Sprintf("be a %s", verb), func(e *traceassert.Event) bool { return e.Verb == verb })
}

// BePub matches a PUB frame: a client publish without headers.
func BePub() M { return BeVerb("PUB") }

// BeHPub matches an HPUB frame: a client publish carrying headers.
func BeHPub() M { return BeVerb("HPUB") }

// BeSub matches a SUB frame: a client subscription.
func BeSub() M { return BeVerb("SUB") }

// BeUnsub matches an UNSUB frame: a client unsubscribe.
func BeUnsub() M { return BeVerb("UNSUB") }

// BeMsg matches a MSG frame: a message the server delivered to the client, without headers.
func BeMsg() M { return BeVerb("MSG") }

// BeHMsg matches an HMSG frame: a message the server delivered to the client, with headers.
func BeHMsg() M { return BeVerb("HMSG") }

// BeConnect matches a CONNECT frame: the client's connect handshake.
func BeConnect() M { return BeVerb("CONNECT") }

// --- request / reply -----------------------------------------------------------

// BeRequest matches an event carrying a reply subject.
func BeRequest() M {
	return eventPred("be a request (carry a reply subject)", func(e *traceassert.Event) bool { return e.IsRequest() })
}

// HaveNoReply matches an event with no reply subject.
func HaveNoReply() M {
	return eventPred("have no reply subject", func(e *traceassert.Event) bool { return e.Reply == "" })
}

// HaveReply applies an inner matcher to the event's reply subject.
func HaveReply(m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string { return e.Reply }, m))
}

// --- subject -------------------------------------------------------------------

// HaveSubject matches an exact subject string.
func HaveSubject(subj string) M {
	return eventPred(fmt.Sprintf("have subject %q", subj), func(e *traceassert.Event) bool { return e.Subject == subj })
}

// MatchSubject matches when the event's subject conforms to the grammar.
func MatchSubject(g *subject.Grammar) M {
	return eventPred(fmt.Sprintf("match subject grammar %q", g), func(e *traceassert.Event) bool { return g.Matches(e.Subject) })
}

// MatchReply matches when the event's reply subject conforms to the grammar.
func MatchReply(g *subject.Grammar) M {
	return eventPred(fmt.Sprintf("match reply grammar %q", g), func(e *traceassert.Event) bool { return g.Matches(e.Reply) })
}

// SubjectToken applies an inner matcher to the i-th (0-based) subject token.
func SubjectToken(i int, m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string {
		toks := e.Tokens()
		if i < 0 || i >= len(toks) {
			return ""
		}
		return toks[i]
	}, m))
}

// SubjectCapture matches when the subject conforms to g and the named capture
// satisfies m. Numeric captures are compared as ints (so Equal(0) works).
func SubjectCapture(g *subject.Grammar, name string, m gtypes.GomegaMatcher) M {
	return capture(func(e *traceassert.Event) string { return e.Subject }, g, name, m)
}

// ReplyCapture is SubjectCapture against the reply subject.
func ReplyCapture(g *subject.Grammar, name string, m gtypes.GomegaMatcher) M {
	return capture(func(e *traceassert.Event) string { return e.Reply }, g, name, m)
}

func capture(get func(*traceassert.Event) string, g *subject.Grammar, name string, m gtypes.GomegaMatcher) M {
	return eventDetail(fmt.Sprintf("match grammar %q with capture %q satisfying the inner matcher", g, name),
		func(e *traceassert.Event) (bool, string) {
			s := get(e)
			caps, ok := g.Match(s)
			if !ok {
				return false, fmt.Sprintf("%q does not match grammar %q", s, g)
			}
			v, ok := caps.Str(name)
			if !ok {
				return false, fmt.Sprintf("grammar %q has no capture %q", g, name)
			}
			// Numeric captures compare as ints (so Equal(0) works); others as strings.
			// A real value is always applied to the inner matcher - never a nil, which
			// some Gomega matchers (BeTrue/BeFalse) panic on rather than failing.
			n, err := strconv.Atoi(v)
			if err == nil {
				return applyTyped(m, n)
			}
			return applyTyped(m, v)
		})
}

// --- sid / queue ---------------------------------------------------------------

// HaveSID applies an inner matcher to the subscription id.
func HaveSID(m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string { return e.SID }, m))
}

// HaveQueueGroup applies an inner matcher to the (first) queue group.
func HaveQueueGroup(m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string { return e.Queue }, m))
}

// --- headers -------------------------------------------------------------------

// HaveHeader matches when a header is present (case-insensitive name).
func HaveHeader(name string) M {
	return eventPred(fmt.Sprintf("have header %q", name), func(e *traceassert.Event) bool {
		_, ok := e.HeaderGet(name)
		return ok
	})
}

// HaveNoHeader matches when a header is absent.
func HaveNoHeader(name string) M {
	return eventPred(fmt.Sprintf("not have header %q", name), func(e *traceassert.Event) bool {
		_, ok := e.HeaderGet(name)
		return !ok
	})
}

// HaveHeaderValue applies an inner matcher to a header's first value.
func HaveHeaderValue(name string, m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string {
		v, _ := e.HeaderGet(name)
		return v
	}, m))
}

// --- payload -------------------------------------------------------------------

// HavePayload applies an inner matcher to the raw payload bytes (as a string).
func HavePayload(m gtypes.GomegaMatcher) M {
	return wrap(gomega.WithTransform(func(e *traceassert.Event) string { return string(e.Payload) }, m))
}

// PayloadIsEmpty matches an empty payload.
func PayloadIsEmpty() M {
	return eventPred("have an empty payload", func(e *traceassert.Event) bool { return len(e.Payload) == 0 })
}

// PayloadJSON applies an inner matcher to a gjson path of the payload. JSON numbers
// arrive as float64, so use BeNumerically("==", n) for numeric comparisons. This is
// the fallback for payloads outside the jsm.go schema registry.
//
// A path that is absent from the payload fails cleanly ("payload has no JSON value at
// path X") rather than feeding a nil to the inner matcher, which some Gomega matchers
// (BeTrue/BeFalse) panic on rather than failing. A path that is present but explicitly
// JSON null is still handed to the inner matcher as nil.
func PayloadJSON(path string, m gtypes.GomegaMatcher) M {
	return eventDetail(fmt.Sprintf("have a JSON value at %q satisfying the inner matcher", path),
		func(e *traceassert.Event) (bool, string) {
			r := gjson.GetBytes(e.Payload, path)
			if !r.Exists() {
				return false, fmt.Sprintf("payload has no JSON value at path %q", path)
			}
			return applyTyped(m, r.Value())
		})
}
