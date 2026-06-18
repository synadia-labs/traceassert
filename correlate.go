package traceassert

import "github.com/synadia-labs/traceassert/subject"

// KeyFunc derives a correlation key from an event, reporting ok=false when the event
// has no key (and should be excluded from grouping).
type KeyFunc func(*Event) (string, bool)

// Conversation is the set of events sharing a correlation key, in trace order.
type Conversation struct {
	Key    string
	Events []*Event
}

// ToServer returns the conversation's client→server events (requests/publishes).
func (c *Conversation) ToServer() []*Event { return filterDir(c.Events, ToServer) }

// FromServer returns the conversation's server→client events (responses).
func (c *Conversation) FromServer() []*Event { return filterDir(c.Events, FromServer) }

func filterDir(evs []*Event, d Direction) []*Event {
	var out []*Event
	for _, e := range evs {
		if e.Dir == d {
			out = append(out, e)
		}
	}
	return out
}

// Conversations is an ordered set of conversations (first-seen key order).
type Conversations []*Conversation

// One returns the single conversation, ok=false if there is not exactly one.
func (cs Conversations) One() (*Conversation, bool) {
	if len(cs) != 1 {
		return nil, false
	}
	return cs[0], true
}

// Get returns the conversation for key.
func (cs Conversations) Get(key string) (*Conversation, bool) {
	for _, c := range cs {
		if c.Key == key {
			return c, true
		}
	}
	return nil, false
}

// GroupBy partitions the trace into conversations by key, preserving first-seen key
// order so results are deterministic. Events for which key reports ok=false are
// dropped.
func (t *Trace) GroupBy(key KeyFunc) Conversations {
	var order []string
	index := map[string]*Conversation{}
	for _, e := range t.Events {
		k, ok := key(e)
		if !ok {
			continue
		}
		c := index[k]
		if c == nil {
			c = &Conversation{Key: k}
			index[k] = c
			order = append(order, k)
		}
		c.Events = append(c.Events, e)
	}
	out := make(Conversations, 0, len(order))
	for _, k := range order {
		out = append(out, index[k])
	}
	return out
}

// ByReply keys on the full reply subject.
func ByReply() KeyFunc {
	return func(e *Event) (string, bool) {
		if e.Reply == "" {
			return "", false
		}
		return e.Reply, true
	}
}

// ByHeader keys on a header value (case-insensitive name).
func ByHeader(name string) KeyFunc {
	return func(e *Event) (string, bool) { return e.HeaderGet(name) }
}

// BySubjectToken keys on the i-th (0-based) subject token.
func BySubjectToken(i int) KeyFunc {
	return func(e *Event) (string, bool) {
		toks := e.Tokens()
		if i < 0 || i >= len(toks) {
			return "", false
		}
		return toks[i], true
	}
}

// ByCapture keys on a named capture of a subject grammar. It tries the event's
// Subject first, then its Reply, so a reply-encoded grammar (e.g. the fast-ingest
// $FI reply) groups publishes while a subject-encoded grammar groups by subject.
func ByCapture(g *subject.Grammar, name string) KeyFunc {
	return func(e *Event) (string, bool) {
		if e.Subject != "" {
			if caps, ok := g.Match(e.Subject); ok {
				if v, ok := caps.Str(name); ok {
					return v, true
				}
			}
		}
		if e.Reply != "" {
			if caps, ok := g.Match(e.Reply); ok {
				if v, ok := caps.Str(name); ok {
					return v, true
				}
			}
		}
		return "", false
	}
}

// ReqResp pairs a request with its correlated response (Response is nil if none was
// found in the trace).
type ReqResp struct {
	Request  *Event
	Response *Event
}

// RequestReplies correlates request/reply exchanges: for each ToServer event that
// matches isReq and carries a reply subject, it finds the first later FromServer
// event delivered to that reply subject. This is exact correlation — the request's
// reply subject is the response's delivery subject — and needs no inbox heuristics.
func (t *Trace) RequestReplies(isReq Predicate) []ReqResp {
	var out []ReqResp
	for i, e := range t.Events {
		if e.Dir != ToServer || e.Reply == "" || !isReq(e) {
			continue
		}
		rr := ReqResp{Request: e}
		for _, r := range t.Events[i+1:] {
			if r.Dir == FromServer && r.Subject == e.Reply {
				rr.Response = r
				break
			}
		}
		out = append(out, rr)
	}
	return out
}
