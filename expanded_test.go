package traceassert

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
