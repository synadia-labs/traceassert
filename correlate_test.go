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
