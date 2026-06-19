// Package main_test is the ADR-50 *fast-ingest* batch publishing conformance suite.
//
// It is a passive, offline analyzer: it loads a captured connection trace (in the
// traceassert "expanded" format) produced by running the adr-50 CLI through a
// capturing proxy, and asserts the client used the fast-ingest protocol correctly.
//
// Each spec is named for the corresponding test in
//
//	nats-architecture-and-design/conformance/ADR-50-fast-batch.md
//
// (the FB-NNN identifiers). That document specifies an *adversarial server-side*
// conformance harness; this suite extracts the subset that is observable purely
// from a well-behaved client's wire trace. Server-driven tests (gap injection,
// idle abandonment, limits, leader change, mirrors/sources, header-mismatch
// errors) cannot be reproduced by a conformant client and are intentionally
// absent — see the README for the full mapping and rationale.
package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
	"github.com/synadia-labs/traceassert/subject"
)

func TestADR50FastIngest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ADR-50 fast-ingest batch publishing (client trace conformance)")
}

// fiReply is the single piece of per-ADR data: the fast-ingest control-plane reply
// subject grammar (ADR-50 §"Fast-ingest Batch Publishing / Client Design"):
//
//	<inbox>.<flow>.<gap>.<batch_seq>.<operation>.$FI
//
// orbit.go folds the batch id into the inbox prefix it subscribes to, so the
// {prefix:rest} token absorbs the leading "_INBOX.<nuid>" tokens.
var fiReply = subject.MustParse(
	"{prefix:rest}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

// streamCreate matches the JetStream stream-create API subject.
var streamCreate = subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

// Fast-ingest operation codes (the reply-subject <operation> field, ADR-50).
const (
	opStart       = 0 // start a batch
	opAppend      = 1 // append to a batch
	opCommitStore = 2 // commit and store the final message
	opCommitEOB   = 3 // commit without storing the final message
	opPing        = 4 // keep-alive / recover lost acks
)

const pubAckType = "io.nats.jetstream.api.v1.pub_ack_response"

// loadCapture loads an expanded trace from $envVar, or testdata/<file>. A missing,
// unreadable, or truncated capture is a hard failure — never a skip — so a green
// run always means real evidence was asserted, not that the capture was quietly
// absent. Produce captures as described in README.md.
func loadCapture(file, envVar string) *traceassert.Trace {
	path := os.Getenv(envVar)
	if path == "" {
		path = filepath.Join("testdata", file)
	}
	_, statErr := os.Stat(path)
	Expect(statErr).NotTo(HaveOccurred(),
		fmt.Sprintf("capture %q not present — produce one as described in README.md", path))
	tr, err := traceassert.LoadExpanded(path)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("loading capture %q", path))
	Expect(tr.Truncated()).To(BeFalse(),
		fmt.Sprintf("capture %q is truncated (the proxy cut it short) — recapture as described in README.md", path))
	return tr
}

// clientBatchPubs are the client→server fast-ingest publishes (their reply subject
// carries the $FI control plane).
func clientBatchPubs(tr *traceassert.Trace) []*traceassert.Event {
	return tr.Select(func(e *traceassert.Event) bool {
		return e.Dir == traceassert.ToServer && fiReply.Matches(e.Reply)
	})
}

// serverBatchReplies are the server→client messages delivered on the control
// channel (flow acks, gaps, errors, and the final pub ack).
func serverBatchReplies(tr *traceassert.Trace) []*traceassert.Event {
	return tr.Select(func(e *traceassert.Event) bool {
		return e.Dir == traceassert.FromServer && fiReply.Matches(e.Subject)
	})
}

// opOf returns the fast-ingest operation for an event, reading its reply (client
// publishes) then its subject (server replies). Returns -1 if absent.
func opOf(e *traceassert.Event) int {
	if v, ok := fiReply.Int("op")(e.Reply); ok {
		return v
	}
	if v, ok := fiReply.Int("op")(e.Subject); ok {
		return v
	}
	return -1
}

func seqOf(e *traceassert.Event) int {
	if v, ok := fiReply.Int("seq")(e.Reply); ok {
		return v
	}
	v, _ := fiReply.Int("seq")(e.Subject)
	return v
}

func without(evs []*traceassert.Event, op int) []*traceassert.Event {
	out := make([]*traceassert.Event, 0, len(evs))
	for _, e := range evs {
		if opOf(e) != op {
			out = append(out, e)
		}
	}
	return out
}

// A control-channel reply is a flow message (ack/gap/err) when it carries a
// "type" field; the final pub ack is the only one that does not.
func hasTypeField(e *traceassert.Event) bool {
	return strings.Contains(string(e.Payload), `"type"`)
}

var _ = Describe("ADR-50 fast-ingest batch publishing", func() {
	Describe("multi-message batch", func() {
		var (
			trace   *traceassert.Trace
			pubs    []*traceassert.Event // all client batch publishes
			nonPing []*traceassert.Event // ... excluding pings
			replies []*traceassert.Event // server control-channel replies
			flowAck []*traceassert.Event // ... that are BatchFlowAcks
			flow    int                  // requested upper-bound flow
			lastSeq int                  // highest batch sequence sent
		)

		BeforeEach(func() {
			trace = loadCapture("fastbatch.expanded.json", "ADR50_CAPTURE")

			pubs = clientBatchPubs(trace)
			Expect(pubs).NotTo(BeEmpty(), "capture contains no fast-ingest publishes")
			nonPing = without(pubs, opPing)
			Expect(len(nonPing)).To(BeNumerically(">=", 2), "this capture must be a multi-message batch")

			f, ok := fiReply.Int("flow")(pubs[0].Reply)
			Expect(ok).To(BeTrue())
			flow = f
			lastSeq = seqOf(nonPing[len(nonPing)-1])

			replies = serverBatchReplies(trace)
			flowAck = nil
			for _, e := range replies {
				if strings.Contains(string(e.Payload), `"type":"ack"`) {
					flowAck = append(flowAck, e)
				}
			}
		})

		It("uses an old-style inbox control channel, subscribed before publishing (ADR-50 §Control Channel)", func() {
			// ADR-50 mandates an old-style inbox (NOT the mux inbox) for the
			// fast-ingest control channel, and the conformance harness requires the
			// inbox SUB to precede any publish replying under it.
			Expect(trace).To(UseOldStyleInbox("_INBOX"))

			// All fast-ingest traffic belongs to exactly one batch (one inbox prefix).
			b, ok := trace.GroupBy(traceassert.ByCapture(fiReply, "prefix")).One()
			Expect(ok).To(BeTrue(), "expected exactly one fast-ingest batch in the capture")
			Expect(b.ToServer()).NotTo(BeEmpty())
		})

		It("FB-101: the stream is created with AllowBatchPublish enabled", func() {
			create, ok := trace.First(func(e *traceassert.Event) bool {
				return e.Dir == traceassert.ToServer && streamCreate.Matches(e.Subject)
			})
			if !ok {
				Skip("capture has no STREAM.CREATE (stream pre-existed) — re-run the CLI with create:true")
			}

			// The create request must be a schema-valid JetStream API request and
			// carry allow_batched.
			Expect(create).To(BeValidJetStreamRequest())
			Expect(create).To(PayloadJSON("allow_batched", BeTrue()))

			// Fast-ingest requires server API level >= 4 (server 2.14+); the INFO
			// preamble carries the negotiated level.
			info, ok := trace.First(func(e *traceassert.Event) bool { return e.Verb == "INFO" })
			Expect(ok).To(BeTrue())
			Expect(info).To(PayloadJSON("api_lvl", BeNumerically(">=", 4)))
		})

		It("FB-301: establishes, appends, then commits a contiguous, in-order batch", func() {
			// Operation sequence: start (0) -> append (1)* -> commit-store (2) / EOB (3).
			Expect(nonPing).To(HaveFirst(ReplyCapture(fiReply, "op", Equal(opStart))))
			Expect(nonPing).To(EndWith(ReplyCapture(fiReply, "op",
				Or(Equal(opCommitStore), Equal(opCommitEOB)))))
			Expect(nonPing[1 : len(nonPing)-1]).To(Each(ReplyCapture(fiReply, "op", Equal(opAppend))))

			// Batch sequences are gapless 1..N, in trace order, and these are publishes.
			Expect(nonPing).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
			Expect(nonPing).To(Each(BePub().Or(BeHPub())))

			// The batch commits with a final pub ack carrying batch + count.
			commit := lastCommitAck(replies)
			Expect(commit).NotTo(BeNil(), "no commit pub ack found on the control channel")
			Expect(commit).To(DecodeJetStreamAs(pubAckType, And(
				HaveField("BatchSize", Equal(expectedCount(nonPing))),
				HaveField("BatchId", Not(BeEmpty())),
				HaveField("Sequence", BeNumerically(">", 0)),
			)))
		})

		It("FB-303: BatchFlowAck seq/msgs invariants hold", func() {
			// A multi-message batch always elicits at least the establishment
			// BatchFlowAck (FB-301), so its absence is a failure, not inconclusive.
			Expect(flowAck).NotTo(BeEmpty(),
				"no BatchFlowAck observed — a multi-message batch must receive the establishment ack (FB-301)")
			// msgs (the active ack frequency) is always a positive count.
			Expect(flowAck).To(Each(PayloadJSON("msgs", BeNumerically(">=", 1))))
			// Every ack refers to a sequence the client has actually sent.
			Expect(flowAck).To(Each(PayloadJSON("seq", BeNumerically("<=", lastSeq))))
			// Acks arrive for non-decreasing sequences (the message each rode in on).
			Expect(flowAck).To(BeMonotonic(GrammarInt(fiReply, "seq")))
		})

		It("FB-401/402/406: the client only emits valid control-plane reply subjects", func() {
			// MatchReply enforces the structural grammar: trailing $FI sentinel
			// (FB-406) and a gap token in {ok,fail} (FB-402).
			Expect(pubs).To(Each(MatchReply(fiReply)))
			// Every operation is one the server knows about (FB-401).
			Expect(pubs).To(Each(ReplyCapture(fiReply, "op",
				Or(Equal(opStart), Equal(opAppend), Equal(opCommitStore), Equal(opCommitEOB), Equal(opPing)))))
		})

		It("FB-403/404: the batch identifier stays within the 64-character limit", func() {
			// orbit.go identifies the batch by its old-style inbox; UseOldStyleInbox
			// already validates a 22-char nats nuid (well under 64). Assert the whole
			// identifying prefix is within the limit too.
			b, ok := trace.GroupBy(traceassert.ByCapture(fiReply, "prefix")).One()
			Expect(ok).To(BeTrue())
			Expect(len(b.Key)).To(BeNumerically("<=", 64))
		})

		It("FB-701/702: server flow (msgs) never exceeds the requested upper bound", func() {
			Expect(flowAck).NotTo(BeEmpty(),
				"no BatchFlowAck observed — a multi-message batch must receive the establishment ack (FB-301)")
			// FB-701: the initial ack frequency is in [1, flow].
			Expect(flowAck[0]).To(PayloadJSON("msgs", BeNumerically(">=", 1)))
			// FB-702: the server may reduce, but must never exceed, the requested flow.
			Expect(flowAck).To(Each(PayloadJSON("msgs", BeNumerically("<=", flow))))
		})

		It("FB-1401: the final PubAck carries batch and count", func() {
			commit := lastCommitAck(replies)
			Expect(commit).NotTo(BeNil())
			Expect(commit).To(DecodeJetStreamAs(pubAckType, And(
				HaveField("BatchSize", Equal(expectedCount(nonPing))),
				HaveField("BatchId", Not(BeEmpty())),
			)))
		})

		It("FB-1402: the PubAck is the only control-channel message without a type field", func() {
			Expect(replies).NotTo(BeEmpty())
			untyped := 0
			for _, e := range replies {
				if hasTypeField(e) {
					Expect(string(e.Payload)).To(Or(
						ContainSubstring(`"type":"ack"`),
						ContainSubstring(`"type":"gap"`),
						ContainSubstring(`"type":"err"`),
					))
					continue
				}
				untyped++
				Expect(string(e.Payload)).To(ContainSubstring(`"count"`),
					"a typeless control-channel message must be the pub ack (carries count)")
			}
			Expect(untyped).To(Equal(1), "exactly one control-channel message (the pub ack) must lack a type field")
		})
	})

	Describe("single-message immediate commit", func() {
		var trace *traceassert.Trace
		var pubs []*traceassert.Event

		BeforeEach(func() {
			trace = loadCapture("single.expanded.json", "ADR50_CAPTURE_SINGLE")
			pubs = clientBatchPubs(trace)
		})

		It("FB-201: operation 2 at batch_seq 1 returns a normal PubAck", func() {
			Expect(pubs).To(HaveLen(1), "a single-message capture must contain exactly one fast-ingest publish")
			Expect(pubs[0]).To(ReplyCapture(fiReply, "op", Equal(opCommitStore)))
			Expect(pubs[0]).To(ReplyCapture(fiReply, "seq", Equal(1)))

			replies := serverBatchReplies(trace)
			Expect(replies).NotTo(BeEmpty())
			// No BatchFlowAck precedes the ack: the single message commits immediately.
			for _, e := range replies {
				Expect(hasTypeField(e)).To(BeFalse(),
					"single immediate commit must not be preceded by a BatchFlowAck")
			}
			Expect(replies[len(replies)-1]).To(DecodeJetStreamAs(pubAckType,
				HaveField("BatchSize", Equal(1))))
		})
	})
})

// lastCommitAck returns the last commit-operation reply (the pub ack), or nil.
func lastCommitAck(replies []*traceassert.Event) *traceassert.Event {
	var ack *traceassert.Event
	for _, e := range replies {
		if op := opOf(e); op == opCommitStore || op == opCommitEOB {
			ack = e
		}
	}
	return ack
}

// expectedCount is the BatchSize the server should report: commit-store stores the
// final message (count == messages sent); commit-eob does not (count == sent-1).
func expectedCount(nonPing []*traceassert.Event) int {
	n := len(nonPing)
	if opOf(nonPing[n-1]) == opCommitEOB {
		n--
	}
	return n
}
