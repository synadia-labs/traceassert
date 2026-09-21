package traceassert

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Session is every connection a client made during one scenario: one Trace per
// connection, in header-timestamp order. A reconnect scenario produces one, and the
// point of keeping the traces apart rather than merging their events is request/reply
// pairing: RequestReplies pairs within each trace, so a reply that arrived on the
// second connection cannot answer a request that died with the first. That fault is
// what a reconnect scenario exists to catch, and a merged trace would hide it.
//
// Every event of a session carries Conn, the index of its trace in Traces, so a
// predicate can say which connection a frame belongs to.
type Session struct {
	Traces []*Trace
}

// NewSession assembles a session from traces in hand: it orders them by Header.Timestamp,
// then by the <n> in a <name>-<n>.expanded.json path with a bare <name> as 0, then by
// Path, and sets Conn on every event to its trace's index. LoadSession uses it; a test
// holding tracegen fixtures can call it directly.
func NewSession(traces ...*Trace) *Session {
	s := &Session{Traces: slices.Clone(traces)}
	slices.SortStableFunc(s.Traces, func(a, b *Trace) int {
		c := a.Header.Timestamp.Compare(b.Header.Timestamp)
		if c != 0 {
			return c
		}
		c = cmp.Compare(captureIndex(a.Path), captureIndex(b.Path))
		if c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	for i, tr := range s.Traces {
		for _, e := range tr.Events {
			e.Conn = i
		}
	}
	return s
}

// Events returns the events of every trace concatenated in session order.
func (s *Session) Events() []*Event {
	n := 0
	for _, tr := range s.Traces {
		n += len(tr.Events)
	}
	out := make([]*Event, 0, n)
	for _, tr := range s.Traces {
		out = append(out, tr.Events...)
	}
	return out
}

// Select returns the events matching p across every trace, in session order.
func (s *Session) Select(p Predicate) []*Event {
	var out []*Event
	for _, tr := range s.Traces {
		out = append(out, tr.Select(p)...)
	}
	return out
}

// First returns the first event matching p across every trace, in session order.
func (s *Session) First(p Predicate) (*Event, bool) {
	for _, tr := range s.Traces {
		e, ok := tr.First(p)
		if ok {
			return e, true
		}
	}
	return nil, false
}

// Count returns how many events match p across every trace.
func (s *Session) Count(p Predicate) int {
	n := 0
	for _, tr := range s.Traces {
		n += tr.Count(p)
	}
	return n
}

// RequestReplies runs Trace.RequestReplies on each trace and concatenates the pairs,
// so a request is only ever paired with a response on its own connection. A request
// whose connection closed before the reply arrived has a nil Response even when the
// reply subject was later delivered on another connection.
func (s *Session) RequestReplies(isReq Predicate) []ReqResp {
	var out []ReqResp
	for _, tr := range s.Traces {
		out = append(out, tr.RequestReplies(isReq)...)
	}
	return out
}

// ClientView returns a session of each trace's ClientView, in the same order. The
// events are shared with the full session, so Conn is untouched.
func (s *Session) ClientView() *Session {
	views := make([]*Trace, len(s.Traces))
	for i, tr := range s.Traces {
		views[i] = tr.ClientView()
	}
	return &Session{Traces: views}
}

// ServerView returns a session of each trace's ServerView, in the same order.
func (s *Session) ServerView() *Session {
	views := make([]*Trace, len(s.Traces))
	for i, tr := range s.Traces {
		views[i] = tr.ServerView()
	}
	return &Session{Traces: views}
}

// LoadSession loads every capture of the scenario name: <name>.expanded.json and each
// <name>-<n>.expanded.json, with <n> a run of digits, from the directory CapturePath
// resolves name into. The harness writes a scenario's captures under these names, one
// per connection, in the order the proxy closed them. A pattern rather than a fixed
// list because the number of connections is what is under test: a client that
// reconnected once more than expected leaves a capture the suite must read.
//
// The traces are ordered by Header.Timestamp, which the proxy sets at accept, with the
// <n> of the filename as the tie-break. Matching is on the name, not a glob, so "fast" does not
// sweep in the sibling scenario "fast-lostack". No matching file is an error naming
// the directory and the names tried, and a truncated capture is refused as LoadCapture
// refuses it: the proxy writes a footer on every close, an induced disconnect included,
// so a missing footer means the capture was cut short, not that the client reconnected.
func LoadSession(name string) (*Session, error) {
	path := CapturePath(name)
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("session %q: %w", name, err)
	}

	var traces []*Trace
	for _, entry := range entries {
		if entry.IsDir() || !isSessionCapture(base, entry.Name()) {
			continue
		}
		tr, err := loadCompleteCapture(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("session %q: %w", name, err)
		}
		traces = append(traces, tr)
	}
	if len(traces) == 0 {
		return nil, fmt.Errorf("session %q: no capture named %s.expanded.json or %s-<n>.expanded.json in %s",
			name, base, base, dir)
	}
	return NewSession(traces...), nil
}

// isSessionCapture reports whether file is a capture of the scenario name:
// <name>.expanded.json or <name>-<n>.expanded.json with <n> one or more digits.
func isSessionCapture(name, file string) bool {
	rest, ok := strings.CutPrefix(file, name)
	if !ok {
		return false
	}
	rest, ok = strings.CutSuffix(rest, ".expanded.json")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	digits, ok := strings.CutPrefix(rest, "-")
	if !ok || digits == "" {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// captureIndex is the <n> of a <name>-<n>.expanded.json path, and 0 for a path with no
// such suffix, so that with equal header timestamps a session keeps the order the
// harness wrote its captures in.
func captureIndex(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), ".expanded.json")
	i := strings.LastIndex(base, "-")
	if i < 0 {
		return 0
	}
	n := 0
	for _, r := range base[i+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
