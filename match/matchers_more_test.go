package match

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	"github.com/synadia-labs/traceassert/subject"
	"github.com/synadia-labs/traceassert/tracegen"
)

func TestVerbPredicates(t *testing.T) {
	g := NewWithT(t)
	for _, c := range []struct {
		verb string
		m    M
	}{
		{"PUB", BePub()}, {"HPUB", BeHPub()}, {"SUB", BeSub()},
		{"UNSUB", BeUnsub()}, {"MSG", BeMsg()}, {"HMSG", BeHMsg()}, {"CONNECT", BeConnect()},
	} {
		g.Expect(&traceassert.Event{Verb: c.verb}).To(c.m, "%s should match", c.verb)
		g.Expect(&traceassert.Event{Verb: "PING"}).NotTo(c.m, "PING should not match %s", c.verb)
	}
}

func TestRequestAndNoReplyPredicates(t *testing.T) {
	g := NewWithT(t)
	req := ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.x", "hi")
	g.Expect(req).To(BeRequest())
	g.Expect(req).NotTo(HaveNoReply())

	noReply := ev(traceassert.ToServer, "PUB", "ORDERS", "", "hi")
	g.Expect(noReply).To(HaveNoReply())
	g.Expect(noReply).NotTo(BeRequest())
}

func TestSubjectMatchers(t *testing.T) {
	g := NewWithT(t)
	streamCreate := subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

	create := ev(traceassert.ToServer, "PUB", "$JS.API.STREAM.CREATE.ORDERS", "", "")
	g.Expect(create).To(MatchSubject(streamCreate))
	g.Expect(create).To(SubjectCapture(streamCreate, "stream", Equal("ORDERS")))
	g.Expect(ev(traceassert.ToServer, "PUB", "other.subject", "", "")).NotTo(MatchSubject(streamCreate))

	g.Expect(ev(traceassert.ToServer, "PUB", "a.two.c", "", "")).To(SubjectToken(1, Equal("two")))
	// out-of-range token resolves to "" (the transform's documented fallback)
	g.Expect(ev(traceassert.ToServer, "PUB", "a", "", "")).To(SubjectToken(9, Equal("")))
}

func TestSIDQueueAndHeaderMatchers(t *testing.T) {
	g := NewWithT(t)

	sub := &traceassert.Event{Verb: "SUB", Subject: "foo", SID: "7", Queue: "workers"}
	g.Expect(sub).To(HaveSID(Equal("7")))
	g.Expect(sub).To(HaveQueueGroup(Equal("workers")))

	hdr := &traceassert.Event{Verb: "HPUB", Subject: "foo", Header: map[string][]string{"Nats-Msg-Id": {"abc"}}}
	g.Expect(hdr).To(HaveHeader("nats-msg-id")) // case-insensitive
	g.Expect(hdr).To(HaveHeaderValue("Nats-Msg-Id", Equal("abc")))
	g.Expect(hdr).To(HaveNoHeader("Nats-Stream"))
	g.Expect(hdr).NotTo(HaveHeader("Nats-Stream"))
	g.Expect(hdr).NotTo(HaveNoHeader("Nats-Msg-Id"))
}

func TestPayloadEmptiness(t *testing.T) {
	g := NewWithT(t)
	g.Expect(ev(traceassert.ToServer, "PUB", "foo", "", "")).To(PayloadIsEmpty())
	g.Expect(ev(traceassert.ToServer, "PUB", "foo", "", "x")).NotTo(PayloadIsEmpty())
}

func TestCombinatorsOrNot(t *testing.T) {
	g := NewWithT(t)
	pub := ev(traceassert.ToServer, "PUB", "foo", "", "")
	sub := &traceassert.Event{Verb: "SUB"}

	g.Expect(pub).To(BePub().Or(BeSub()))
	g.Expect(sub).To(BePub().Or(BeSub()))
	g.Expect(&traceassert.Event{Verb: "MSG"}).NotTo(BePub().Or(BeSub()))

	g.Expect(sub).To(BePub().Not())
	g.Expect(pub).NotTo(BePub().Not())
}

func TestFieldExtractors(t *testing.T) {
	g := NewWithT(t)
	streamCreate := subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

	// GrammarStr: subject is tried first, then the reply (so a reply-encoded grammar works).
	gap := GrammarStr(fiReply, "gap")
	v, ok := gap(ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.1.0.$FI", ""))
	g.Expect(ok).To(BeTrue())
	g.Expect(v).To(Equal("ok"))
	_, ok = gap(ev(traceassert.ToServer, "PUB", "plain", "", ""))
	g.Expect(ok).To(BeFalse())

	// PayloadField
	count := PayloadField("count")
	v, ok = count(ev(traceassert.FromServer, "MSG", "x", "", `{"count":4}`))
	g.Expect(ok).To(BeTrue())
	g.Expect(v).To(Equal("4"))
	_, ok = count(ev(traceassert.FromServer, "MSG", "x", "", `{}`))
	g.Expect(ok).To(BeFalse())

	// SameValue: a subject capture equals a payload field on the same event.
	stream := GrammarStr(streamCreate, "stream")
	name := PayloadField("name")
	g.Expect(ev(traceassert.ToServer, "PUB", "$JS.API.STREAM.CREATE.ORDERS", "", `{"name":"ORDERS"}`)).
		To(SameValue(stream, name))
	g.Expect(ev(traceassert.ToServer, "PUB", "$JS.API.STREAM.CREATE.ORDERS", "", `{"name":"BILLING"}`)).
		NotTo(SameValue(stream, name))
	g.Expect(ev(traceassert.ToServer, "PUB", "$JS.API.STREAM.CREATE.ORDERS", "", `{}`)).
		NotTo(SameValue(stream, name)) // a missing field fails rather than matching
}

func TestStreamQuantifiers(t *testing.T) {
	g := NewWithT(t)
	tr := fastIngest(t) // INFO, CONNECT, SUB, 4×PUB, 2×MSG

	g.Expect(tr).To(ContainEvent(BeSub()))
	g.Expect(tr).To(ContainEvent(BeConnect()))
	g.Expect(tr).NotTo(ContainEvent(BeHMsg()))

	g.Expect(tr).To(AtLeast(4, BePub()))
	g.Expect(tr).NotTo(AtLeast(5, BePub()))

	g.Expect(tr).To(Never(BeUnsub()))
	g.Expect(tr).NotTo(Never(BeMsg())) // exercises the "matched at" detail path
}

func TestPositionalNegatives(t *testing.T) {
	g := NewWithT(t)

	var empty []*traceassert.Event
	g.Expect(empty).NotTo(HaveFirst(BePub()))      // "no events"
	g.Expect(empty).NotTo(EndWith(BePub()))        // "no events"
	g.Expect(empty).NotTo(HaveFinalReply(BePub())) // "no server replies found"

	evs := []*traceassert.Event{
		ev(traceassert.ToServer, "SUB", "x", "", ""),
		ev(traceassert.ToServer, "PUB", "y", "", ""),
	}
	g.Expect(evs).NotTo(HaveFirst(BePub())) // first is SUB
	g.Expect(evs).NotTo(EndWith(BeSub()))   // last is PUB
	g.Expect(evs).NotTo(Each(BePub()))      // a SUB is present

	// BeMonotonic detail paths: a decreasing sequence, then no sequence field at all.
	dec := []*traceassert.Event{
		ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.2.0.$FI", ""),
		ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.1.0.$FI", ""),
	}
	g.Expect(dec).NotTo(BeMonotonic(GrammarInt(fiReply, "seq")))
	g.Expect([]*traceassert.Event{ev(traceassert.ToServer, "PUB", "plain", "", "")}).
		NotTo(BeMonotonic(GrammarInt(fiReply, "seq")))
}

func TestRequestReplyMatcher(t *testing.T) {
	g := NewWithT(t)
	streamCreate := subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

	answered := tracegen.New("client")
	answered.Info(`{}`).Connect("{}")
	answered.Pub("$JS.API.STREAM.CREATE.ORDERS", "_INBOX.r.1", []byte(`{"name":"ORDERS"}`))
	answered.MsgString("_INBOX.r.1", "9", `{"hello":"world"}`)
	g.Expect(loadTrace(t, answered)).To(RequestReply(MatchSubject(streamCreate), FromServer()))

	// a request whose reply never arrives fails, with a line-numbered reason.
	unanswered := tracegen.New("client")
	unanswered.Info(`{}`).Connect("{}")
	unanswered.Pub("$JS.API.STREAM.CREATE.BILLING", "_INBOX.r.2", []byte(`{"name":"BILLING"}`))
	tr := loadTrace(t, unanswered)

	rr := RequestReply(MatchSubject(streamCreate), FromServer())
	ok, err := rr.Match(tr)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(rr.FailureMessage(tr)).To(ContainSubstring("no correlated response"))
	g.Expect(rr.NegatedFailureMessage(tr)).To(ContainSubstring("some request/response"))

	// a response that arrives but fails the response matcher (the MSG reply is not a SUB).
	wrongResp := RequestReply(MatchSubject(streamCreate), BeSub())
	at := loadTrace(t, answered)
	ok, err = wrongResp.Match(at)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(wrongResp.FailureMessage(at)).To(ContainSubstring("did not match"))

	// wrong actual type is an error, not a mismatch.
	_, err = RequestReply(BePub(), FromServer()).Match([]*traceassert.Event{})
	g.Expect(err).To(HaveOccurred())
}

func TestFailureMessages(t *testing.T) {
	g := NewWithT(t)

	// event matcher: failure message names the expectation and renders the event.
	bs := BeSub()
	e := ev(traceassert.ToServer, "PUB", "ORDERS", "", "")
	ok, err := bs.Match(e)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(bs.FailureMessage(e)).To(SatisfyAll(
		ContainSubstring("expected event to"), ContainSubstring("be a SUB"), ContainSubstring("PUB")))
	g.Expect(bs.NegatedFailureMessage(e)).To(ContainSubstring("expected event not to"))

	// event matcher rejects a non-event actual.
	_, err = BePub().Match("not an event")
	g.Expect(err).To(HaveOccurred())

	// slice matcher: failure message carries the detail suffix ("got 1").
	ex := Exactly(2, BePub())
	evs := []*traceassert.Event{ev(traceassert.ToServer, "PUB", "a", "", "")}
	ok, err = ex.Match(evs)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(ex.FailureMessage(evs)).To(SatisfyAll(ContainSubstring("exactly 2"), ContainSubstring("got 1")))
	g.Expect(ex.NegatedFailureMessage(evs)).To(ContainSubstring("not to"))

	// slice matcher rejects an unsupported actual.
	_, err = Each(BePub()).Match("nope")
	g.Expect(err).To(HaveOccurred())
}

func TestSliceMatchersAcceptConversation(t *testing.T) {
	g := NewWithT(t)
	tr := fastIngest(t)

	// grouping by the leading subject token yields the "ORDERS" conversation (its 4 PUBs),
	// and passing a *Conversation to a slice matcher exercises that toEvents branch.
	orders, ok := tr.GroupBy(traceassert.BySubjectToken(0)).Get("ORDERS")
	g.Expect(ok).To(BeTrue())
	g.Expect(orders).To(ContainEvent(BePub()))
	g.Expect(orders).To(Each(BePub()))
}

func TestJSMNegatives(t *testing.T) {
	g := NewWithT(t)

	// subject is not a JetStream API request
	g.Expect(ev(traceassert.ToServer, "PUB", "not.an.api", "", `{}`)).NotTo(BeValidJetStreamRequest())
	// payload is not parseable as a JetStream message
	g.Expect(ev(traceassert.FromServer, "MSG", "x", "", `not json`)).NotTo(BeValidJetStreamMessage())
	// the detected type does not equal the asserted one
	resp := ev(traceassert.FromServer, "MSG", "x", "", `{"type":"io.nats.jetstream.api.v1.stream_create_response"}`)
	g.Expect(resp).NotTo(BeJetStreamType("io.nats.jetstream.api.v1.pub_ack_response"))
	// an unknown schema name for explicit decode
	g.Expect(ev(traceassert.FromServer, "MSG", "x", "", `{}`)).
		NotTo(DecodeJetStreamAs("io.nats.bogus.type", HaveField("X", Equal(1))))
}
