// Package tracegen is a test-only builder for trace fixtures. It accumulates a
// sequence of structured frames and can render them either as an ADR-2 trace (base64
// wire frames — to exercise the protocol parser) or directly as the traceassert
// "expanded" format (pre-parsed JSON — to exercise the assertion side without a
// parser). It deliberately depends on nothing but the standard library so it can be
// shared by both the assertion package and the capture package without import cycles.
package tracegen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	frameTS  = "2026-01-01T00:00:00Z"
	footerTS = "2026-01-01T00:00:01Z"
	footerNs = 1_000_000_000

	// These mirror traceassert.ExpandedFormat / ExpandedVersion. They are duplicated
	// as literals (rather than imported) so tracegen stays free of any traceassert
	// import — keeping the assertion package's own (internal) tests cycle-free.
	expandedFormat  = "traceassert-expanded"
	expandedVersion = 1
)

// frame is one captured protocol frame in structured form. raw is the NATS wire
// encoding (for ADR-2 output); the remaining fields are the decoded view (for
// expanded output).
type frame struct {
	dir     string // "backend" = to server, "client" = from server
	verb    string
	subject string
	reply   string
	sid     string
	payload []byte
	raw     []byte
}

// Builder accumulates frames for one connection.
type Builder struct {
	protocol string
	frames   []frame
}

// New starts a trace with the given connection protocol ("client" or "leaf").
func New(protocol string) *Builder { return &Builder{protocol: protocol} }

func (b *Builder) add(f frame) *Builder {
	b.frames = append(b.frames, f)
	return b
}

// Info adds a server INFO (handshake preamble); payload is the JSON body.
func (b *Builder) Info(json string) *Builder {
	return b.add(frame{dir: "client", verb: "INFO", payload: []byte(json), raw: []byte("INFO " + json + "\r\n")})
}

// Err adds a server -ERR; payload is the error text.
func (b *Builder) Err(msg string) *Builder {
	return b.add(frame{dir: "client", verb: "-ERR", payload: []byte(msg), raw: []byte("-ERR '" + msg + "'\r\n")})
}

// Connect adds a client CONNECT; payload is the JSON body.
func (b *Builder) Connect(json string) *Builder {
	if json == "" {
		json = "{}"
	}
	return b.add(frame{dir: "backend", verb: "CONNECT", payload: []byte(json), raw: []byte("CONNECT " + json + "\r\n")})
}

// Sub adds a client SUB.
func (b *Builder) Sub(subject, sid string) *Builder {
	return b.add(frame{dir: "backend", verb: "SUB", subject: subject, sid: sid,
		raw: fmt.Appendf(nil, "SUB %s %s\r\n", subject, sid)})
}

// Pub adds a client PUB (reply may be empty).
func (b *Builder) Pub(subject, reply string, payload []byte) *Builder {
	var sb strings.Builder
	sb.WriteString("PUB ")
	sb.WriteString(subject)
	if reply != "" {
		sb.WriteByte(' ')
		sb.WriteString(reply)
	}
	fmt.Fprintf(&sb, " %d\r\n", len(payload))
	sb.Write(payload)
	sb.WriteString("\r\n")
	return b.add(frame{dir: "backend", verb: "PUB", subject: subject, reply: reply, payload: payload, raw: []byte(sb.String())})
}

// Msg adds a server MSG delivered to subject on sid (reply may be empty).
func (b *Builder) Msg(subject, sid, reply string, payload []byte) *Builder {
	var sb strings.Builder
	sb.WriteString("MSG ")
	sb.WriteString(subject)
	sb.WriteByte(' ')
	sb.WriteString(sid)
	if reply != "" {
		sb.WriteByte(' ')
		sb.WriteString(reply)
	}
	fmt.Fprintf(&sb, " %d\r\n", len(payload))
	sb.Write(payload)
	sb.WriteString("\r\n")
	return b.add(frame{dir: "client", verb: "MSG", subject: subject, sid: sid, reply: reply, payload: payload, raw: []byte(sb.String())})
}

// MsgString is Msg with a string payload.
func (b *Builder) MsgString(subject, sid, payload string) *Builder {
	return b.Msg(subject, sid, "", []byte(payload))
}

// --- ADR-2 rendering (base64 wire frames) ---------------------------------

// Bytes renders the trace as an ADR-2 file. If footer is true a completion line is
// appended; omit it to simulate a truncated trace.
func (b *Builder) Bytes(footer bool) []byte {
	lines := []string{fmt.Sprintf(
		`{"version":1,"device":"tracegen","ts":"%s","cuuid":"test","port":"%s","protocol":"%s","profile":{"uuid":"test"},"file":"test"}`,
		frameTS, b.protocol, b.protocol)}
	for _, f := range b.frames {
		lines = append(lines, fmt.Sprintf(
			`{"ts":"%s","dir":"%s","msg":"%s","dat":"%s"}`,
			frameTS, f.dir, f.verb, base64.StdEncoding.EncodeToString(f.raw)))
	}
	if footer {
		lines = append(lines, fmt.Sprintf(`{"ts":"%s","duration":%d}`, footerTS, footerNs))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// WriteFile renders the ADR-2 trace (with footer) to path.
func (b *Builder) WriteFile(path string) error { return os.WriteFile(path, b.Bytes(true), 0o644) }

// WriteFileTruncated renders the ADR-2 trace WITHOUT a footer to path.
func (b *Builder) WriteFileTruncated(path string) error {
	return os.WriteFile(path, b.Bytes(false), 0o644)
}

// --- expanded rendering (pre-parsed JSON) ---------------------------------

type expDoc struct {
	Format  string     `json:"format"`
	Version int        `json:"version"`
	Header  expHeader  `json:"header"`
	Footer  *expFooter `json:"footer,omitempty"`
	Events  []expEvent `json:"events"`
}

type expHeader struct {
	Version  int    `json:"version"`
	Device   string `json:"device"`
	Protocol string `json:"protocol"`
}

type expFooter struct {
	Timestamp string `json:"ts"`
	Duration  int64  `json:"duration"`
}

type expEvent struct {
	Line    int    `json:"line"`
	At      string `json:"at"`
	Dir     string `json:"dir"`
	Verb    string `json:"verb,omitempty"`
	Subject string `json:"subject,omitempty"`
	Reply   string `json:"reply,omitempty"`
	SID     string `json:"sid,omitempty"`
	Payload []byte `json:"payload,omitempty"`
}

// ExpandedBytes renders the trace as a traceassert expanded document. If footer is
// true the completion record is included; omit it to simulate a truncated trace.
func (b *Builder) ExpandedBytes(footer bool) []byte {
	doc := expDoc{
		Format:  expandedFormat,
		Version: expandedVersion,
		Header:  expHeader{Version: 1, Device: "tracegen", Protocol: b.protocol},
	}
	if footer {
		doc.Footer = &expFooter{Timestamp: footerTS, Duration: footerNs}
	}
	for i, f := range b.frames {
		dir := "to_server"
		if f.dir == "client" {
			dir = "from_server"
		}
		doc.Events = append(doc.Events, expEvent{
			Line:    i + 2, // header is line 1
			At:      frameTS,
			Dir:     dir,
			Verb:    f.verb,
			Subject: f.subject,
			Reply:   f.reply,
			SID:     f.sid,
			Payload: f.payload,
		})
	}
	out, _ := json.MarshalIndent(&doc, "", "  ")
	return append(out, '\n')
}

// WriteExpandedFile renders the expanded trace (with footer) to path.
func (b *Builder) WriteExpandedFile(path string) error {
	return os.WriteFile(path, b.ExpandedBytes(true), 0o644)
}

// WriteExpandedFileTruncated renders the expanded trace WITHOUT a footer to path.
func (b *Builder) WriteExpandedFileTruncated(path string) error {
	return os.WriteFile(path, b.ExpandedBytes(false), 0o644)
}
