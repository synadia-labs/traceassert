package traceassert

import (
	"testing"

	"github.com/synadia-labs/traceassert/subject"
	"github.com/synadia-labs/traceassert/tracegen"
)

var fiReply = subject.MustParse("{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

func TestGroupBy_ByCapture_TwoInterleavedBatches(t *testing.T) {
	b := tracegen.New("client")
	b.Info(`{}`).Connect("{}")
	b.Pub("ORDERS", "_INBOX.b1.10.ok.1.0.$FI", []byte("a")) // batch b1 seq1
	b.Pub("ORDERS", "_INBOX.b2.10.ok.1.0.$FI", []byte("x")) // batch b2 seq1 (interleaved)
	b.Pub("ORDERS", "_INBOX.b1.10.ok.2.1.$FI", []byte("b")) // batch b1 seq2

	tr := loadExpanded(t, b)

	convos := tr.GroupBy(ByCapture(fiReply, "uuid"))
	if len(convos) != 2 {
		t.Fatalf("conversations = %d, want 2", len(convos))
	}
	// first-seen order preserved
	if convos[0].Key != "b1" || convos[1].Key != "b2" {
		t.Fatalf("keys = %q,%q want b1,b2", convos[0].Key, convos[1].Key)
	}

	b1, ok := convos.Get("b1")
	if !ok || len(b1.ToServer()) != 2 {
		t.Fatalf("b1 publishes = %v, want 2", b1)
	}
	b2, _ := convos.Get("b2")
	if len(b2.ToServer()) != 1 {
		t.Fatalf("b2 publishes = %d, want 1", len(b2.ToServer()))
	}

	// .One() must reject a multi-conversation set.
	if _, ok := convos.One(); ok {
		t.Errorf("One() should be false for 2 conversations")
	}
	// but be true for a single-batch grouping.
	single := loadExpanded(t, fastIngestBuilder()).
		GroupBy(ByCapture(fiReply, "uuid"))
	if c, ok := single.One(); !ok || c.Key != "batch1" {
		t.Errorf("One() = %v,%v want batch1,true", c, ok)
	}
}

func TestRequestReplies_Correlation(t *testing.T) {
	streamCreate := subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

	b := tracegen.New("client")
	b.Info(`{}`).Connect("{}")
	// request 1: gets a response on its reply inbox
	b.Pub("$JS.API.STREAM.CREATE.ORDERS", "_INBOX.r.1", []byte(`{"name":"ORDERS"}`))
	b.MsgString("_INBOX.r.1", "9", `{"type":"io.nats.jetstream.api.v1.stream_create_response"}`)
	// request 2: never answered
	b.Pub("$JS.API.STREAM.CREATE.BILLING", "_INBOX.r.2", []byte(`{"name":"BILLING"}`))

	tr := loadExpanded(t, b)

	isCreate := func(e *Event) bool { return streamCreate.Matches(e.Subject) }
	pairs := tr.RequestReplies(isCreate)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2", len(pairs))
	}
	if pairs[0].Response == nil {
		t.Errorf("request 1 should have a correlated response")
	} else if pairs[0].Response.Subject != "_INBOX.r.1" {
		t.Errorf("response subject = %q, want _INBOX.r.1", pairs[0].Response.Subject)
	}
	if pairs[1].Response != nil {
		t.Errorf("request 2 should have no response, got %s", pairs[1].Response)
	}
}

func TestRequestReplies_Dropped(t *testing.T) {
	full := loadExpanded(t, shapedConnection())
	views := map[string]*Trace{
		"full trace":  full,
		"client view": full.ClientView(),
		"server view": full.ServerView(),
	}

	pairFor := func(t *testing.T, pairs []ReqResp, reply string) (ReqResp, bool) {
		t.Helper()
		for _, p := range pairs {
			if p.Request.Reply == reply {
				return p, true
			}
		}
		return ReqResp{}, false
	}

	t.Run("a dropped request is skipped", func(t *testing.T) {
		for name, tr := range views {
			pairs := tr.RequestReplies(isRequest)
			if len(pairs) != 2 {
				t.Errorf("%s: got %d pairs, want 2 (the dropped request is not owed a reply)", name, len(pairs))
			}
			if _, ok := pairFor(t, pairs, "_INBOX.s.r2"); ok {
				t.Errorf("%s: the dropped request was paired", name)
			}
		}
	})

	t.Run("a dropped reply pairs over the full trace and the server view", func(t *testing.T) {
		for _, name := range []string{"full trace", "server view"} {
			p, ok := pairFor(t, views[name].RequestReplies(isRequest), "_INBOX.s.r1")
			if !ok {
				t.Fatalf("%s: the request whose reply was dropped is missing", name)
			}
			if p.Response == nil || !p.Response.Dropped() || p.Response.Line != droppedReplyLine {
				t.Errorf("%s: response = %v, want the dropped reply at line %d", name, p.Response, droppedReplyLine)
			}
		}
	})

	t.Run("a dropped reply leaves the request unanswered in the client view", func(t *testing.T) {
		p, ok := pairFor(t, views["client view"].RequestReplies(isRequest), "_INBOX.s.r1")
		if !ok {
			t.Fatal("the request whose reply was dropped is missing from the client view")
		}
		if p.Response != nil {
			t.Errorf("response = %v, want none: the client never received it", p.Response)
		}
	})

	t.Run("a stalled request still pairs", func(t *testing.T) {
		for name, tr := range views {
			p, ok := pairFor(t, tr.RequestReplies(isRequest), "_INBOX.s.r3")
			if !ok || p.Response == nil || string(p.Response.Payload) != "reply" {
				t.Errorf("%s: stalled request pair = %+v, want its reply", name, p)
			}
		}
	})
}

func TestKeyFuncs_HeaderAndToken(t *testing.T) {
	b := tracegen.New("client")
	b.Info(`{}`).Connect("{}")
	b.Pub("a.one.x", "", []byte("1"))
	b.Pub("a.two.y", "", []byte("2"))
	b.Pub("a.one.z", "", []byte("3"))

	tr := loadExpanded(t, b)

	// group by the 2nd subject token (index 1)
	byTok := tr.GroupBy(BySubjectToken(1))
	if len(byTok) != 2 {
		t.Fatalf("token groups = %d, want 2 (one,two)", len(byTok))
	}
	if c, ok := byTok.Get("one"); !ok || len(c.Events) != 2 {
		t.Errorf("token 'one' group = %v, want 2 events", c)
	}

	// ByHeader excludes events lacking the header (these PUBs have none → 0 groups)
	if g := tr.GroupBy(ByHeader("Nats-Batch-Id")); len(g) != 0 {
		t.Errorf("header groups = %d, want 0 (no headers present)", len(g))
	}
}
