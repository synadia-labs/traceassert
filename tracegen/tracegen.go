// Package tracegen is a test-only builder for trace fixtures. It accumulates a
// sequence of structured frames and can render them either as an ADR-2 trace (base64
// wire frames — to exercise the protocol parser) or directly as the traceassert
// "expanded" format (pre-parsed JSON — to exercise the assertion side without a
// parser). It deliberately depends on nothing but the standard library so it can be
// shared by both the assertion package and the capture package without import cycles.
package tracegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	expandedVersion = 2
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
	header  map[string][]string
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

// HPub adds a client HPUB carrying headers (reply may be empty).
func (b *Builder) HPub(subject, reply string, header map[string][]string, payload []byte) *Builder {
	hdr := headerBlock(header)
	var sb strings.Builder
	sb.WriteString("HPUB ")
	sb.WriteString(subject)
	if reply != "" {
		sb.WriteByte(' ')
		sb.WriteString(reply)
	}
	fmt.Fprintf(&sb, " %d %d\r\n", len(hdr), len(hdr)+len(payload))
	sb.WriteString(hdr)
	sb.Write(payload)
	sb.WriteString("\r\n")
	return b.add(frame{dir: "backend", verb: "HPUB", subject: subject, reply: reply, header: header, payload: payload, raw: []byte(sb.String())})
}

// HMsg adds a server HMSG carrying headers, delivered to subject on sid (reply may be empty).
func (b *Builder) HMsg(subject, sid, reply string, header map[string][]string, payload []byte) *Builder {
	hdr := headerBlock(header)
	var sb strings.Builder
	sb.WriteString("HMSG ")
	sb.WriteString(subject)
	sb.WriteByte(' ')
	sb.WriteString(sid)
	if reply != "" {
		sb.WriteByte(' ')
		sb.WriteString(reply)
	}
	fmt.Fprintf(&sb, " %d %d\r\n", len(hdr), len(hdr)+len(payload))
	sb.WriteString(hdr)
	sb.Write(payload)
	sb.WriteString("\r\n")
	return b.add(frame{dir: "client", verb: "HMSG", subject: subject, sid: sid, reply: reply, header: header, payload: payload, raw: []byte(sb.String())})
}

// headerBlock renders headers as a NATS/1.0 header block, with keys sorted so the
// wire frame is stable, including the trailing blank line that separates the headers
// from the body.
func headerBlock(header map[string][]string) string {
	var sb strings.Builder
	sb.WriteString("NATS/1.0\r\n")
	keys := make([]string, 0, len(header))
	for k := range header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range header[k] {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\r\n")
		}
	}
	sb.WriteString("\r\n")
	return sb.String()
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

// expHeaderLine, expEvent and expFooterLine are the three JSON Lines record shapes of
// the expanded v2 format: the header line, one event line per frame, and the footer
// line. They mirror traceassert's on-disk shape so a tracegen fixture loads via
// traceassert.LoadExpanded.
type expHeaderLine struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Header  expHeader `json:"header"`
}

type expFooterLine struct {
	Footer expFooter `json:"footer"`
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
	Line    int                 `json:"line"`
	At      string              `json:"at"`
	Dir     string              `json:"dir"`
	Verb    string              `json:"verb,omitempty"`
	Subject string              `json:"subject,omitempty"`
	Reply   string              `json:"reply,omitempty"`
	SID     string              `json:"sid,omitempty"`
	Header  map[string][]string `json:"header,omitempty"`
	Payload []byte              `json:"payload,omitempty"`
	Bytes   int                 `json:"bytes,omitempty"`
}

// ExpandedBytes renders the trace as a traceassert expanded document (JSON Lines: a
// header line, one line per event, then a footer line). If footer is true the
// completion record is included; omit it to simulate a truncated trace.
func (b *Builder) ExpandedBytes(footer bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	// Encode writes each value on its own line (with a trailing newline), so the buffer
	// ends up as one JSON record per line — the expanded v2 shape. Writes to a
	// bytes.Buffer never fail, so the errors are deliberately ignored.
	_ = enc.Encode(expHeaderLine{
		Format:  expandedFormat,
		Version: expandedVersion,
		Header:  expHeader{Version: 1, Device: "tracegen", Protocol: b.protocol},
	})
	for i, f := range b.frames {
		dir := "to_server"
		if f.dir == "client" {
			dir = "from_server"
		}
		_ = enc.Encode(expEvent{
			Line:    i + 2, // header is line 1
			At:      frameTS,
			Dir:     dir,
			Verb:    f.verb,
			Subject: f.subject,
			Reply:   f.reply,
			SID:     f.sid,
			Header:  f.header,
			Payload: f.payload,
			Bytes:   len(f.raw),
		})
	}
	if footer {
		_ = enc.Encode(expFooterLine{Footer: expFooter{Timestamp: footerTS, Duration: footerNs}})
	}
	return buf.Bytes()
}

// WriteExpandedFile renders the expanded trace (with footer) to path.
func (b *Builder) WriteExpandedFile(path string) error {
	return os.WriteFile(path, b.ExpandedBytes(true), 0o644)
}

// WriteExpandedFileTruncated renders the expanded trace WITHOUT a footer to path.
func (b *Builder) WriteExpandedFileTruncated(path string) error {
	return os.WriteFile(path, b.ExpandedBytes(false), 0o644)
}
