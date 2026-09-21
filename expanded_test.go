package traceassert

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/synadia-labs/traceassert/tracegen"
)

// TestExpanded_RoundTrip loads an expanded fixture, re-serializes it with
// WriteExpanded, reloads it, and checks the event stream and footer survive intact.
func TestExpanded_RoundTrip(t *testing.T) {
	first := loadExpanded(t, fastIngestBuilder())
	if first.Truncated() {
		t.Fatal("fixture should have a footer (not truncated)")
	}

	path := filepath.Join(t.TempDir(), "round-trip.expanded.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteExpanded(f, first); err != nil {
		t.Fatalf("WriteExpanded: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}

	if second.Truncated() {
		t.Error("re-loaded trace should not be truncated")
	}
	if len(second.Events) != len(first.Events) {
		t.Fatalf("event count = %d, want %d", len(second.Events), len(first.Events))
	}
	for i := range first.Events {
		a, b := first.Events[i], second.Events[i]
		if a.Line != b.Line || a.Dir != b.Dir || a.Verb != b.Verb ||
			a.Subject != b.Subject || a.Reply != b.Reply || a.SID != b.SID {
			t.Errorf("event %d mismatch:\n got %+v\nwant %+v", i, b, a)
		}
		if !bytes.Equal(a.Payload, b.Payload) {
			t.Errorf("event %d payload = %q, want %q", i, b.Payload, a.Payload)
		}
		if !a.At.Equal(b.At) {
			t.Errorf("event %d timestamp = %s, want %s", i, b.At, a.At)
		}
	}
}

// TestExpanded_PreservesHeadersAndWireBytes checks that message headers and the
// recorded wire size survive both the initial expanded render and a WriteExpanded
// round trip — the two places a serialization gap would silently drop them.
func TestExpanded_PreservesHeadersAndWireBytes(t *testing.T) {
	header := map[string][]string{
		"Nats-Msg-Id": {"abc"},
		"X-Custom":    {"v1", "v2"},
	}

	b := tracegen.New("client")
	b.Info(`{"server_id":"test"}`)
	b.Connect("{}")
	b.HPub("ORDERS", "_INBOX.reply", header, []byte("hello"))

	first := loadExpanded(t, b)

	hpub, ok := first.First(func(e *Event) bool { return e.Verb == "HPUB" })
	if !ok {
		t.Fatal("no HPUB event in fixture")
	}
	if !reflect.DeepEqual(hpub.Header, header) {
		t.Fatalf("HPUB header = %v, want %v", hpub.Header, header)
	}
	if hpub.WireBytes <= 0 {
		t.Fatalf("HPUB WireBytes = %d, want > 0", hpub.WireBytes)
	}

	path := filepath.Join(t.TempDir(), "round-trip.expanded.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteExpanded(f, first); err != nil {
		t.Fatalf("WriteExpanded: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}
	reloaded, ok := second.First(func(e *Event) bool { return e.Verb == "HPUB" })
	if !ok {
		t.Fatal("no HPUB event after round trip")
	}
	if !reflect.DeepEqual(reloaded.Header, header) {
		t.Errorf("round-tripped header = %v, want %v", reloaded.Header, header)
	}
	if reloaded.WireBytes != hpub.WireBytes {
		t.Errorf("round-tripped WireBytes = %d, want %d", reloaded.WireBytes, hpub.WireBytes)
	}
}

// TestExpanded_RoundTripShaped checks that a shaped event survives the initial expanded
// render, a WriteExpanded round trip and the streaming scanner, and that the events the
// proxy left alone stay nil.
func TestExpanded_RoundTripShaped(t *testing.T) {
	want := &Shaped{Rule: "drop-ack-30", Action: "drop"}

	b := tracegen.New("client")
	b.Info(`{"server_id":"test"}`)
	b.Connect("{}")
	b.Sub("_INBOX.batch1.>", "1")
	b.Pub("ORDERS", "_INBOX.batch1.10.ok.1.0.$FI", []byte("m1"))
	b.MsgString("_INBOX.batch1.ack", "1", `{"type":"ack","seq":1,"msgs":15}`).Shaped(want.Rule, want.Action)

	first := loadExpanded(t, b)
	checkShaped := func(stage string, tr *Trace) {
		t.Helper()
		for _, e := range tr.Events {
			if e.Verb == "MSG" {
				if !reflect.DeepEqual(e.Shaped, want) {
					t.Errorf("%s: %s Shaped = %+v, want %+v", stage, e, e.Shaped, want)
				}
				continue
			}
			if e.Shaped != nil {
				t.Errorf("%s: %s Shaped = %+v, want nil", stage, e, e.Shaped)
			}
		}
	}
	checkShaped("render", first)

	path := filepath.Join(t.TempDir(), "shaped.expanded.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteExpanded(f, first); err != nil {
		t.Fatalf("WriteExpanded: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}
	checkShaped("writer", second)

	s, err := ScanExpanded(path)
	if err != nil {
		t.Fatalf("ScanExpanded: %v", err)
	}
	defer s.Close()
	streamed := &Trace{}
	for e, err := range s.Events() {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		streamed.Events = append(streamed.Events, e)
	}
	if len(streamed.Events) != len(first.Events) {
		t.Fatalf("streamed %d events, want %d", len(streamed.Events), len(first.Events))
	}
	checkShaped("scanner", streamed)
}

// TestLoadExpanded_WithoutShapedKey checks that a v2 document written before the
// "shaped" key existed loads with a nil Shaped on every event.
func TestLoadExpanded_WithoutShapedKey(t *testing.T) {
	doc := `{"format":"traceassert-expanded","version":2,"header":{"version":1,"device":"test","protocol":"client"}}
{"line":2,"at":"2026-01-01T00:00:00Z","dir":"from_server","verb":"INFO","payload":"e30="}
{"line":3,"at":"2026-01-01T00:00:00Z","dir":"to_server","verb":"PUB","subject":"ORDERS","payload":"bTE=","bytes":16}
{"footer":{"ts":"2026-01-01T00:00:01Z","duration":1000000000}}
`
	path := filepath.Join(t.TempDir(), "unshaped.expanded.json")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}
	if len(tr.Events) != 2 {
		t.Fatalf("loaded %d events, want 2", len(tr.Events))
	}
	for _, e := range tr.Events {
		if e.Shaped != nil {
			t.Errorf("%s Shaped = %+v, want nil", e, e.Shaped)
		}
	}
}

// TestExpanded_HeaderTimestamp checks that tracegen's Timestamp reaches the loaded
// trace header.
func TestExpanded_HeaderTimestamp(t *testing.T) {
	ts := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	tr := loadExpanded(t, fastIngestBuilder().Timestamp(ts))
	if !tr.Header.Timestamp.Equal(ts) {
		t.Errorf("header timestamp = %s, want %s", tr.Header.Timestamp, ts)
	}
}

// TestLoadExpanded_RejectsForeignJSON ensures an unrelated JSON file is not mistaken
// for an expanded trace.
func TestLoadExpanded_RejectsForeignJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(path, []byte(`{"hello":"world"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExpanded(path); err == nil {
		t.Fatal("expected error loading non-expanded JSON, got nil")
	}
}

// TestExpanded_Truncated checks that a footer-less expanded document (a capture cut
// short) loads as Truncated — the no-footer path of the JSON Lines reader.
func TestExpanded_Truncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.expanded.json")
	if err := fastIngestBuilder().WriteExpandedFileTruncated(path); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	tr, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}
	if !tr.Truncated() {
		t.Error("expected Truncated()=true for a footer-less expanded trace")
	}
	if len(tr.Events) == 0 {
		t.Error("expected events to load even without a footer")
	}
}

// TestScanExpanded_StreamsEvents checks the single-pass streaming reader: it yields the
// same events LoadExpanded materializes, and reports the footer once drained.
func TestScanExpanded_StreamsEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.expanded.json")
	if err := fastIngestBuilder().WriteExpandedFile(path); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	want, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}

	s, err := ScanExpanded(path)
	if err != nil {
		t.Fatalf("ScanExpanded: %v", err)
	}
	defer s.Close()

	got := 0
	for e, err := range s.Events() {
		if err != nil {
			t.Fatalf("stream event %d: %v", got, err)
		}
		if got >= len(want.Events) {
			t.Fatalf("streamed more events than the %d LoadExpanded returned", len(want.Events))
		}
		if w := want.Events[got]; e.Line != w.Line || e.Verb != w.Verb || e.Subject != w.Subject {
			t.Errorf("event %d mismatch: got %s, want %s", got, e, w)
		}
		got++
	}
	if got != len(want.Events) {
		t.Fatalf("streamed %d events, want %d", got, len(want.Events))
	}
	if s.Truncated() || s.Footer() == nil {
		t.Error("expected a footer to be populated after draining a complete trace")
	}
}
