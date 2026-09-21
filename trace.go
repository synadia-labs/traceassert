// Package traceassert is a generic framework for asserting that a NATS client, as
// captured in an ADR-2 trace file, used NATS correctly per a given ADR.
//
// This file is the event model. Every protocol frame in the trace becomes an Event —
// including CONNECT, INFO and -ERR — so suites can assert on the handshake and error
// handling, not just publishes and subscriptions.
package traceassert

import (
	"fmt"
	"strings"
	"time"
)

// Direction is the travel direction of a frame.
type Direction int

const (
	// ToServer is a frame the client sent to the server (trace "dir":"backend").
	// These are the things the client did.
	ToServer Direction = iota
	// FromServer is a frame the server delivered to the client (trace "dir":"client").
	// These are the server's responses.
	FromServer
)

func (d Direction) String() string {
	if d == FromServer {
		return "from_server"
	}
	return "to_server"
}

// Event is one decoded protocol frame, normalized for assertion.
type Event struct {
	Line    int                 // 1-based line in the trace file (diagnostics)
	At      time.Time           // frame timestamp
	ID      string              // tracer-assigned frame id
	Dir     Direction           // travel direction
	Verb    string              // PUB, HPUB, SUB, UNSUB, MSG, HMSG, CONNECT, INFO, -ERR, PING, PONG
	Subject string              // delivery / publish subject
	Reply   string              // reply-to / inbox (PUB/HPUB/MSG/HMSG)
	SID     string              // subscription id (SUB/UNSUB/MSG)
	Queue   string              // first queue group, if any
	Header  map[string][]string // parsed HPUB/HMSG headers (nil otherwise)
	Payload []byte              // message body; for CONNECT/INFO the JSON, for -ERR the error text

	// WireBytes is the size of the original on-the-wire frame in bytes, including the
	// verb, subject, headers and protocol framing — not just the payload. It is the
	// length of the raw ADR-2 frame the capture recorded; 0 when unknown (e.g. an
	// expanded document written before this field existed).
	WireBytes int

	// Shaped is set when a traffic-shaping proxy acted on the frame, and nil on every
	// frame the proxy left alone.
	Shaped *Shaped

	// Conn is the index of the event's trace within its Session: 0 for the first
	// connection the client made, 1 for the next, and so on. It is set when a Session
	// is assembled and is 0 on every event of a trace loaded on its own. It is not part
	// of the expanded format.
	Conn int

	tokens []string // lazy: Subject split on '.'
}

// Shaped records that a traffic-shaping proxy acted on a frame. Rule is the id of the
// shaping rule that fired (e.g. "drop-ack-30"). Action is the string the proxy wrote
// for what it did to the frame, such as "drop", "stall", "throttle" or "disconnect";
// it is not a constant this package defines.
type Shaped struct {
	Rule   string
	Action string
}

// Tokens returns the subject split on '.', computed once.
func (e *Event) Tokens() []string {
	if e.tokens == nil && e.Subject != "" {
		e.tokens = strings.Split(e.Subject, ".")
	}
	return e.tokens
}

// HeaderGet returns the first value of a header, matched case-insensitively.
func (e *Event) HeaderGet(key string) (string, bool) {
	for k, v := range e.Header {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0], true
		}
	}
	return "", false
}

// IsRequest reports whether the event carries a reply subject (a request).
func (e *Event) IsRequest() bool { return e.Reply != "" }

// Dropped reports whether the proxy never forwarded the frame: Shaped is set and its
// Action is "drop" or "disconnect". A stalled or throttled frame was delivered late
// and is not dropped. This is the one definition the match predicates and the views
// share.
func (e *Event) Dropped() bool {
	if e.Shaped == nil {
		return false
	}
	return e.Shaped.Action == "drop" || e.Shaped.Action == "disconnect"
}

func (e *Event) String() string {
	return fmt.Sprintf("line %d %s %s %q", e.Line, e.Dir, e.Verb, e.Subject)
}

// Header mirrors the ADR-2 trace header (the connection-metadata line). It is plain
// data, copied here so the assertion side carries no dependency on the capture and
// protocol-parsing packages that produce a trace.
type Header struct {
	Version     int       `json:"version"`
	Device      string    `json:"device"`
	Timestamp   time.Time `json:"ts"`
	CUUID       string    `json:"cuuid"`
	PortName    string    `json:"port"`
	Src         string    `json:"src"`
	SrcPort     int       `json:"spr"`
	Dst         string    `json:"dst"`
	DstPort     int       `json:"dpt"`
	BackendDst  string    `json:"bdst"`
	BackendPort int       `json:"bdpt"`
	Protocol    string    `json:"protocol"`
	Profile     struct {
		UUID string `json:"uuid"`
	} `json:"profile"`
	File string `json:"file"`
}

// Footer mirrors the ADR-2 completion line.
type Footer struct {
	Timestamp time.Time `json:"ts"`
	Duration  int64     `json:"duration"`
}

// Trace is a fully decoded trace file plus query helpers.
type Trace struct {
	Header Header
	Events []*Event
	Footer *Footer // nil when the trace was truncated (no footer line)
	Path   string
}

// Truncated reports whether the trace ended without a footer line — e.g. cut short
// by the tracer's MaxSize/MaxTime. Assertions use this to choose Inconclusive over
// Fail when required evidence is simply absent.
func (t *Trace) Truncated() bool { return t.Footer == nil }

// Predicate is a simple event test used by the core selection helpers. The matcher
// layer adds Gomega-matcher-based overloads on top of these.
type Predicate func(*Event) bool

// Select returns the events matching p, preserving trace order.
func (t *Trace) Select(p Predicate) []*Event {
	var out []*Event
	for _, e := range t.Events {
		if p(e) {
			out = append(out, e)
		}
	}
	return out
}

// First returns the first event matching p.
func (t *Trace) First(p Predicate) (*Event, bool) {
	for _, e := range t.Events {
		if p(e) {
			return e, true
		}
	}
	return nil, false
}

// Count returns how many events match p.
func (t *Trace) Count(p Predicate) int {
	n := 0
	for _, e := range t.Events {
		if p(e) {
			n++
		}
	}
	return n
}

// ClientView returns the trace as the client saw it: the same Header, Footer and
// Path, and every event except a dropped FromServer frame, which the proxy never
// forwarded to the client. The events are shared, not copied, so Line, ID and Conn
// are those of the full capture and a failure still names the frame in the file.
// Every matcher runs over a view unchanged; a lost ack read through the client view
// is a request with no response.
func (t *Trace) ClientView() *Trace { return t.view(FromServer) }

// ServerView returns the trace as the server saw it: every event except a dropped
// ToServer frame, which the proxy never forwarded to the server. See ClientView.
func (t *Trace) ServerView() *Trace { return t.view(ToServer) }

// view returns a trace without the dropped frames traveling in dir.
func (t *Trace) view(dir Direction) *Trace {
	out := &Trace{Header: t.Header, Footer: t.Footer, Path: t.Path}
	out.Events = make([]*Event, 0, len(t.Events))
	for _, e := range t.Events {
		if e.Dir == dir && e.Dropped() {
			continue
		}
		out.Events = append(out.Events, e)
	}
	return out
}
