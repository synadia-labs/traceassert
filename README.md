# traceassert

`traceassert` is a generic framework for asserting that a NATS client used
the protocol correctly. It is a **passive, offline analyzer**: it loads a captured
connection trace and lets you write conformance checks by composing a bag of
[Gomega](https://github.com/onsi/gomega) matchers — typically inside
[Ginkgo](https://github.com/onsi/ginkgo) specs, but any Gomega assertion works.

Per-protocol knowledge lives in *data* — a subject grammar, a JSON-schema name, a
correlation key — not in framework code, so the same matchers describe any ADR.

```go
Expect(trace).To(UseOldStyleInbox("_INBOX"))

pubs := trace.Select(func(e *traceassert.Event) bool { return fiReply.Matches(e.Reply) })
Expect(pubs).To(HaveFirst(ReplyCapture(fiReply, "seq", Equal(1))))
Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))

Expect(trace).To(HaveFinalReply(
    DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response",
        HaveField("BatchSize", Equal(5)))))
```

## Contents

- [Install](#install)
- [Input: the expanded trace format](#input-the-expanded-trace-format)
- [Quick start](#quick-start)
- [The event model](#the-event-model)
- [Matchers](#matchers)
  - [Event predicates](#event-predicates)
  - [Subjects & grammars](#subjects--grammars)
  - [Headers, SID & queue](#headers-sid--queue)
  - [Payloads](#payloads)
  - [JetStream payloads](#jetstream-payloads)
  - [Selection & quantifiers](#selection--quantifiers)
  - [Sequences & field extractors](#sequences--field-extractors)
  - [Request/reply & ordering](#requestreply--ordering)
  - [Inbox style](#inbox-style)
  - [Combinators](#combinators)
- [Subject grammars](#subject-grammars)
- [Correlation](#correlation)
- [More examples](#more-examples)

## Install

```bash
go get github.com/synadia-labs/traceassert
```

```go
import (
    "github.com/synadia-labs/traceassert"
    . "github.com/synadia-labs/traceassert/match"   // matchers (dot-import reads best in specs)
    "github.com/synadia-labs/traceassert/subject"   // subject grammars
)
```

## Input: the expanded trace format

`traceassert` reads a **pre-parsed JSON trace** (the *expanded* format): a self-contained
document of fully decoded protocol frames — every `PUB`/`HPUB`/`SUB`/`UNSUB`/`MSG`/`HMSG`,
plus `CONNECT`, `INFO`, `-ERR`, `PING` and `PONG`. Loading needs nothing but the standard
library; there is no protocol parser in this package.

You produce an expanded trace from a captured NATS connection with the companion
`traceassert` CLI (distributed as a binary):

```bash
# stand up a policy-free capturing proxy in front of a NATS server
traceassert proxy --listen 0.0.0.0:4223 --to 127.0.0.1:4222 --capture ./captures

# convert a captured trace to the expanded format
traceassert convert ./captures/client_abc.trace      # -> client_abc.expanded.json

# inspect a capture (either format), with filters
traceassert view ./captures/client_abc.expanded.json --verb PUB --subject 'orders.>'
```

The format is plain JSON, so fixtures are easy to commit, diff, and review.

## Quick start

A Ginkgo suite that asserts against a committed capture (no live server needed):

```go
package fastingest_test

import (
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    "github.com/synadia-labs/traceassert"
    . "github.com/synadia-labs/traceassert/match"
    "github.com/synadia-labs/traceassert/subject"
)

func TestConformance(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "fast-ingest conformance")
}

// The one piece of per-ADR data: how the client encodes its control plane in a subject.
var fiReply = subject.MustParse("{prefix:rest}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

var _ = Describe("fast ingest", func() {
    var trace *traceassert.Trace

    BeforeEach(func() {
        var err error
        trace, err = traceassert.LoadExpanded("testdata/capture.expanded.json")
        Expect(err).NotTo(HaveOccurred())
    })

    It("subscribes a dedicated inbox before publishing", func() {
        Expect(trace).To(UseOldStyleInbox("_INBOX"))
    })

    It("publishes a contiguous, in-order batch", func() {
        pubs := trace.Select(func(e *traceassert.Event) bool { return fiReply.Matches(e.Reply) })
        Expect(pubs).To(HaveFirst(ReplyCapture(fiReply, "op", Equal(0))))
        Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
        Expect(pubs).To(Each(BePub()))
    })
})
```

Prefer plain `go test`? Every matcher is a Gomega matcher:

```go
func TestHandshake(t *testing.T) {
    g := NewWithT(t)
    tr, err := traceassert.LoadExpanded("testdata/capture.expanded.json")
    g.Expect(err).NotTo(HaveOccurred())

    g.Expect(tr).To(ContainInOrder(BeConnect(), BeSub(), BePub()))
    g.Expect(tr).To(ContainEvent(BeConnect().And(ToServer())))
}
```

## The event model

`LoadExpanded(path)` returns a `*Trace`. Every frame is an `Event`:

```go
type Event struct {
    Line    int                 // 1-based line in the source trace
    At      time.Time           // frame timestamp
    ID      string              // tracer-assigned frame id
    Dir     Direction           // ToServer (client→server) or FromServer (server→client)
    Verb    string              // PUB HPUB SUB UNSUB MSG HMSG CONNECT INFO -ERR PING PONG
    Subject string
    Reply   string
    SID     string
    Queue   string
    Header  map[string][]string // HPUB/HMSG headers
    Payload []byte              // body; for CONNECT/INFO the JSON, for -ERR the error text
}
```

`Trace` carries the decoded events plus query helpers:

| Method                                      | Returns                                                                                         |
|---------------------------------------------|-------------------------------------------------------------------------------------------------|
| `Select(p Predicate) []*Event`              | events matching `p`, in order                                                                   |
| `First(p Predicate) (*Event, bool)`         | first match                                                                                     |
| `Count(p Predicate) int`                    | number of matches                                                                               |
| `Truncated() bool`                          | true if the trace ended without a footer (cut short) — use to choose *inconclusive* over *fail* |
| `GroupBy(key KeyFunc) Conversations`        | partition into correlated conversations                                                         |
| `RequestReplies(isReq Predicate) []ReqResp` | pair requests with their responses                                                              |

`Predicate` is `func(*Event) bool`.

## Matchers

Matchers come in two shapes:

- **Event predicates** assert about a single `*Event`.
- **Selection / quantifier matchers** assert about a collection and accept a `*Trace`,
  a `[]*Event`, or a `*Conversation` interchangeably.

Event predicates return `M`, a thin wrapper that adds fluent [combinators](#combinators)
(`.And` / `.Or` / `.Not`) so compositions read naturally: `BePub().And(MatchReply(g))`.
Matchers that take an *inner* matcher (shown as `m`) accept any Gomega matcher, so you can
drop in `Equal`, `BeNumerically`, `ContainSubstring`, `HaveField`, etc.

### Event predicates

| Matcher                                                                       | Matches when the event…                     |
|-------------------------------------------------------------------------------|---------------------------------------------|
| `ToServer()`                                                                  | was sent by the client (client→server)      |
| `FromServer()`                                                                | was delivered by the server (server→client) |
| `BeVerb(verb)`                                                                | has the given protocol verb                 |
| `BePub()` `BeHPub()` `BeSub()` `BeUnsub()` `BeMsg()` `BeHMsg()` `BeConnect()` | is that verb                                |
| `BeRequest()`                                                                 | carries a reply subject                     |
| `HaveNoReply()`                                                               | has no reply subject                        |
| `HaveReply(m)`                                                                | reply subject satisfies `m`                 |

### Subjects & grammars

| Matcher                      | Matches when…                                        |
|------------------------------|------------------------------------------------------|
| `HaveSubject(subj)`          | subject equals `subj` exactly                        |
| `MatchSubject(g)`            | subject conforms to grammar `g`                      |
| `MatchReply(g)`              | reply subject conforms to `g`                        |
| `SubjectToken(i, m)`         | the i-th (0-based) subject token satisfies `m`       |
| `SubjectCapture(g, name, m)` | subject matches `g` and capture `name` satisfies `m` |
| `ReplyCapture(g, name, m)`   | as above, against the reply subject                  |

Numeric captures are compared as ints, so `ReplyCapture(g, "seq", Equal(1))` works.

### Headers, SID & queue

| Matcher                    | Matches when…                               |
|----------------------------|---------------------------------------------|
| `HaveSID(m)`               | the subscription id satisfies `m`           |
| `HaveQueueGroup(m)`        | the (first) queue group satisfies `m`       |
| `HaveHeader(name)`         | header `name` is present (case-insensitive) |
| `HaveNoHeader(name)`       | header `name` is absent                     |
| `HaveHeaderValue(name, m)` | header `name`'s first value satisfies `m`   |

### Payloads

| Matcher                | Matches when…                                                                     |
|------------------------|-----------------------------------------------------------------------------------|
| `HavePayload(m)`       | the raw payload (as a string) satisfies `m`                                       |
| `PayloadIsEmpty()`     | the payload is empty                                                              |
| `PayloadJSON(path, m)` | the [gjson](https://github.com/tidwall/gjson) `path` of the payload satisfies `m` |

JSON numbers arrive as `float64`, so use `BeNumerically("==", n)` for `PayloadJSON` numerics.

### JetStream payloads

Decode and validate JetStream API payloads against the real `nats-io/jsm.go` schemas and
typed Go structs.

| Matcher                                | Matches when…                                                                                        |
|----------------------------------------|------------------------------------------------------------------------------------------------------|
| `BeValidJetStreamRequest()`            | subject is a JS API request and payload is schema-valid for it (type from the subject)               |
| `BeValidJetStreamMessage()`            | payload's embedded `type` names a schema and it is schema-valid (responses, events, advisories)      |
| `BeJetStreamType(schemaType)`          | the derived/detected schema type equals `schemaType`                                                 |
| `DecodeJetStream(inner)`               | decodes to the typed struct (auto-detected) and `inner` matches it                                   |
| `DecodeJetStreamAs(schemaType, inner)` | decodes as the named type (for payloads with no `type` field, e.g. a pub ack) and `inner` matches it |

```go
Expect(req).To(BeValidJetStreamRequest())
Expect(req).To(DecodeJetStream(HaveField("Name", Equal("ORDERS"))))
Expect(ack).To(DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response",
    HaveField("BatchSize", Equal(5))))
```

### Selection & quantifiers

Accept a `*Trace`, `[]*Event`, or `*Conversation`.

| Matcher                    | Matches when…                                                                     |
|----------------------------|-----------------------------------------------------------------------------------|
| `ContainEvent(m)`          | at least one event satisfies `m`                                                  |
| `HaveFirst(m)`             | the first event satisfies `m`                                                     |
| `EndWith(m)`               | the last event satisfies `m`                                                      |
| `Each(m)`                  | every event satisfies `m`                                                         |
| `Exactly(n, m)`            | exactly `n` events satisfy `m`                                                    |
| `AtLeast(n, m)`            | at least `n` events satisfy `m`                                                   |
| `Never(m)`                 | no event satisfies `m`                                                            |
| `ContainInOrder(steps...)` | events contain a (not necessarily adjacent) subsequence matching `steps` in order |

### Sequences & field extractors

Field extractors pull a typed value out of an event; the sequence matchers assert over the
events that carry that field.

| Function              | Returns                                                      |
|-----------------------|--------------------------------------------------------------|
| `GrammarInt(g, name)` | `IntField` — named int capture (tries subject then reply)    |
| `GrammarStr(g, name)` | `StrField` — named string capture (tries subject then reply) |
| `PayloadField(path)`  | `StrField` — gjson `path` of the payload, as a string        |

| Matcher                      | Matches when…                                                                                          |
|------------------------------|--------------------------------------------------------------------------------------------------------|
| `BeContiguousFrom(start, f)` | the field-bearing events form a gapless `start, start+1, …` sequence (in order)                        |
| `BeMonotonic(f)`             | the field-bearing events are strictly increasing (gaps allowed)                                        |
| `SameValue(a, b)`            | two field extractors yield the same value on the event (e.g. a subject capture equals a payload field) |

```go
Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
Expect(req).To(SameValue(GrammarStr(streamCreate, "stream"), PayloadField("name")))
```

### Request/reply & ordering

| Matcher                           | Matches when…                                                                                                 |
|-----------------------------------|---------------------------------------------------------------------------------------------------------------|
| `RequestReply(reqM, respM)`       | every request matching `reqM` (over a `*Trace`) has a server response on its reply subject satisfying `respM` |
| `WaitForReply(resp).Before(next)` | the first event matching `resp` occurs before any event matching `next`                                       |
| `HaveFinalReply(m)`               | the last server→client event satisfies `m`                                                                    |

```go
Expect(trace).To(RequestReply(MatchSubject(streamCreate), BeValidJetStreamMessage()))
Expect(pubs).To(WaitForReply(FromServer().And(PayloadJSON("type", Equal("ack")))).
    Before(ReplyCapture(fiReply, "seq", BeNumerically(">=", 2))))
```

### Inbox style

Precise, mutually-exclusive checks of how the client built its reply inboxes, validated
against real `nats.go` behavior. `prefix` defaults to `_INBOX` when empty.

| Matcher                    | Matches when…                                                                                                                                                   |
|----------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `UseOldStyleInbox(prefix)` | the client subscribed a dedicated `<prefix>.<nuid>` (or `…<nuid>.>`) inbox before publishing under it (`<nuid>` = a real 22-char nats-io/nuid)                  |
| `UseNewStyleInbox(prefix)` | the client used a shared mux subscription `<prefix>.<nuid>.*` with per-request replies `<prefix>.<nuid>.<suffix>` (`<suffix>` = an 8-char nats.go reply suffix) |

### Combinators

Every event predicate (type `M`) composes fluently:

| Method             | Result                 |
|--------------------|------------------------|
| `m.And(others...)` | all must pass          |
| `m.Or(others...)`  | at least one must pass |
| `m.Not()`          | inverts `m`            |

```go
Expect(e).To(BePub().And(MatchReply(fiReply)))
Expect(e).To(BeSub().Or(BeUnsub()))
Expect(e).To(BeConnect().Not())
```

## Subject grammars

A `subject.Grammar` declares a positional subject encoding once, as a one-line string.
From that single declaration you get validation, capture, correlation keys, and field
extractors — no bespoke parsing per ADR.

```go
var fiReply = subject.MustParse(
    "{prefix:rest}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")
```

| Token                          | Meaning                                      |
|--------------------------------|----------------------------------------------|
| `$FI`, `STREAM`, `>` (literal) | matched exactly                              |
| `{name}`                       | one token, any value                         |
| `{name:int}`                   | one token, must parse as an int              |
| `{name:enum(a,b,c)}`           | one token, must be in the set                |
| `{name:rest}`                  | one or more tokens — at most one per grammar |

Matching anchors the fixed tokens from both ends; the single `rest` token absorbs the
slack in the middle. Grammar API:

| Call                                | Returns                                      |
|-------------------------------------|----------------------------------------------|
| `g.Match(subject)`                  | `(Captures, bool)` — bindings if it conforms |
| `g.Matches(subject)`                | `bool`                                       |
| `g.Int(name)` / `g.Str(name)`       | an extractor `func(subject) (T, bool)`       |
| `caps.Int(name)` / `caps.Str(name)` | a captured value, typed                      |

## Correlation

`GroupBy(key)` partitions a trace into `Conversation`s in first-seen key order; events for
which the key reports `ok=false` are dropped.

| KeyFunc              | Groups by                                           |
|----------------------|-----------------------------------------------------|
| `ByReply()`          | the full reply subject                              |
| `ByHeader(name)`     | a header value (case-insensitive)                   |
| `BySubjectToken(i)`  | the i-th (0-based) subject token                    |
| `ByCapture(g, name)` | a named grammar capture (tries subject, then reply) |

```go
batches := trace.GroupBy(ByCapture(fiReply, "uuid"))   // Conversations
if b, ok := batches.Get("batch-1"); ok {
    Expect(b.ToServer()).To(HaveLen(5))                // its publishes
    Expect(b).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
}
```

A `Conversation` exposes `Key`, `Events`, and `ToServer()` / `FromServer()` slices.
`Conversations` offers `Get(key)` and `One()` (the single conversation, or `ok=false`).

`RequestReplies(isReq)` does exact request/response pairing — for each `ToServer` event
that matches `isReq` and carries a reply, it finds the first later `FromServer` event
delivered to that reply subject — returning `[]ReqResp{ Request, Response }` (`Response` is
`nil` when unanswered). No inbox heuristics.

## More examples

**Handshake & API level (INFO carries the server JSON):**

```go
info, ok := trace.First(func(e *traceassert.Event) bool { return e.Verb == "INFO" })
Expect(ok).To(BeTrue())
Expect(info).To(FromServer())
Expect(info).To(PayloadJSON("api_lvl", BeNumerically(">=", 4)))
```

**JetStream request → response shape:**

```go
streamCreate := subject.MustParse("$JS.API.STREAM.CREATE.{stream}")

Expect(trace).To(ContainEvent(
    MatchSubject(streamCreate).And(BeValidJetStreamRequest())))

Expect(trace).To(RequestReply(
    MatchSubject(streamCreate),
    BeJetStreamType("io.nats.jetstream.api.v1.stream_create_response")))
```

**Inconclusive vs fail on a truncated capture:**

```go
if trace.Truncated() {
    Skip("trace was cut short (MaxSize/MaxTime) — required evidence is absent")
}
Expect(trace).To(HaveFinalReply(BeValidJetStreamMessage()))
```
