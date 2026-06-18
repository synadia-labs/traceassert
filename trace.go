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

	tokens []string // lazy: Subject split on '.'
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
