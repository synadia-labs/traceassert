package match

import (
	"encoding/json"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/nats-io/jsm.go/api"

	"github.com/synadia-labs/traceassert"
	"github.com/synadia-labs/traceassert/subject"
	"github.com/synadia-labs/traceassert/tracegen"
)

var fiReply = subject.MustParse("{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

func ev(dir traceassert.Direction, verb, subj, reply string, payload string) *traceassert.Event {
	return &traceassert.Event{Dir: dir, Verb: verb, Subject: subj, Reply: reply, Payload: []byte(payload)}
}

func TestEventPredicates(t *testing.T) {
	g := NewWithT(t)
	pub := ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.1.0.$FI", "hello")

	g.Expect(pub).To(ToServer())
	g.Expect(pub).To(BePub())
	g.Expect(pub).To(BeRequest())
	g.Expect(pub).To(HaveSubject("ORDERS"))
	g.Expect(pub).To(MatchReply(fiReply))
	g.Expect(pub).To(BePub().And(MatchReply(fiReply)))
	g.Expect(pub).NotTo(BeSub())
	g.Expect(pub).To(HaveReply(Equal("_INBOX.b.10.ok.1.0.$FI")))
	g.Expect(pub).To(HavePayload(Equal("hello")))

	msg := ev(traceassert.FromServer, "MSG", "_INBOX.b.ack", "", `{"type":"ack","seq":1,"msgs":15}`)
	g.Expect(msg).To(FromServer())
	g.Expect(msg).To(PayloadJSON("type", Equal("ack")))
	g.Expect(msg).To(PayloadJSON("msgs", BeNumerically("==", 15)))
}

func TestCaptureMatchers(t *testing.T) {
	g := NewWithT(t)
	start := ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.1.0.$FI", "")

	// numeric captures compare as ints; enum captures as strings.
	g.Expect(start).To(ReplyCapture(fiReply, "op", Equal(0)))
	g.Expect(start).To(ReplyCapture(fiReply, "seq", Equal(1)))
	g.Expect(start).To(ReplyCapture(fiReply, "gap", Equal("ok")))
	g.Expect(start).To(ReplyCapture(fiReply, "seq", BeNumerically(">=", 1)))

	commit := ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.9.2.$FI", "")
	g.Expect(commit).To(ReplyCapture(fiReply, "op", BeElementOf(2, 3)))
	g.Expect(start).NotTo(ReplyCapture(fiReply, "op", BeElementOf(2, 3)))
}

func fastIngest(t *testing.T) *traceassert.Trace {
	t.Helper()
	const inbox = "_INBOX.a1B2c3D4e5F6g7H8i9J0kL" // _INBOX.<22-char nuid>
	b := tracegen.New("client")
	b.Info(`{}`).Connect("{}")
	b.Sub(inbox+".>", "1")
	b.Pub("ORDERS", inbox+".10.ok.1.0.$FI", []byte("m1"))
	b.MsgString(inbox+".ack", "1", `{"type":"ack","seq":1,"msgs":15}`)
	b.Pub("ORDERS", inbox+".10.ok.2.1.$FI", []byte("m2"))
	b.Pub("ORDERS", inbox+".10.ok.3.1.$FI", []byte("m3"))
	b.Pub("ORDERS", inbox+".10.ok.4.2.$FI", []byte("m4"))
	b.MsgString(inbox+".ack", "1", `{"stream":"ORDERS","count":4}`)
	path := filepath.Join(t.TempDir(), "fi.expanded.json")
	if err := b.WriteExpandedFile(path); err != nil {
		t.Fatal(err)
	}
	tr, err := traceassert.LoadExpanded(path)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func loadTrace(t *testing.T, b *tracegen.Builder) *traceassert.Trace {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.expanded.json")
	if err := b.WriteExpandedFile(path); err != nil {
		t.Fatal(err)
	}
	tr, err := traceassert.LoadExpanded(path)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestUseOldStyleInbox(t *testing.T) {
	g := NewWithT(t)
	const nuid = "a1B2c3D4e5F6g7H8i9J0kL" // 22 base62 chars

	// a dedicated <prefix>.<nuid>.> inbox subscribed before publishing under it: PASS.
	good := tracegen.New("client")
	good.Info(`{}`).Connect("{}")
	good.Sub("_INBOX."+nuid+".>", "1")
	good.Pub("foo", "_INBOX."+nuid+".100.fail.1.0.$FI", []byte("m"))
	g.Expect(loadTrace(t, good)).To(UseOldStyleInbox("_INBOX"))

	// a shared mux inbox (.*) reused across requests is NOT old-style: FAIL.
	mux := tracegen.New("client")
	mux.Info(`{}`).Connect("{}")
	mux.Sub("_INBOX."+nuid+".*", "1")
	mux.Pub("foo", "_INBOX."+nuid+".AB", []byte("m"))
	g.Expect(loadTrace(t, mux)).NotTo(UseOldStyleInbox("_INBOX"))

	// an inbox whose token is not a real nuid (wrong length) is rejected: FAIL.
	bad := tracegen.New("client")
	bad.Info(`{}`).Connect("{}")
	bad.Sub("_INBOX.batch1.>", "1")
	bad.Pub("foo", "_INBOX.batch1.1.0.$FI", []byte("m"))
	g.Expect(loadTrace(t, bad)).NotTo(UseOldStyleInbox("_INBOX"))

	// a custom inbox prefix is honoured.
	custom := tracegen.New("client")
	custom.Info(`{}`).Connect("{}")
	custom.Sub("my.inbox."+nuid+".>", "1")
	custom.Pub("foo", "my.inbox."+nuid+".1.0.$FI", []byte("m"))
	g.Expect(loadTrace(t, custom)).To(UseOldStyleInbox("my.inbox"))
}

func TestUseNewStyleInbox(t *testing.T) {
	g := NewWithT(t)
	const nuid = "a1B2c3D4e5F6g7H8i9J0kL" // 22 base62 chars
	const suffix = "A1b2C3d4"             // 8 base62 chars (nats.go reply suffix)

	// a shared mux sub <prefix>.<nuid>.* with a <suffix> reply under it: PASS.
	mux := tracegen.New("client")
	mux.Info(`{}`).Connect("{}")
	mux.Sub("_INBOX."+nuid+".*", "1")
	mux.Pub("$JS.API.STREAM.INFO.X", "_INBOX."+nuid+"."+suffix, []byte("{}"))
	g.Expect(loadTrace(t, mux)).To(UseNewStyleInbox("_INBOX"))

	// a dedicated old-style .> inbox is not a mux: FAIL.
	old := tracegen.New("client")
	old.Info(`{}`).Connect("{}")
	old.Sub("_INBOX."+nuid+".>", "1")
	old.Pub("foo", "_INBOX."+nuid+".100.fail.1.0.$FI", []byte("m"))
	g.Expect(loadTrace(t, old)).NotTo(UseNewStyleInbox("_INBOX"))

	// a mux sub but a reply suffix of the wrong length: FAIL.
	badSuffix := tracegen.New("client")
	badSuffix.Info(`{}`).Connect("{}")
	badSuffix.Sub("_INBOX."+nuid+".*", "1")
	badSuffix.Pub("foo", "_INBOX."+nuid+".AB", []byte("m")) // 2-char suffix
	g.Expect(loadTrace(t, badSuffix)).NotTo(UseNewStyleInbox("_INBOX"))

	// a custom inbox prefix is honoured.
	custom := tracegen.New("client")
	custom.Info(`{}`).Connect("{}")
	custom.Sub("my.inbox."+nuid+".*", "1")
	custom.Pub("foo", "my.inbox."+nuid+"."+suffix, []byte("m"))
	g.Expect(loadTrace(t, custom)).To(UseNewStyleInbox("my.inbox"))
}

// TestInboxStylesAreMutuallyExclusive pins down that an old-style inbox is never
// matched as new-style, and a new-style mux inbox is never matched as old-style.
func TestInboxStylesAreMutuallyExclusive(t *testing.T) {
	g := NewWithT(t)
	const nuid = "a1B2c3D4e5F6g7H8i9J0kL" // 22 base62 chars
	const suffix = "A1b2C3d4"             // 8 base62 chars

	// a clean old-style trace: dedicated <prefix>.<nuid>.> inbox.
	oldStyle := tracegen.New("client")
	oldStyle.Info(`{}`).Connect("{}")
	oldStyle.Sub("_INBOX."+nuid+".>", "1")
	oldStyle.Pub("foo", "_INBOX."+nuid+".100.fail.1.0.$FI", []byte("m"))

	// a clean new-style trace: shared <prefix>.<nuid>.* mux inbox.
	newStyle := tracegen.New("client")
	newStyle.Info(`{}`).Connect("{}")
	newStyle.Sub("_INBOX."+nuid+".*", "1")
	newStyle.Pub("$JS.API.STREAM.INFO.X", "_INBOX."+nuid+"."+suffix, []byte("{}"))

	g.Expect(loadTrace(t, oldStyle)).To(UseOldStyleInbox("_INBOX"))
	g.Expect(loadTrace(t, oldStyle)).NotTo(UseNewStyleInbox("_INBOX"))

	g.Expect(loadTrace(t, newStyle)).To(UseNewStyleInbox("_INBOX"))
	g.Expect(loadTrace(t, newStyle)).NotTo(UseOldStyleInbox("_INBOX"))
}

func TestSliceMatchers(t *testing.T) {
	g := NewWithT(t)
	tr := fastIngest(t)
	pubs := tr.Select(func(e *traceassert.Event) bool { return fiReply.Matches(e.Reply) })

	g.Expect(pubs).To(HaveFirst(ReplyCapture(fiReply, "op", Equal(0)).And(ReplyCapture(fiReply, "seq", Equal(1)))))
	g.Expect(pubs).To(EndWith(ReplyCapture(fiReply, "op", BeElementOf(2, 3))))
	g.Expect(pubs).To(Exactly(1, ReplyCapture(fiReply, "op", BeElementOf(2, 3))))
	g.Expect(pubs).To(Each(BePub()))
	g.Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
	g.Expect(pubs).To(BeMonotonic(GrammarInt(fiReply, "seq")))

	g.Expect(tr).To(ContainInOrder(BeSub(), BePub().And(MatchReply(fiReply))))
	g.Expect(tr).To(UseOldStyleInbox("_INBOX"))
	g.Expect(tr).To(HaveFinalReply(PayloadJSON("count", BeNumerically("==", 4))))
	g.Expect(tr).To(WaitForReply(FromServer().And(PayloadJSON("type", Equal("ack")))).
		Before(ReplyCapture(fiReply, "seq", BeNumerically(">=", 2))))
}

func TestSliceMatchers_DetectViolations(t *testing.T) {
	g := NewWithT(t)
	tr := fastIngest(t)
	pubs := tr.Select(func(e *traceassert.Event) bool { return fiReply.Matches(e.Reply) })

	// a gapped sequence is not contiguous from 1 (real seq is 1,2,3,4)
	g.Expect(pubs).NotTo(BeContiguousFrom(2, GrammarInt(fiReply, "seq")))
	// there is no second commit
	g.Expect(pubs).NotTo(Exactly(2, ReplyCapture(fiReply, "op", BeElementOf(2, 3))))
}

func validRequestJSON(t *testing.T) []byte {
	t.Helper()
	req := api.JSApiStreamCreateRequest{}
	req.Name = "ORDERS"
	req.Subjects = []string{"orders.>"}
	req.Retention = api.LimitsPolicy
	req.Discard = api.DiscardOld
	req.Storage = api.FileStorage
	req.Replicas = 1
	req.MaxConsumers = -1
	req.MaxMsgs = -1
	req.MaxBytes = -1
	req.MaxMsgsPer = -1
	req.MaxMsgSize = -1
	d, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestJSMMatchers(t *testing.T) {
	g := NewWithT(t)

	req := &traceassert.Event{
		Dir: traceassert.ToServer, Verb: "PUB",
		Subject: "$JS.API.STREAM.CREATE.ORDERS", Reply: "_INBOX.r.1",
		Payload: validRequestJSON(t),
	}
	g.Expect(req).To(BeValidJetStreamRequest())
	g.Expect(req).To(BeJetStreamType("io.nats.jetstream.api.v1.stream_create_request"))
	g.Expect(req).To(DecodeJetStream(HaveField("Name", Equal("ORDERS"))))

	// a malformed body fails validation, with a useful message.
	bad := &traceassert.Event{
		Dir: traceassert.ToServer, Verb: "PUB",
		Subject: "$JS.API.STREAM.CREATE.ORDERS", Payload: []byte(`{"retention":"bogus"}`),
	}
	g.Expect(bad).NotTo(BeValidJetStreamRequest())

	// a response, type auto-detected from its payload; error form is schema-valid.
	resp := api.JSApiStreamCreateResponse{}
	resp.Type = "io.nats.jetstream.api.v1.stream_create_response"
	resp.Error = &api.ApiError{Code: 404, Description: "stream not found"}
	rj, err := json.Marshal(resp)
	g.Expect(err).NotTo(HaveOccurred())
	respEv := ev(traceassert.FromServer, "MSG", "_INBOX.r.1", "", string(rj))
	g.Expect(respEv).To(BeValidJetStreamMessage())
	g.Expect(respEv).To(BeJetStreamType("io.nats.jetstream.api.v1.stream_create_response"))

	// a typeless pub ack: it carries no `type` field, so auto-detection cannot type it,
	// but it can be decoded explicitly by schema name (the reply-to-a-publish context).
	pubAck := ev(traceassert.FromServer, "MSG", "_INBOX.r.2", "", `{"stream":"FAST","seq":5,"batch":"b1","count":5}`)
	g.Expect(pubAck).NotTo(BeJetStreamType("io.nats.jetstream.api.v1.pub_ack_response"))
	g.Expect(pubAck).To(DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response", HaveField("BatchSize", Equal(5))))
	g.Expect(pubAck).To(DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response", HaveField("Stream", Equal("FAST"))))
}
