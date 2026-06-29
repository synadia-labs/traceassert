// Package main_test is the JetStream INFO-request rate-limiting conformance suite.
//
// It is a passive, offline analyzer: it loads a captured connection trace (in the
// traceassert "expanded" format) and asserts the client did not poll any stream or
// consumer's INFO endpoint faster than a token bucket allows - at most a short burst,
// then a sustained rate.
//
// It is a worked example of traceassert's generic rate-limit assertion
// (match.RespectRateLimit): the only JetStream-specific code here is the selector
// (isInfoRequest) and the grouping key (infoAsset); the token-bucket machinery is the
// library's. See the README for how to retarget it at other repeating events.
package main_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
	"github.com/synadia-labs/traceassert/subject"
)

func TestInfoRequests(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "JetStream INFO request rate limiting (client trace conformance)")
}

// The two JetStream API subjects this suite rate-limits. Both are fixed-arity, so a
// match is exact: STREAM.INFO never matches CONSUMER.INFO, CREATE, LIST, etc. This
// example assumes the default "$JS.API" prefix (no JetStream domain) - see the README.
var (
	streamInfo   = subject.MustParse("$JS.API.STREAM.INFO.{stream}")
	consumerInfo = subject.MustParse("$JS.API.CONSUMER.INFO.{stream}.{consumer}")
)

// infoAsset is the grouping key: each stream and each consumer is an independent
// budget. "stream/ORDERS" and "consumer/ORDERS/worker" are distinct buckets by design
// (the per-asset choice) - a client may poll each at the limit, but not any one of them
// faster.
func infoAsset(e *traceassert.Event) (string, bool) {
	if caps, ok := streamInfo.Match(e.Subject); ok {
		s, _ := caps.Str("stream")
		return "stream/" + s, true
	}
	if caps, ok := consumerInfo.Match(e.Subject); ok {
		s, _ := caps.Str("stream")
		c, _ := caps.Str("consumer")
		return "consumer/" + s + "/" + c, true
	}
	return "", false
}

// isInfoRequest is the selector. It is derived from infoAsset, so every selected event
// is keyable: the selector and the grouping cannot drift apart and let traffic escape
// the check (which RespectRateLimit would otherwise flag as Unkeyed).
func isInfoRequest(e *traceassert.Event) bool {
	if e.Dir != traceassert.ToServer {
		return false
	}
	if e.Verb != "PUB" && e.Verb != "HPUB" {
		return false
	}
	_, ok := infoAsset(e)
	return ok
}

// infoLimit is the ceiling: up to five back-to-back, then no faster than one per second.
// It is a ceiling with headroom, not the client's exact target - see the README.
var infoLimit = traceassert.RateLimit{Burst: 5, Every: time.Second}

var _ = Describe("STREAM/CONSUMER.INFO polling rate", func() {
	Context("a compliant client", func() {
		var trace *traceassert.Trace

		BeforeEach(func() {
			trace = MustLoadCapture("compliant.expanded.json")
			// Fail closed: if the capture carried no INFO requests there is nothing to
			// assert, and a green run would be meaningless.
			Expect(trace.Select(isInfoRequest)).NotTo(BeEmpty(),
				"capture contains no STREAM/CONSUMER.INFO requests to rate-check")
		})

		It("stays within burst 5, then 1/s for each stream and consumer independently", func() {
			Expect(trace).To(RespectRateLimit(isInfoRequest, infoAsset, infoLimit))
		})

		It("would exceed the same limit if all assets shared one bucket (By: nil)", func() {
			// The same capture is compliant per asset yet far above 1/s in aggregate.
			// A per-key limit only bounds each key; By: nil bounds total load.
			Expect(trace).NotTo(RespectRateLimit(isInfoRequest, nil, infoLimit))
		})
	})

	Context("a bursty client", func() {
		var trace *traceassert.Trace

		BeforeEach(func() {
			trace = MustLoadCapture("bursty.expanded.json")
			Expect(trace.Select(isInfoRequest)).NotTo(BeEmpty(),
				"capture contains no STREAM/CONSUMER.INFO requests to rate-check")
		})

		It("breaches the per-stream ceiling with a tight poll loop", func() {
			Expect(trace).NotTo(RespectRateLimit(isInfoRequest, infoAsset, infoLimit))
		})

		It("flags the offending stream and leaves well-behaved assets alone", func() {
			report := traceassert.CheckRate(trace.Events, traceassert.RateCheck{
				Select: isInfoRequest,
				By:     infoAsset,
				Limit:  infoLimit,
			})
			Expect(report.Violations).To(HaveLen(1))
			Expect(report.Violations[0].Key).To(Equal("stream/ORDERS"))
			// The sixth poll on ORDERS is the one that found the bucket empty.
			Expect(report.Violations[0].Count).To(Equal(6))
		})
	})
})
