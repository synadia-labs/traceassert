package traceassert

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"time"
)

// The expanded format is a fully pre-parsed trace: the event stream that the
// capture/parsing side produces from an ADR-2 trace. It is written as JSON Lines — a
// header line, then one line per event, then a footer line — which mirrors the ADR-2
// source format and, crucially, lets both the writer and the reader stream: neither
// ever holds more than a single frame in memory, so an arbitrarily large trace can be
// produced and consumed in bounded space. Reading it back requires nothing but
// encoding/json — no protocol parser — which is what lets the assertion side of
// traceassert stand on its own, away from the proxy and the protocol model that
// produced the capture.
const (
	// ExpandedFormat tags the header line so a stray JSON file can't be mistaken for one.
	ExpandedFormat = "traceassert-expanded"
	// ExpandedVersion is bumped on any breaking change to the on-disk shape.
	ExpandedVersion = 2
)

// expandedHeaderLine is the first line of an expanded document: the format tag, the
// version and the trace Header. Keeping it on its own line (rather than wrapping the
// whole trace in one object) is what makes the format streamable on both ends.
type expandedHeaderLine struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Header  Header `json:"header"`
}

// expandedEvent mirrors Event for serialization, one per line. Dir is written as its
// string form ("to_server"/"from_server") rather than the raw enum, and Payload rides
// along as base64 (encoding/json's default for []byte).
type expandedEvent struct {
	Line    int                 `json:"line"`
	At      time.Time           `json:"at"`
	ID      string              `json:"id,omitempty"`
	Dir     string              `json:"dir"`
	Verb    string              `json:"verb,omitempty"`
	Subject string              `json:"subject,omitempty"`
	Reply   string              `json:"reply,omitempty"`
	SID     string              `json:"sid,omitempty"`
	Queue   string              `json:"queue,omitempty"`
	Header  map[string][]string `json:"header,omitempty"`
	Payload []byte              `json:"payload,omitempty"`
	Bytes   int                 `json:"bytes,omitempty"`
}

// expandedFooterLine is the last line, written only when the trace completed (had a
// footer). Its distinctive "footer" key is how the reader tells it apart from an event
// line without paying for a per-line type tag on every frame.
type expandedFooterLine struct {
	Footer Footer `json:"footer"`
}

func toExpandedEvent(e *Event) expandedEvent {
	return expandedEvent{
		Line:    e.Line,
		At:      e.At,
		ID:      e.ID,
		Dir:     e.Dir.String(),
		Verb:    e.Verb,
		Subject: e.Subject,
		Reply:   e.Reply,
		SID:     e.SID,
		Queue:   e.Queue,
		Header:  e.Header,
		Payload: e.Payload,
		Bytes:   e.WireBytes,
	}
}

func (ee *expandedEvent) toEvent() *Event {
	dir := ToServer
	if ee.Dir == FromServer.String() {
		dir = FromServer
	}
	return &Event{
		Line:      ee.Line,
		At:        ee.At,
		ID:        ee.ID,
		Dir:       dir,
		Verb:      ee.Verb,
		Subject:   ee.Subject,
		Reply:     ee.Reply,
		SID:       ee.SID,
		Queue:     ee.Queue,
		Header:    ee.Header,
		Payload:   ee.Payload,
		WireBytes: ee.Bytes,
	}
}

// ExpandedWriter streams an expanded document as JSON Lines: a header line (written
// immediately), then one line per event, then — unless the trace was truncated — a
// footer line. A producer (e.g. a capture→expanded converter) can therefore write a
// trace of any size by calling WriteEvent once per frame; nothing is retained between
// calls.
type ExpandedWriter struct {
	enc *json.Encoder
}

// NewExpandedWriter starts an expanded document on w, writing the header line.
func NewExpandedWriter(w io.Writer, h Header) (*ExpandedWriter, error) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // subjects routinely carry > < & — keep them readable
	if err := enc.Encode(expandedHeaderLine{Format: ExpandedFormat, Version: ExpandedVersion, Header: h}); err != nil {
		return nil, err
	}
	return &ExpandedWriter{enc: enc}, nil
}

// WriteEvent appends one event line.
func (ew *ExpandedWriter) WriteEvent(e *Event) error {
	ee := toExpandedEvent(e)
	return ew.enc.Encode(&ee)
}

// Close finishes the document. A non-nil footer is written as the final line; a nil
// footer writes nothing, marking the trace truncated (a capture cut short by the
// tracer's MaxSize/MaxTime). It does not close the underlying writer.
func (ew *ExpandedWriter) Close(f *Footer) error {
	if f == nil {
		return nil
	}
	return ew.enc.Encode(expandedFooterLine{Footer: *f})
}

// WriteExpanded serializes a fully decoded Trace to the expanded format. It is a thin
// convenience over ExpandedWriter for callers that already hold the whole trace in
// memory; the streaming converter uses ExpandedWriter directly.
func WriteExpanded(w io.Writer, t *Trace) error {
	ew, err := NewExpandedWriter(w, t.Header)
	if err != nil {
		return err
	}
	for _, e := range t.Events {
		if err := ew.WriteEvent(e); err != nil {
			return err
		}
	}
	return ew.Close(t.Footer)
}

// footerKey is the JSON key that distinguishes a footer line from an event line. A
// payload is base64 and every other event field is a JSON string, so the exact bytes
// `"footer":` can never occur in an event line (an embedded quote would be escaped).
var footerKey = []byte(`"footer":`)

// ExpandedScanner streams the events of an expanded document one at a time, so a
// single-pass check can run over a huge trace in bounded memory. The header is read
// eagerly by ScanExpanded; the footer (and therefore Truncated) becomes known only
// once Events has been iterated to completion.
type ExpandedScanner struct {
	path   string
	f      *os.File
	r      *bufio.Reader
	header Header
	footer *Footer
}

// ScanExpanded opens an expanded document for streaming reads, reading and validating
// the header line up front. The caller must Close it.
func ScanExpanded(path string) (*ExpandedScanner, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("trace %s: %w", path, err)
	}
	s := &ExpandedScanner{path: path, f: f, r: bufio.NewReader(f)}
	if err := s.readHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// Header returns the trace header, available immediately after ScanExpanded.
func (s *ExpandedScanner) Header() Header { return s.header }

// Footer returns the completion record, or nil if the trace was truncated. It is only
// meaningful once Events has been iterated to completion.
func (s *ExpandedScanner) Footer() *Footer { return s.footer }

// Truncated reports whether the trace ended without a footer. Meaningful only once
// Events has been fully consumed.
func (s *ExpandedScanner) Truncated() bool { return s.footer == nil }

// Close releases the underlying file.
func (s *ExpandedScanner) Close() error { return s.f.Close() }

// readHeader reads and validates the first line.
func (s *ExpandedScanner) readHeader() error {
	line, eof, err := s.nextLine()
	if err != nil {
		return fmt.Errorf("trace %s: %w", s.path, err)
	}
	if eof {
		return fmt.Errorf("trace %s: empty file", s.path)
	}
	var h expandedHeaderLine
	if err := json.Unmarshal(line, &h); err != nil {
		return fmt.Errorf("trace %s: %w", s.path, err)
	}
	if h.Format != ExpandedFormat {
		return fmt.Errorf("trace %s: not a %s file (format %q)", s.path, ExpandedFormat, h.Format)
	}
	if h.Version != ExpandedVersion {
		return fmt.Errorf("trace %s: unsupported expanded version %d (want %d)", s.path, h.Version, ExpandedVersion)
	}
	s.header = h.Header
	return nil
}

// Events streams the event lines. Iteration stops at the footer line (after which
// Footer/Truncated are populated) or at end of file. A read or decode error is yielded
// once, with a nil event, and ends iteration.
func (s *ExpandedScanner) Events() iter.Seq2[*Event, error] {
	return func(yield func(*Event, error) bool) {
		for {
			line, eof, err := s.nextLine()
			if err != nil {
				yield(nil, fmt.Errorf("trace %s: %w", s.path, err))
				return
			}
			if eof {
				return
			}
			if bytes.Contains(line, footerKey) {
				var ff expandedFooterLine
				if err := json.Unmarshal(line, &ff); err != nil {
					yield(nil, fmt.Errorf("trace %s: %w", s.path, err))
					return
				}
				s.footer = &ff.Footer
				return
			}
			var ee expandedEvent
			if err := json.Unmarshal(line, &ee); err != nil {
				yield(nil, fmt.Errorf("trace %s: %w", s.path, err))
				return
			}
			if !yield(ee.toEvent(), nil) {
				return
			}
		}
	}
}

// nextLine returns the next non-empty, trimmed line, or eof=true at end of file. A
// trace entry's base64 payload can exceed bufio.Scanner's 64KB token cap, so the
// underlying bufio.Reader grows to whatever a line needs.
func (s *ExpandedScanner) nextLine() (line []byte, eof bool, err error) {
	for {
		raw, rerr := s.r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
			return trimmed, false, nil
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil, true, nil
			}
			return nil, false, rerr
		}
	}
}

// LoadExpanded reads an expanded-format trace into the same *Trace that the capture
// side yields, so every matcher works identically on either source. It streams the
// document internally (one frame resident at a time) and materializes the slice only
// because the matcher API queries a trace repeatedly; single-pass callers can use
// ScanExpanded to avoid materializing at all.
func LoadExpanded(path string) (*Trace, error) {
	s, err := ScanExpanded(path)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var events []*Event
	for e, err := range s.Events() {
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}

	return &Trace{
		Header: s.Header(),
		Events: events,
		Footer: s.Footer(),
		Path:   path,
	}, nil
}
