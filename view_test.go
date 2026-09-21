package traceassert

import (
	"testing"

	"github.com/synadia-labs/traceassert/tracegen"
)

// shapedConnection is one connection a shaping proxy acted on: the reply to the
// first request is dropped, the second request is dropped before the server saw it,
// the third request is stalled and answered. Line 6 is the dropped reply and line 7
// the dropped request.
func shapedConnection() *tracegen.Builder {
	b := tracegen.New("client").Timestamp(sessionT0)
	b.Info(`{"server_id":"a"}`).Connect("{}")
	b.Sub("_INBOX.s.*", "1")
	b.Pub("svc.echo", "_INBOX.s.r1", []byte("lost reply"))
	b.MsgString("_INBOX.s.r1", "1", "dropped ack").Shaped("drop-ack-30", "drop")
	b.Pub("svc.echo", "_INBOX.s.r2", []byte("dropped request")).Shaped("drop-req", "drop")
	b.Pub("svc.echo", "_INBOX.s.r3", []byte("stalled")).Shaped("stall-req", "stall")
	b.MsgString("_INBOX.s.r3", "1", "reply")
	return b
}

const (
	droppedReplyLine   = 6
	droppedRequestLine = 7
)

func TestEventDropped(t *testing.T) {
	cases := []struct {
		name   string
		shaped *Shaped
		want   bool
	}{
		{"unshaped", nil, false},
		{"drop", &Shaped{Rule: "r", Action: "drop"}, true},
		{"disconnect", &Shaped{Rule: "r", Action: "disconnect"}, true},
		{"stall", &Shaped{Rule: "r", Action: "stall"}, false},
		{"throttle", &Shaped{Rule: "r", Action: "throttle"}, false},
	}
	for _, c := range cases {
		e := &Event{Shaped: c.shaped}
		if got := e.Dropped(); got != c.want {
			t.Errorf("%s: Dropped() = %v, want %v", c.name, got, c.want)
		}
	}
}

// lines returns the Line of every event, for comparing a view against the capture.
func lines(evs []*Event) []int {
	out := make([]int, len(evs))
	for i, e := range evs {
		out[i] = e.Line
	}
	return out
}

func containsLine(evs []*Event, line int) bool {
	for _, e := range evs {
		if e.Line == line {
			return true
		}
	}
	return false
}

// checkView verifies what every view shares with its capture: the header, footer
// and path, the event pointers themselves, and the Conn on each event.
func checkView(t *testing.T, full, view *Trace) {
	t.Helper()
	if view == full {
		t.Fatal("view is the capture itself")
	}
	if view.Header != full.Header || view.Footer != full.Footer || view.Path != full.Path {
		t.Error("view does not share the capture's header, footer and path")
	}
	byLine := map[int]*Event{}
	for _, e := range full.Events {
		byLine[e.Line] = e
	}
	for _, e := range view.Events {
		if byLine[e.Line] != e {
			t.Errorf("line %d: the view holds a copy, not the capture's event", e.Line)
		}
		if e.Conn != full.Events[0].Conn {
			t.Errorf("line %d: Conn = %d, want %d", e.Line, e.Conn, full.Events[0].Conn)
		}
	}
}

func TestTraceViews(t *testing.T) {
	full := loadExpanded(t, shapedConnection())
	for _, e := range full.Events {
		e.Conn = 3
	}

	t.Run("ClientView leaves out the dropped from_server frame", func(t *testing.T) {
		v := full.ClientView()
		checkView(t, full, v)
		if len(v.Events) != len(full.Events)-1 {
			t.Fatalf("client view has lines %v, want the capture's %v less line %d",
				lines(v.Events), lines(full.Events), droppedReplyLine)
		}
		if containsLine(v.Events, droppedReplyLine) {
			t.Error("the dropped reply is in the client view")
		}
		if !containsLine(v.Events, droppedRequestLine) {
			t.Error("the dropped request is missing from the client view; the client did send it")
		}
	})

	t.Run("ServerView leaves out the dropped to_server frame", func(t *testing.T) {
		v := full.ServerView()
		checkView(t, full, v)
		if len(v.Events) != len(full.Events)-1 {
			t.Fatalf("server view has lines %v, want the capture's %v less line %d",
				lines(v.Events), lines(full.Events), droppedRequestLine)
		}
		if containsLine(v.Events, droppedRequestLine) {
			t.Error("the dropped request is in the server view")
		}
		if !containsLine(v.Events, droppedReplyLine) {
			t.Error("the dropped reply is missing from the server view; the server did send it")
		}
	})

	t.Run("a view leaves the capture whole", func(t *testing.T) {
		full.ClientView()
		full.ServerView()
		if !containsLine(full.Events, droppedReplyLine) || !containsLine(full.Events, droppedRequestLine) {
			t.Error("taking a view removed events from the capture")
		}
	})

	t.Run("a stalled frame is in both views", func(t *testing.T) {
		stalled := func(e *Event) bool { return e.Shaped != nil && e.Shaped.Action == "stall" }
		if full.ClientView().Count(stalled) != 1 || full.ServerView().Count(stalled) != 1 {
			t.Error("a stalled frame was delivered and belongs in both views")
		}
	})
}

func TestSessionViews(t *testing.T) {
	first := loadExpanded(t, shapedConnection())
	second := loadExpanded(t, secondConnection())
	s := NewSession(first, second)

	t.Run("ClientView is the per-trace client views in session order", func(t *testing.T) {
		v := s.ClientView()
		if len(v.Traces) != 2 {
			t.Fatalf("got %d traces, want 2", len(v.Traces))
		}
		checkView(t, first, v.Traces[0])
		checkView(t, second, v.Traces[1])
		if containsLine(v.Traces[0].Events, droppedReplyLine) {
			t.Error("the dropped reply is in the client view of connection 0")
		}
		if len(v.Traces[1].Events) != len(second.Events) {
			t.Error("an unshaped connection's client view is not the whole connection")
		}
		if v.Traces[0].Events[0].Conn != 0 || v.Traces[1].Events[0].Conn != 1 {
			t.Error("Conn changed on the view's events")
		}
		if s.Traces[0] != first || len(first.Events) != 8 {
			t.Error("taking a view changed the session")
		}
	})

	t.Run("ServerView is the per-trace server views in session order", func(t *testing.T) {
		v := s.ServerView()
		if len(v.Traces) != 2 {
			t.Fatalf("got %d traces, want 2", len(v.Traces))
		}
		checkView(t, first, v.Traces[0])
		checkView(t, second, v.Traces[1])
		if containsLine(v.Traces[0].Events, droppedRequestLine) {
			t.Error("the dropped request is in the server view of connection 0")
		}
		if v.Traces[0].Events[0].Conn != 0 || v.Traces[1].Events[0].Conn != 1 {
			t.Error("Conn changed on the view's events")
		}
	})
}
