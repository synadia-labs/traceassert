package shaped

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
	"github.com/synadia-labs/traceassert/subject"
)

// fiReply is ADR-50's fast-ingest reply subject grammar. The client publishes each
// batch message with it as the reply subject and the server answers on that subject.
var fiReply = subject.MustParse("{prefix:rest}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

const (
	// opPing is the fast-ingest operation a client sends to recover lost acks; it
	// repeats the highest sequence sent and is not a batch message.
	opPing = 4

	// outstanding is how many ack windows the client that produced the captures may
	// run ahead of its last ack; gen/main.go configures the publisher with it.
	outstanding = 2
)

// isBatchPub is a client publish of one batch message.
func isBatchPub(e *traceassert.Event) bool {
	if e.Dir != traceassert.ToServer || e.Verb != "PUB" {
		return false
	}
	op, ok := fiReply.Int("op")(e.Reply)
	return ok && op != opPing
}

// isFlowAck is a flow ack the server sent on the batch's control channel.
func isFlowAck(e *traceassert.Event) bool {
	return e.Dir == traceassert.FromServer && e.Verb == "MSG" && fiReply.Matches(e.Subject) &&
		bytes.Contains(e.Payload, []byte(`"type":"ack"`))
}

// batchPub matches the publish of batch sequence seq.
func batchPub(seq int) M {
	return ToServer().And(BePub(), ReplyCapture(fiReply, "seq", Equal(seq)), ReplyCapture(fiReply, "op", Not(Equal(opPing))))
}

// pred turns an event matcher into a Predicate for the Select and RequestReplies helpers.
func pred(m M) traceassert.Predicate {
	return func(e *traceassert.Event) bool {
		ok, err := m.Match(e)
		return err == nil && ok
	}
}

// lastSeq is the batch sequence of the last batch publish in evs.
func lastSeq(evs []*traceassert.Event) int {
	seq, ok := GrammarInt(fiReply, "seq")(evs[len(evs)-1])
	Expect(ok).To(BeTrue(), "the last publish carries no batch sequence")
	return seq
}

var _ = Describe("a fast-ingest batch through a shaping proxy", Label("reconnect"), func() {
	var (
		session *traceassert.Session
		pubs    []*traceassert.Event // every batch publish, both connections
		acks    []*traceassert.Event // every flow ack the server sent, dropped or not
	)

	BeforeEach(func() {
		session = MustLoadSession("reconnect")
		Expect(session.Traces).To(HaveLen(2), "the scenario is one reconnect: a capture per connection")
		pubs = session.Select(isBatchPub)
		acks = session.Select(isFlowAck)
		Expect(pubs).NotTo(BeEmpty())
		Expect(acks).NotTo(BeEmpty())
	})

	Describe("the dropped flow ack", Label("drop-ack-30"), func() {
		It("fired once, on the ack for sequence 30", func() {
			// The guard runs over the full session: a rule that never fired is a
			// scenario error, and the dropped frame is absent from the client view.
			Expect(session).To(Exactly(1, ShapedBy("drop-ack-30")))
			Expect(session).To(ContainEvent(Dropped().And(
				SubjectCapture(fiReply, "seq", Equal(30)), PayloadJSON("type", Equal("ack")))))
			Expect(acks).To(Exactly(1, Dropped()))

			// The acks that reached the client, named rather than counted, so a
			// second drop fails here as well.
			for _, seq := range []int{0, 10, 20, 40} {
				Expect(acks).To(Exactly(1, Delivered().And(PayloadJSON("seq", BeNumerically("==", seq)))))
			}
		})

		It("left the request for sequence 30 unanswered in the client's view", func() {
			// Over the full session the request pairs with the ack the proxy dropped.
			Expect(session).To(RequestReply(batchPub(30), Dropped()))

			// Over the client view the ack is absent, so the request has no response,
			// which is what the client experienced.
			Expect(session.ClientView()).NotTo(RequestReply(batchPub(30), BeMsg()))
			pairs := session.ClientView().RequestReplies(pred(batchPub(30)))
			Expect(pairs).To(HaveLen(1))
			Expect(pairs[0].Response).To(BeNil())
		})

		It("did not stop the client publishing within the credit of the ack for 20", func() {
			// The batch resumes on the second connection with the sequence it left
			// at, so the events of both connections replay as one flow. A *Session
			// starts the replay afresh on each connection, where the first send here
			// is sequence 36 against a last ack of zero.
			Expect(session.ClientView().Events()).To(RespectCreditWindow(isBatchPub, isFlowAck,
				GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, outstanding))
		})

		It("with the multiplier at one, the publish of 31 is past that credit", func() {
			// The client sent 31 to 40 on the credit of the ack for 20, two windows
			// ahead. One window allows ten, so the publish of 31 fails over the client
			// view and the failure names its line. Over the full session the dropped
			// ack counts as credit the client never received and the same window
			// passes, which is why the matcher runs over the client view.
			oneWindow := RespectCreditWindow(isBatchPub, isFlowAck,
				GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, 1)
			Expect(session.ClientView().Events()).NotTo(oneWindow)
			Expect(session.Events()).To(oneWindow)
		})
	})

	Describe("the induced disconnect", Label("disconnect-pub-35"), func() {
		It("fired once, on the publish of sequence 35, which ends the first capture", func() {
			Expect(session).To(Exactly(1, ShapedBy("disconnect-pub-35")))
			Expect(session.Traces[0]).To(EndWith(ShapedBy("disconnect-pub-35").And(batchPub(35))))
			// The proxy writes a footer on an induced close too, so the capture is
			// complete and the session loader accepts it.
			Expect(session.Traces[0].Truncated()).To(BeFalse())
			// The server never saw that publish.
			Expect(session.ServerView()).NotTo(ContainEvent(batchPub(35)))
		})

		It("left the client re-subscribing the inbox before it published again", func() {
			// Conn is the index of the event's capture in the session; 1 is the
			// connection the client made after the close.
			subsAndPubs := session.Select(func(e *traceassert.Event) bool {
				return e.Conn == 1 && (e.Verb == "SUB" || e.Verb == "PUB")
			})
			Expect(subsAndPubs).To(HaveFirst(BeSub()))
			Expect(session.Traces[1]).To(ContainInOrder(BeConnect(), BeSub(), BePub()))
		})

		It("resumed the batch sequence where the first connection left it", func() {
			Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))

			left := lastSeq(session.Traces[0].Select(isBatchPub))
			Expect(session.Traces[1].Select(isBatchPub)).To(BeContiguousFrom(left+1, GrammarInt(fiReply, "seq")))
		})

		It("took a publish the server never saw, which it reported as a gap", func() {
			left := lastSeq(session.Traces[0].Select(isBatchPub))
			Expect(session.Traces[1]).To(RequestReply(batchPub(left+1),
				PayloadJSON("type", Equal("gap")).And(PayloadJSON("last_seq", BeNumerically("==", left)))))
		})
	})

	// A generated suite skips a rule whose capability the client tool did not
	// declare, and writes the harness vocabulary into the Skip message; ta copies
	// that message into the CTRF entry's skip_reason. This spec always skips, to
	// show what ends up in the report.
	It("needs a capability the client did not declare", func() {
		Skip("capability-absent: no fast ingest")
	})
})
