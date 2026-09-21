package match

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	"github.com/synadia-labs/traceassert/tracegen"
)

// creditSend is a fast-ingest publish carrying batch sequence seq in its $FI reply.
func creditSend(line, seq int) *traceassert.Event {
	e := ev(traceassert.ToServer, "PUB", "ORDERS", fmt.Sprintf("_INBOX.b.10.ok.%d.1.$FI", seq), "x")
	e.Line = line
	return e
}

// creditAck is a BatchFlowAck for seq granting msgs.
func creditAck(line, seq, msgs int) *traceassert.Event {
	e := ev(traceassert.FromServer, "MSG", "_INBOX.b.10.ok.1.1.$FI", "",
		fmt.Sprintf(`{"type":"ack","seq":%d,"msgs":%d}`, seq, msgs))
	e.Line = line
	return e
}

func isCreditSend(e *traceassert.Event) bool {
	return e.Dir == traceassert.ToServer && fiReply.Matches(e.Reply)
}

func isCreditAck(e *traceassert.Event) bool {
	return e.Dir == traceassert.FromServer && strings.Contains(string(e.Payload), `"type":"ack"`)
}

// adr50Window is the ADR-50 wiring: initial 1, window msgs * outstanding.
func adr50Window(outstanding int) M {
	return RespectCreditWindow(isCreditSend, isCreditAck,
		GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, outstanding)
}

func TestPayloadInt(t *testing.T) {
	g := NewWithT(t)
	msgs := PayloadInt("msgs")

	v, ok := msgs(ev(traceassert.FromServer, "MSG", "x", "", `{"msgs":15}`))
	g.Expect(ok).To(BeTrue())
	g.Expect(v).To(Equal(15))

	v, ok = msgs(ev(traceassert.FromServer, "MSG", "x", "", `{"msgs":"7"}`))
	g.Expect(ok).To(BeTrue())
	g.Expect(v).To(Equal(7))

	for _, payload := range []string{`{}`, `{"msgs":1.5}`, `{"msgs":"many"}`, `{"msgs":true}`, `{"msgs":null}`} {
		_, ok = msgs(ev(traceassert.FromServer, "MSG", "x", "", payload))
		g.Expect(ok).To(BeFalse(), payload)
	}
}

func TestRespectCreditWindow_Holds(t *testing.T) {
	g := NewWithT(t)
	// ack seq 1 with msgs 2 and outstanding 2 opens a window of 4: seqs 2..5 may follow,
	// then ack seq 5 opens 6..9.
	evs := []*traceassert.Event{
		creditSend(1, 1),
		creditAck(2, 1, 2),
		creditSend(3, 2), creditSend(4, 3), creditSend(5, 4), creditSend(6, 5),
		creditAck(7, 5, 2),
		creditSend(8, 6), creditSend(9, 7), creditSend(10, 8), creditSend(11, 9),
	}
	g.Expect(evs).To(adr50Window(2))
	g.Expect(&traceassert.Trace{Events: evs}).To(adr50Window(2))
}

func TestRespectCreditWindow_ExceededOnFrame(t *testing.T) {
	g := NewWithT(t)
	evs := []*traceassert.Event{
		creditSend(1, 1),
		creditAck(2, 1, 2), // window 4: seqs 2..5
		creditSend(3, 2), creditSend(4, 3), creditSend(5, 4), creditSend(6, 5),
		creditSend(7, 6), // 6 - 1 = 5 > 4
	}
	m := adr50Window(2)
	ok, err := m.Match(evs)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	msg := m.FailureMessage(evs)
	for _, want := range []string{"line 7:", "send seq 6", "last ack seq 1", "window 4"} {
		g.Expect(msg).To(ContainSubstring(want))
	}
	g.Expect(evs).NotTo(adr50Window(2))
	g.Expect(m.NegatedFailureMessage(evs)).To(ContainSubstring("exceed the credit window"))
}

func TestRespectCreditWindow_BeforeFirstAck(t *testing.T) {
	g := NewWithT(t)
	// ADR-50: one send, then wait for the first reply.
	two := []*traceassert.Event{creditSend(1, 1), creditSend(2, 2)}
	m := adr50Window(2)
	ok, err := m.Match(two)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(m.FailureMessage(two)).To(ContainSubstring("line 2: send seq 2 is 2 past last ack seq 0, window 1"))

	// the initial window is what a protocol says it is: with 3 the same three sends hold.
	three := []*traceassert.Event{creditSend(1, 1), creditSend(2, 2), creditSend(3, 3)}
	wide := RespectCreditWindow(isCreditSend, isCreditAck,
		GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 3, 1)
	g.Expect(three).To(wide)
	narrow := RespectCreditWindow(isCreditSend, isCreditAck,
		GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 2, 1)
	g.Expect(three).NotTo(narrow)
}

func TestRespectCreditWindow_AckRaisesWindow(t *testing.T) {
	g := NewWithT(t)
	// msgs 1 with outstanding 2 opens a window of 2; the ack at seq 3 raises msgs to 4,
	// so eight sends may follow it.
	raised := []*traceassert.Event{
		creditSend(1, 1),
		creditAck(2, 1, 1),
		creditSend(3, 2), creditSend(4, 3),
		creditAck(5, 3, 4),
		creditSend(6, 4), creditSend(7, 5), creditSend(8, 6), creditSend(9, 7),
		creditSend(10, 8), creditSend(11, 9), creditSend(12, 10), creditSend(13, 11),
	}
	g.Expect(raised).To(adr50Window(2))

	// without the second ack the same sends run past the window of 2 at seq 4.
	var stale []*traceassert.Event
	for _, e := range raised {
		if e.Line != 5 {
			stale = append(stale, e)
		}
	}
	m := adr50Window(2)
	ok, err := m.Match(stale)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(m.FailureMessage(stale)).To(ContainSubstring("line 6: send seq 4 is 3 past last ack seq 1, window 2"))
}

func TestRespectCreditWindow_SessionStartsAfresh(t *testing.T) {
	g := NewWithT(t)
	first := &traceassert.Trace{Events: []*traceassert.Event{
		creditSend(1, 1),
		creditAck(2, 1, 5), // window 10 on this connection
		creditSend(3, 2), creditSend(4, 3), creditSend(5, 4),
	}}
	// the client reconnected and published twice before the new connection's first ack.
	second := &traceassert.Trace{Events: []*traceassert.Event{
		creditSend(1, 1), creditSend(2, 2),
	}}

	// flattened, the first connection's window carries over and the run passes.
	flat := append(append([]*traceassert.Event{}, first.Events...), second.Events...)
	g.Expect(flat).To(adr50Window(2))

	// as a session the second connection starts with no ack and the initial window.
	s := traceassert.NewSession(first, second)
	m := adr50Window(2)
	ok, err := m.Match(s)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(m.FailureMessage(s)).To(ContainSubstring("connection 1 line 2: send seq 2 is 2 past last ack seq 0, window 1"))

	// a second connection that waits for its own ack holds.
	polite := &traceassert.Trace{Events: []*traceassert.Event{
		creditSend(1, 1),
		creditAck(2, 1, 1),
		creditSend(3, 2), creditSend(4, 3),
	}}
	g.Expect(traceassert.NewSession(first, polite)).To(adr50Window(2))
}

func TestRespectCreditWindow_DroppedAckGrantsNoCreditInClientView(t *testing.T) {
	g := NewWithT(t)
	b := tracegen.New("client")
	b.Info(`{}`).Connect("{}")
	b.Pub("ORDERS", "_INBOX.b.10.ok.1.0.$FI", []byte("x"))
	b.MsgString("_INBOX.b.10.ok.1.0.$FI", "1", `{"type":"ack","seq":1,"msgs":2}`).Shaped("drop-ack", "drop")
	b.Pub("ORDERS", "_INBOX.b.10.ok.2.1.$FI", []byte("x"))
	b.Pub("ORDERS", "_INBOX.b.10.ok.3.1.$FI", []byte("x"))
	tr := loadTrace(t, b)

	// over the full trace the ack is in the file and the window of 4 covers seqs 2 and 3.
	g.Expect(tr).To(adr50Window(2))

	// the client never received it: it published seq 2 on the initial window of 1.
	m := adr50Window(2)
	ok, err := m.Match(tr.ClientView())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse())
	g.Expect(m.FailureMessage(tr.ClientView())).To(ContainSubstring("send seq 2 is 2 past last ack seq 0, window 1"))

	// the same over a session's client view.
	s := traceassert.NewSession(tr)
	g.Expect(s).To(adr50Window(2))
	g.Expect(s.ClientView()).NotTo(adr50Window(2))
}

func TestRespectCreditWindow_UnreadableFieldIsAnError(t *testing.T) {
	g := NewWithT(t)

	// a send whose reply no longer carries the grammar: the send sequence cannot be read.
	badSend := ev(traceassert.ToServer, "PUB", "ORDERS", "_INBOX.b.10.ok.1.0.$FI", "x")
	badSend.Line = 3
	sendsByReply := func(e *traceassert.Event) bool { return e.Dir == traceassert.ToServer }
	m := RespectCreditWindow(sendsByReply, isCreditAck,
		PayloadInt("seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, 2)
	_, err := m.Match([]*traceassert.Event{badSend})
	g.Expect(err).To(MatchError(ContainSubstring("line 3: cannot read the send sequence")))

	// an ack without msgs: the credit cannot be read.
	noMsgs := ev(traceassert.FromServer, "MSG", "_INBOX.b.10.ok.1.1.$FI", "", `{"type":"ack","seq":1}`)
	noMsgs.Line = 2
	_, err = adr50Window(2).Match([]*traceassert.Event{creditSend(1, 1), noMsgs})
	g.Expect(err).To(MatchError(ContainSubstring("line 2: cannot read the credit")))

	// an ack without seq: the ack sequence cannot be read.
	noSeq := ev(traceassert.FromServer, "MSG", "_INBOX.b.10.ok.1.1.$FI", "", `{"type":"ack","msgs":2}`)
	noSeq.Line = 2
	_, err = adr50Window(2).Match([]*traceassert.Event{creditSend(1, 1), noSeq})
	g.Expect(err).To(MatchError(ContainSubstring("line 2: cannot read the ack sequence")))
}

func TestRespectCreditWindow_RejectsOtherActuals(t *testing.T) {
	g := NewWithT(t)
	_, err := adr50Window(2).Match("not a trace")
	g.Expect(err).To(HaveOccurred())
}
