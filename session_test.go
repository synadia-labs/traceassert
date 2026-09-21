package traceassert

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/synadia-labs/traceassert/tracegen"
)

var (
	sessionT0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sessionT1 = sessionT0.Add(time.Second)
)

// firstConnection is a connection that sent a request and lost the connection before
// the reply arrived; secondConnection is the reconnect, on which that reply subject is
// delivered and a fresh request is answered. Their header timestamps are the reverse
// of their filenames in the tests below, so the timestamp order is what is pinned.
func firstConnection() *tracegen.Builder {
	b := tracegen.New("client").Timestamp(sessionT0)
	b.Info(`{"server_id":"a"}`).Connect("{}")
	b.Sub("_INBOX.one.*", "1")
	b.Pub("svc.echo", "_INBOX.one.r1", []byte("first"))
	return b
}

func secondConnection() *tracegen.Builder {
	b := tracegen.New("client").Timestamp(sessionT1)
	b.Info(`{"server_id":"b"}`).Connect("{}")
	b.Sub("_INBOX.one.*", "1")
	b.MsgString("_INBOX.one.r1", "1", "late reply to the first connection's request")
	b.Pub("svc.echo", "_INBOX.one.r2", []byte("second"))
	b.MsgString("_INBOX.one.r2", "1", "reply")
	return b
}

func writeSessionFixture(t *testing.T, dir, file string, b *tracegen.Builder) {
	t.Helper()
	err := b.WriteExpandedFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("write fixture %s: %v", file, err)
	}
}

func isRequest(e *Event) bool { return e.Verb == "PUB" && e.Reply != "" }

func TestNewSession(t *testing.T) {
	// filenames put the later connection first so the sort has work to do.
	dir := t.TempDir()
	writeSessionFixture(t, dir, "scn.expanded.json", secondConnection())
	writeSessionFixture(t, dir, "scn-1.expanded.json", firstConnection())

	second, err := LoadExpanded(filepath.Join(dir, "scn.expanded.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadExpanded(filepath.Join(dir, "scn-1.expanded.json"))
	if err != nil {
		t.Fatal(err)
	}

	s := NewSession(second, first)

	t.Run("orders traces by header timestamp", func(t *testing.T) {
		if len(s.Traces) != 2 {
			t.Fatalf("got %d traces, want 2", len(s.Traces))
		}
		if s.Traces[0] != first || s.Traces[1] != second {
			t.Errorf("traces are not in header-timestamp order: %v then %v",
				s.Traces[0].Header.Timestamp, s.Traces[1].Header.Timestamp)
		}
	})

	t.Run("sets Conn to the trace index on every event", func(t *testing.T) {
		for i, tr := range s.Traces {
			for _, e := range tr.Events {
				if e.Conn != i {
					t.Errorf("%s: Conn = %d, want %d", e, e.Conn, i)
				}
			}
		}
	})

	t.Run("Events concatenates in session order", func(t *testing.T) {
		evs := s.Events()
		want := len(first.Events) + len(second.Events)
		if len(evs) != want {
			t.Fatalf("got %d events, want %d", len(evs), want)
		}
		if evs[0] != first.Events[0] || evs[len(evs)-1] != second.Events[len(second.Events)-1] {
			t.Error("events are not the first trace's followed by the second's")
		}
	})

	t.Run("Select, First and Count span every trace", func(t *testing.T) {
		if n := s.Count(isRequest); n != 2 {
			t.Errorf("Count = %d, want 2", n)
		}
		reqs := s.Select(isRequest)
		if len(reqs) != 2 || reqs[0].Conn != 0 || reqs[1].Conn != 1 {
			t.Errorf("Select returned %v", reqs)
		}
		e, ok := s.First(func(e *Event) bool { return e.Conn == 1 && e.Verb == "MSG" })
		if !ok || string(e.Payload) != "late reply to the first connection's request" {
			t.Errorf("First on connection 1 returned %v, %v", e, ok)
		}
	})

	t.Run("pairs requests per trace", func(t *testing.T) {
		pairs := s.RequestReplies(isRequest)
		if len(pairs) != 2 {
			t.Fatalf("got %d pairs, want 2", len(pairs))
		}
		if pairs[0].Request.Conn != 0 || pairs[0].Response != nil {
			t.Errorf("the first connection's request must stay unanswered, got response %v", pairs[0].Response)
		}
		if pairs[1].Request.Conn != 1 || pairs[1].Response == nil || string(pairs[1].Response.Payload) != "reply" {
			t.Errorf("the second connection's request must pair with its own reply, got %v", pairs[1].Response)
		}

		// the same events merged into one trace would pair the dead request with the
		// late delivery, which is the fault a session exists to keep visible.
		merged := &Trace{Events: s.Events()}
		if merged.RequestReplies(isRequest)[0].Response == nil {
			t.Fatal("merged trace did not pair the late reply; the fixture no longer demonstrates the difference")
		}
	})
}

func TestStandaloneTraceConnIsZero(t *testing.T) {
	tr := loadExpanded(t, secondConnection())
	for _, e := range tr.Events {
		if e.Conn != 0 {
			t.Errorf("%s: Conn = %d on a trace loaded on its own", e, e.Conn)
		}
	}
}

func TestLoadSession(t *testing.T) {
	dir := t.TempDir()
	// the scenario under test, with filenames in the reverse of timestamp order
	writeSessionFixture(t, dir, "fast.expanded.json", secondConnection())
	writeSessionFixture(t, dir, "fast-1.expanded.json", firstConnection())
	// a sibling scenario sharing the prefix, which must not be swept in
	writeSessionFixture(t, dir, "fast-lostack.expanded.json", firstConnection())
	writeSessionFixture(t, dir, "fast-lostack-1.expanded.json", secondConnection())
	t.Setenv(TraceDirEnv, dir)

	t.Run("loads the scenario's captures in header-timestamp order", func(t *testing.T) {
		s, err := LoadSession("fast")
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Traces) != 2 {
			t.Fatalf("got %d traces, want 2", len(s.Traces))
		}
		if filepath.Base(s.Traces[0].Path) != "fast-1.expanded.json" || filepath.Base(s.Traces[1].Path) != "fast.expanded.json" {
			t.Errorf("traces in order %q, %q; want fast-1 then fast", s.Traces[0].Path, s.Traces[1].Path)
		}
		if s.Traces[0].Events[0].Conn != 0 || s.Traces[1].Events[0].Conn != 1 {
			t.Error("Conn not assigned in session order")
		}
	})

	t.Run("does not sweep in a sibling scenario", func(t *testing.T) {
		s, err := LoadSession("fast-lostack")
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Traces) != 2 {
			t.Fatalf("fast-lostack: got %d traces, want 2", len(s.Traces))
		}
		for _, tr := range s.Traces {
			if !strings.HasPrefix(filepath.Base(tr.Path), "fast-lostack") {
				t.Errorf("fast-lostack swept in %q", tr.Path)
			}
		}
	})

	t.Run("no capture is an error naming the directory and the names tried", func(t *testing.T) {
		_, err := LoadSession("absent")
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{dir, "absent.expanded.json", "absent-<n>.expanded.json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("a truncated capture is refused", func(t *testing.T) {
		writeSessionFixture(t, dir, "cut.expanded.json", firstConnection())
		err := secondConnection().WriteExpandedFileTruncated(filepath.Join(dir, "cut-1.expanded.json"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadSession("cut")
		if err == nil {
			t.Fatal("expected an error for a truncated capture")
		}
		if !strings.Contains(err.Error(), "truncated") {
			t.Errorf("error %q does not mention truncation", err)
		}
	})
}

func TestIsSessionCapture(t *testing.T) {
	cases := []struct {
		name, file string
		want       bool
	}{
		{"fast", "fast.expanded.json", true},
		{"fast", "fast-1.expanded.json", true},
		{"fast", "fast-12.expanded.json", true},
		{"fast", "fast-lostack.expanded.json", false},
		{"fast", "fast-lostack-1.expanded.json", false},
		{"fast", "fast-.expanded.json", false},
		{"fast", "fast-1a.expanded.json", false},
		{"fast", "fast1.expanded.json", false},
		{"fast", "fast.json", false},
		{"fast", "unfast.expanded.json", false},
		{"a[1", "a[1.expanded.json", true},
		{"a[1", "a[1-2.expanded.json", true},
	}
	for _, c := range cases {
		got := isSessionCapture(c.name, c.file)
		if got != c.want {
			t.Errorf("isSessionCapture(%q, %q) = %v, want %v", c.name, c.file, got, c.want)
		}
	}
}

func TestNewSessionTieBreakByIndex(t *testing.T) {
	// Equal header timestamps fall back to the <n> in the filename, so the bare name
	// comes first and 10 follows 2, which a byte compare of the paths would not give.
	mk := func(path string) *Trace {
		return &Trace{Path: path}
	}
	s := NewSession(
		mk("/t/fast-10.expanded.json"),
		mk("/t/fast-2.expanded.json"),
		mk("/t/fast-1.expanded.json"),
		mk("/t/fast.expanded.json"),
	)
	want := []string{
		"/t/fast.expanded.json",
		"/t/fast-1.expanded.json",
		"/t/fast-2.expanded.json",
		"/t/fast-10.expanded.json",
	}
	for i, tr := range s.Traces {
		if tr.Path != want[i] {
			t.Fatalf("trace %d is %q, want %q", i, tr.Path, want[i])
		}
	}
}
