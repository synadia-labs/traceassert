package match

import (
	"fmt"

	"github.com/synadia-labs/traceassert"
)

// RespectCreditWindow asserts that the client never sent further ahead of the last
// acknowledgment than the server's credit allowed: on every event matching sends,
// sendSeq minus the last acknowledged sequence is at most the window in force. It is
// the trace form of ADR-50's invariant that sent_seq - last_ack_seq <= msgs * outstanding.
//
// It replays the events in trace order. An event matching acks sets the last
// acknowledged sequence from ackSeq and the window from credit * multiplier; before
// the first ack the last acknowledged sequence is zero and the window is initial.
// ADR-50 passes 1, since the client must wait for the first reply before sending
// again. An implied ack needs nothing more: the latest ack sets the last acknowledged
// sequence whatever it acknowledged. Over a *Session the replay starts afresh on each
// trace, so a window granted on one connection does not carry onto the next.
//
//	Expect(trace.ClientView()).To(RespectCreditWindow(isBatchPub, isFlowAck,
//		GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, outstanding))
//
// Run it over the client view: an ack the proxy dropped then grants no credit, and a
// client that kept publishing past the lost ack is caught.
//
// A field that cannot be read from a matched event is an error rather than a skipped
// event, so a grammar that stopped matching does not turn into a green window. As an
// error it fails both To and NotTo.
func RespectCreditWindow(sends, acks traceassert.Predicate, sendSeq, ackSeq, credit IntField, initial, multiplier int) M {
	return wrap(&creditMatcher{
		sends: sends, acks: acks,
		sendSeq: sendSeq, ackSeq: ackSeq, credit: credit,
		initial: initial, multiplier: multiplier,
	})
}

type creditMatcher struct {
	sends, acks             traceassert.Predicate
	sendSeq, ackSeq, credit IntField
	initial, multiplier     int
	detail                  string // set during Match for the messages
}

func (m *creditMatcher) Match(actual any) (bool, error) {
	m.detail = ""
	s, ok := actual.(*traceassert.Session)
	if !ok {
		evs, err := toEvents(actual)
		if err != nil {
			return false, err
		}
		return m.replay(evs, "")
	}
	for i, tr := range s.Traces {
		ok, err := m.replay(tr.Events, fmt.Sprintf("connection %d ", i))
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

// replay walks one connection's events with a fresh window. where prefixes the
// frame's line in messages, naming the connection when the actual is a session,
// since line numbers restart in every capture of one.
func (m *creditMatcher) replay(evs []*traceassert.Event, where string) (bool, error) {
	lastAck := 0
	window := m.initial
	for _, e := range evs {
		if m.sends(e) {
			seq, ok := m.sendSeq(e)
			if !ok {
				return false, fmt.Errorf("%sline %d: cannot read the send sequence from %s", where, e.Line, e.String())
			}
			if seq-lastAck > window {
				m.detail = fmt.Sprintf("%sline %d: send seq %d is %d past last ack seq %d, window %d",
					where, e.Line, seq, seq-lastAck, lastAck, window)
				return false, nil
			}
		}
		if m.acks(e) {
			seq, ok := m.ackSeq(e)
			if !ok {
				return false, fmt.Errorf("%sline %d: cannot read the ack sequence from %s", where, e.Line, e.String())
			}
			c, ok := m.credit(e)
			if !ok {
				return false, fmt.Errorf("%sline %d: cannot read the credit from %s", where, e.Line, e.String())
			}
			lastAck = seq
			window = c * m.multiplier
		}
	}
	return true, nil
}

func (m *creditMatcher) FailureMessage(any) string {
	return "expected sends to respect the credit window" + suffix(m.detail)
}

func (m *creditMatcher) NegatedFailureMessage(any) string {
	return "expected some send to exceed the credit window, but every send stayed within it"
}
