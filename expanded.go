package traceassert

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// The expanded format is a fully pre-parsed trace: the event stream that the
// capture/parsing side produces from an ADR-2 trace, serialized as a single JSON
// document. Reading it back requires nothing but encoding/json — no protocol parser
// — which is what lets the assertion side of traceassert stand on its own, away from
// the proxy and the protocol model that produced the capture.
const (
	// ExpandedFormat tags the document so a stray JSON file can't be mistaken for one.
	ExpandedFormat = "traceassert-expanded"
	// ExpandedVersion is bumped on any breaking change to the on-disk shape.
	ExpandedVersion = 1
)

// expandedFile is the on-disk JSON document. Header/Footer carry the ADR-2 JSON
// shapes (plain data with stable json tags); the events are the normalized, decoded
// frames.
type expandedFile struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Header  Header          `json:"header"`
	Footer  *Footer         `json:"footer,omitempty"`
	Events  []expandedEvent `json:"events"`
}

// expandedEvent mirrors Event for serialization. Dir is written as its string form
// ("to_server"/"from_server") rather than the raw enum, and Payload rides along as
// base64 (encoding/json's default for []byte).
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

// WriteExpanded serializes a decoded Trace to the expanded JSON format.
func WriteExpanded(w io.Writer, t *Trace) error {
	ef := expandedFile{
		Format:  ExpandedFormat,
		Version: ExpandedVersion,
		Header:  t.Header,
		Footer:  t.Footer,
		Events:  make([]expandedEvent, len(t.Events)),
	}
	for i, e := range t.Events {
		ef.Events[i] = expandedEvent{
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

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(&ef)
}

// LoadExpanded reads an expanded-format trace into the same *Trace that Load yields
// from an ADR-2 capture, so every matcher works identically on either source.
func LoadExpanded(path string) (*Trace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trace %s: %w", path, err)
	}

	var ef expandedFile
	if err := json.Unmarshal(data, &ef); err != nil {
		return nil, fmt.Errorf("trace %s: %w", path, err)
	}
	if ef.Format != ExpandedFormat {
		return nil, fmt.Errorf("trace %s: not a %s file (format %q)", path, ExpandedFormat, ef.Format)
	}
	if ef.Version != ExpandedVersion {
		return nil, fmt.Errorf("trace %s: unsupported expanded version %d (want %d)", path, ef.Version, ExpandedVersion)
	}

	events := make([]*Event, len(ef.Events))
	for i := range ef.Events {
		ee := &ef.Events[i]
		dir := ToServer
		if ee.Dir == FromServer.String() {
			dir = FromServer
		}
		events[i] = &Event{
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

	return &Trace{
		Header: ef.Header,
		Events: events,
		Footer: ef.Footer,
		Path:   path,
	}, nil
}
