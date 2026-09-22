# traceassert

`traceassert` is a generic framework for asserting that a NATS client used
the protocol correctly. It is a **passive, offline analyzer**: it loads a captured
connection trace and lets you write conformance checks by composing
[Gomega](https://github.com/onsi/gomega) matchers, typically inside
[Ginkgo](https://github.com/onsi/ginkgo) specs, though any Gomega assertion works.

Per-protocol knowledge is data, a subject grammar, a JSON-schema name or a correlation
key, rather than framework code, so the same matchers describe any ADR.

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
  - [Rate limiting](#rate-limiting)
  - [Flow control](#flow-control)
  - [Combinators](#combinators)
- [Subject grammars](#subject-grammars)
- [Correlation](#correlation)
- [The ta runner](#the-ta-runner)
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

`traceassert` reads a **pre-parsed trace**, the *expanded* format: a
[JSON Lines](https://jsonlines.org) document of decoded protocol frames, a header
line, then one line per frame (every `PUB`/`HPUB`/`SUB`/`UNSUB`/`MSG`/`HMSG`, plus
`CONNECT`, `INFO`, `-ERR`, `PING` and `PONG`), then a footer line. Loading needs nothing
but the standard library; there is no protocol parser in this package.

Traces can be made using the [NATS Testing Framework](https://hub.docker.com/r/synadia/ntf-server) tool.

Because it is line-oriented, the format **streams on both ends**: producing or reading a
trace never holds more than a single frame in memory, so captures of any size are handled.
`LoadExpanded` still returns a fully materialized `*Trace` (the matchers query it
repeatedly); to consume an arbitrarily large trace one frame at a time instead, use
`ScanExpanded`. It is plain JSON, one object per line, so fixtures stay easy to commit,
diff, and review.

An event line carries a `shaped` key when a traffic-shaping proxy acted on that frame:

```json
{"line":7,"at":"2026-01-01T00:00:00Z","dir":"from_server","verb":"MSG","subject":"_INBOX.batch1.ack","sid":"1","payload":"...","bytes":52,"shaped":{"rule":"drop-ack-30","action":"drop"}}
```

`rule` is the id of the shaping rule that fired and `action` is the string the proxy wrote,
such as `drop`, `stall`, `throttle` or `disconnect`. The key is absent on every frame the proxy left
alone, and the format version stays 2: a reader ignores keys it does not know, so a capture
written before the key existed loads with no frame shaped.

### Loading a capture

Suites rarely call `LoadExpanded` directly. These helpers resolve a fixture by name and
fail loudly on a missing, unreadable, or **truncated** capture, so a green run always means
real evidence was asserted:

| Helper                                          | Use                                                                                                |
|-------------------------------------------------|----------------------------------------------------------------------------------------------------|
| `match.MustLoadCapture(file)`                   | Ginkgo one-liner: loads `file`, or fails the spec (a clean failure, never a panic). Returns `*Trace` |
| `traceassert.LoadCapture(file)`                 | the same, returning `(*Trace, error)`, for plain `go test` with a local Gomega                     |
| `match.MustLoadSession(name)`                   | Ginkgo one-liner: loads every capture of scenario `name`, or fails the spec. Returns `*Session`    |
| `traceassert.LoadSession(name)`                 | the same, returning `(*Session, error)`                                                            |
| `traceassert.CapturePath(file)`                 | just the path resolution, no load                                                                   |
| `traceassert.TraceDirEnv` (`"TRACE_DIR"`)       | the env var the [`ta`](cmd/ta) runner sets to the capture directory                                 |

The path is resolved as `$TRACE_DIR/<file>` (the directory `ta` exports to the suite) when
`TRACE_DIR` is set, otherwise `testdata/<file>` for a plain `go test`. Once `TRACE_DIR` is
set the `testdata/` fallback is deliberately not used, so a run against a supplied directory
can never silently assert a committed fixture instead.

#### Sessions: one capture per connection

A scenario in which the client reconnects produces one capture per connection. The harness
writes them as `<name>.expanded.json` and `<name>-<n>.expanded.json` (`<n>` a run of
digits), and `LoadSession(name)` reads every one of them from the capture directory into a
`Session`:

```go
type Session struct {
    Traces []*Trace // one per connection, ordered by header timestamp
}
```

The files are matched on the name, not a glob, so `fast` does not sweep in the sibling
scenario `fast-lostack`. The loader takes a pattern rather than a fixed list because how many
connections the client makes is under test: a client that reconnected once more than expected
leaves a capture the suite must read. No matching file is an error, and the loader refuses a
truncated capture as `LoadCapture` does: the proxy writes a footer on every close, an induced
disconnect included, so a missing footer means the capture was cut short, not that the client
reconnected.

A `Session` offers `Events()` (the traces' events concatenated), `Select`, `First` and
`Count` with the same signatures as `Trace`, and `RequestReplies`, which pairs **within each
trace** and concatenates the pairs. Merged into one trace, a reply arriving on connection 2
would pair with a request that died with connection 1, the fault a reconnect scenario exists
to catch. Every event of a session carries `Conn`, the index of its trace, so a predicate can
say which connection a frame belongs to. Every collection matcher accepts a `*Session`, and so
does `RequestReply`.

#### Views: what each side saw

A shaping proxy records a frame it dropped in the capture, but the side it was headed for
never received it. `ClientView()` on a `Trace` returns a `*Trace` without the dropped
`from_server` frames; `ServerView()` returns one without the dropped `to_server` frames. A
frame is dropped when its `Shaped.Action` is `drop` or `disconnect` (`Event.Dropped()`); a
stalled or throttled frame was delivered and stays in both views. On a `Session` each returns
a `*Session` of the per-trace views. A view shares the header, footer and event pointers with
the capture, so `Line`, `ID` and `Conn` are unchanged and a failure still names the frame in
the file. It is the same type, so every matcher, `RequestReply` included, runs over it
unchanged; a lost ack read through the client view is a request with no response, as the
client experienced it.

`RequestReplies` skips a request the proxy dropped, since the server never saw it and owes no
reply. A dropped reply pairs over the full trace, where `Delivered()` can be asserted on it,
and is absent from the client view, where its request is unanswered.

Assert that the rule fired over the full capture before asserting the client's reaction over
a view. A rule that never fired is a scenario error, and the dropped frame is absent from the
client view, where `Exactly(1, ...)` would count zero:

```go
trace := MustLoadCapture("fast-lostack.expanded.json")
Expect(trace).To(Exactly(1, ShapedBy("drop-ack-30")))
Expect(trace.ClientView()).To(AtLeast(2, MatchSubject(publish))) // the client resent after the lost ack
```

The [shaped example](examples/shaped) is the worked example for shaped sessions. A shaping proxy
dropped the flow ack for sequence 30 and closed the connection on the publish of sequence 35, and
the run left two captures of one fast-ingest client. The suite loads them as a session and checks
that each rule fired before asserting the client's reaction over the client view. Under `ta` the
rule ids become labels and the skip reason reaches the CTRF report.

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
        // MustLoadCapture resolves the path from $TRACE_DIR (set by the `ta` runner) or
        // testdata/, loads it, and fails the spec if it is missing, unreadable, or truncated.
        trace = MustLoadCapture("capture.expanded.json")
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

Prefer plain `go test`? Every matcher is a Gomega matcher. Use `traceassert.LoadCapture`
(the error-returning loader behind `MustLoadCapture`) with a local Gomega:

```go
func TestHandshake(t *testing.T) {
    g := NewWithT(t)
    tr, err := traceassert.LoadCapture("capture.expanded.json")
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
    Dir     Direction           // ToServer (client to server) or FromServer (server to client)
    Verb    string              // PUB HPUB SUB UNSUB MSG HMSG CONNECT INFO -ERR PING PONG
    Subject string
    Reply   string
    SID     string
    Queue   string
    Header  map[string][]string // HPUB/HMSG headers
    Payload []byte              // body; for CONNECT/INFO the JSON, for -ERR the error text
    WireBytes int               // size of the raw on-the-wire frame (verb+subject+headers+framing), 0 if unknown
    Shaped  *Shaped             // set when a traffic-shaping proxy acted on the frame, nil otherwise
    Conn    int                 // index of the event's trace in its Session; 0 on a trace loaded on its own
}

type Shaped struct {
    Rule   string // id of the shaping rule that fired, e.g. "drop-ack-30"
    Action string // what the proxy did, as the proxy wrote it: "drop", "stall", "throttle", "disconnect"
}
```

`Shaped` is nil on every frame the proxy left alone, including every frame of a capture
taken without a shaping proxy. `Action` is the proxy's string, not a constant this package
defines.

`Trace` carries the decoded events plus query helpers:

| Method                                      | Returns                                                                                         |
|---------------------------------------------|-------------------------------------------------------------------------------------------------|
| `Select(p Predicate) []*Event`              | events matching `p`, in order                                                                   |
| `First(p Predicate) (*Event, bool)`         | first match                                                                                     |
| `Count(p Predicate) int`                    | number of matches                                                                               |
| `Truncated() bool`                          | true if the trace ended without a footer; use it to choose *inconclusive* over *fail*           |
| `GroupBy(key KeyFunc) Conversations`        | partition into correlated conversations                                                         |
| `RequestReplies(isReq Predicate) []ReqResp` | pair requests with their responses; a request the proxy dropped is skipped                     |
| `ClientView() *Trace` `ServerView() *Trace` | the trace as [that side saw it](#views-what-each-side-saw)                                      |

`Predicate` is `func(*Event) bool`.

A [`Session`](#sessions-one-capture-per-connection) (one `Trace` per connection) offers
`Events()`, `Select`, `First`, `Count`, `RequestReplies` (pairing within each trace),
`ClientView()` and `ServerView()`.

## Matchers

Matchers come in two shapes:

- **Event predicates** assert about a single `*Event`.
- **Selection / quantifier matchers** assert about a collection and accept a `*Trace`,
  a `*Session`, a `[]*Event`, or a `*Conversation` interchangeably.

Event predicates return `M`, a thin wrapper that adds fluent [combinators](#combinators)
(`.And` / `.Or` / `.Not`) so compositions read naturally: `BePub().And(MatchReply(g))`.
Matchers that take an *inner* matcher (shown as `m`) accept any Gomega matcher, so you can
drop in `Equal`, `BeNumerically`, `ContainSubstring`, `HaveField`, etc.

### Event predicates

| Matcher                                                                       | Matches when the event...                     |
|-------------------------------------------------------------------------------|---------------------------------------------|
| `ToServer()`                                                                  | was sent by the client                      |
| `FromServer()`                                                                | was sent by the server                      |
| `BeVerb(verb)`                                                                | has the given protocol verb                 |
| `BePub()` `BeHPub()` `BeSub()` `BeUnsub()` `BeMsg()` `BeHMsg()` `BeConnect()` | is that verb                                |
| `BeRequest()`                                                                 | carries a reply subject                     |
| `HaveNoReply()`                                                               | has no reply subject                        |
| `HaveReply(m)`                                                                | reply subject satisfies `m`                 |
| `Dropped()`                                                                   | the proxy never forwarded it: `Shaped` is set with action `drop` or `disconnect` |
| `Delivered()`                                                                 | the negation of `Dropped()`; a stalled or throttled frame is delivered |
| `ShapedBy(rule)`                                                              | `Shaped.Rule` is `rule`, whatever the action |

### Subjects & grammars

| Matcher                      | Matches when...                                        |
|------------------------------|------------------------------------------------------|
| `HaveSubject(subj)`          | subject equals `subj` exactly                        |
| `MatchSubject(g)`            | subject conforms to grammar `g`                      |
| `MatchReply(g)`              | reply subject conforms to `g`                        |
| `SubjectToken(i, m)`         | the i-th (0-based) subject token satisfies `m`       |
| `SubjectCapture(g, name, m)` | subject matches `g` and capture `name` satisfies `m` |
| `ReplyCapture(g, name, m)`   | as above, against the reply subject                  |

Numeric captures are compared as ints, so `ReplyCapture(g, "seq", Equal(1))` works.

### Headers, SID & queue

| Matcher                    | Matches when...                               |
|----------------------------|---------------------------------------------|
| `HaveSID(m)`               | the subscription id satisfies `m`           |
| `HaveQueueGroup(m)`        | the (first) queue group satisfies `m`       |
| `HaveHeader(name)`         | header `name` is present (case-insensitive) |
| `HaveNoHeader(name)`       | header `name` is absent                     |
| `HaveHeaderValue(name, m)` | header `name`'s first value satisfies `m`   |

### Payloads

| Matcher                | Matches when...                                                                     |
|------------------------|-----------------------------------------------------------------------------------|
| `HavePayload(m)`       | the raw payload (as a string) satisfies `m`                                       |
| `PayloadIsEmpty()`     | the payload is empty                                                              |
| `PayloadJSON(path, m)` | the [gjson](https://github.com/tidwall/gjson) `path` of the payload satisfies `m` |

JSON numbers arrive as `float64`, so use `BeNumerically("==", n)` for `PayloadJSON` numerics.

### JetStream payloads

Decode and validate JetStream API payloads against the real `nats-io/jsm.go` schemas and
typed Go structs.

| Matcher                                | Matches when...                                                                                        |
|----------------------------------------|------------------------------------------------------------------------------------------------------|
| `BeValidJetStreamRequest()`            | subject is a JS API request and payload is schema-valid for it (type from the subject)               |
| `BeValidJetStreamMessage()`            | payload's embedded `type` names a schema and it is schema-valid (responses, events, advisories)      |
| `BeJetStreamType(schemaType)`          | the derived/detected schema type equals `schemaType`                                                 |
| `DecodeJetStream(inner)`               | decodes to the typed struct (auto-detected) and `inner` matches it                                   |
| `DecodeJetStreamAs(schemaType, inner)` | decodes as the named type (for payloads with no `type` field, e.g. a pub ack) and `inner` matches it |
| `HaveAPILevel(m)`                      | a stream/consumer create or info response reports a hosted API level (`_nats.level`) satisfying `m`  |

```go
Expect(req).To(BeValidJetStreamRequest())
Expect(req).To(DecodeJetStream(HaveField("Name", Equal("ORDERS"))))
Expect(ack).To(DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response",
    HaveField("BatchSize", Equal(5))))
Expect(reply).To(HaveAPILevel(BeNumerically(">=", 4))) // stream/consumer hosted at level >= 4
```

### Selection & quantifiers

Accept a `*Trace`, `*Session`, `[]*Event`, or `*Conversation`.

| Matcher                    | Matches when...                                                                     |
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
| `GrammarInt(g, name)` | `IntField` of the named int capture, tried on the subject then the reply    |
| `GrammarStr(g, name)` | `StrField` of the named string capture, tried on the subject then the reply |
| `PayloadField(path)`  | `StrField` of the gjson `path` of the payload, as a string                  |
| `PayloadInt(path)`    | `IntField` of the gjson `path` of the payload, a JSON number or numeric string |
| `HeaderField(name)`   | `StrField` of the first value of header `name`, matched case-insensitively     |
| `HeaderInt(name)`     | `IntField` of the first value of header `name`; absent or non-numeric reports `ok=false` |

| Matcher                      | Matches when...                                                                                          |
|------------------------------|--------------------------------------------------------------------------------------------------------|
| `BeContiguousFrom(start, f)` | the field-bearing events form a gapless `start, start+1, ...` sequence (in order)                      |
| `BeMonotonic(f)`             | the field-bearing events are strictly increasing (gaps allowed)                                        |
| `SameValue(a, b)`            | two field extractors yield the same value on the event (e.g. a subject capture equals a payload field) |

```go
Expect(pubs).To(BeContiguousFrom(1, GrammarInt(fiReply, "seq")))
Expect(req).To(SameValue(GrammarStr(streamCreate, "stream"), PayloadField("name")))

// an ADR-50 atomic batch carries its control plane in headers: the publishes of
// one batch number 1..n, the last one commits, and the pub ack names the batch.
batch, ok := trace.GroupBy(ByHeader("Nats-Batch-Id")).Get("b1")
Expect(ok).To(BeTrue())
Expect(batch).To(BeContiguousFrom(1, HeaderInt("Nats-Batch-Sequence")))
Expect(batch.ToServer()).To(EndWith(HaveHeaderValue("Nats-Batch-Commit", Equal("1"))))
isCommit := func(e *traceassert.Event) bool {
    _, ok := e.HeaderGet("Nats-Batch-Commit")
    return ok
}
for _, p := range trace.RequestReplies(isCommit) {
    id, _ := HeaderField("Nats-Batch-Id")(p.Request)
    Expect(p.Response).To(PayloadJSON("batch", Equal(id)))
}
```

### Request/reply & ordering

| Matcher                           | Matches when...                                                                                                 |
|-----------------------------------|---------------------------------------------------------------------------------------------------------------|
| `RequestReply(reqM, respM)`       | every request matching `reqM` (over a `*Trace` or, per connection, a `*Session`) has a server response on its reply subject satisfying `respM` |
| `WaitForReply(resp).Before(next)` | the first event matching `resp` occurs before any event matching `next`                                       |
| `HaveFinalReply(m)`               | the last `FromServer` event satisfies `m`                                                                     |

```go
Expect(trace).To(RequestReply(MatchSubject(streamCreate), BeValidJetStreamMessage()))
Expect(pubs).To(WaitForReply(FromServer().And(PayloadJSON("type", Equal("ack")))).
    Before(ReplyCapture(fiReply, "seq", BeNumerically(">=", 2))))
```

### Inbox style

Precise, mutually-exclusive checks of how the client built its reply inboxes, validated
against real `nats.go` behavior. `prefix` defaults to `_INBOX` when empty.

| Matcher                    | Matches when...                                                                                                                                                   |
|----------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `UseOldStyleInbox(prefix)` | the client subscribed a dedicated `<prefix>.<nuid>` (or `<prefix>.<nuid>.>`) inbox before publishing under it (`<nuid>` = a real 22-char nats-io/nuid)          |
| `UseNewStyleInbox(prefix)` | the client used a shared mux subscription `<prefix>.<nuid>.*` with per-request replies `<prefix>.<nuid>.<suffix>` (`<suffix>` = an 8-char nats.go reply suffix) |

### Rate limiting

Assert that a class of repeating events stayed within a
[token bucket](https://en.wikipedia.org/wiki/Token_bucket), a short burst then a
sustained rate, with each grouping key tracked as its own budget. The rate is computed
from the **timestamps the trace recorded**, never wall-clock time, so the same capture
always gives the same result.

| Matcher / function                              | Asserts / returns                                                                                      |
|-------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| `RespectRateLimit(sel, by, limit)`              | (matcher) every event `sel` selects, grouped into independent buckets by `by`, stays within `limit`    |
| `traceassert.CheckRate(events, RateCheck{...})` | (core) the same analysis as a `RateReport`, the violations plus match counts, for precise assertions   |

`limit` is a `traceassert.RateLimit{Burst, Every}`: up to `Burst` events back-to-back (the
bucket starts full), then one more every `Every`. `sel` is a `Predicate` (nil = all
events); `by` is a `KeyFunc` (nil = a single global bucket), and the prebuilt keyers from
[Correlation](#correlation) (`ByReply`, `BySubjectToken`, `ByCapture`, ...) serve as one.

It checks a ceiling, not an average: set the limit above the client's intended rate with
headroom, or ordinary timestamp jitter trips it. It **fails closed**: when nothing matches
`sel`, or a selected event cannot be keyed by `by`, the assertion errors rather than passing
vacuously, so it fails under both `To` and `NotTo`.

```go
// No stream or consumer's INFO is polled faster than 5 back-to-back, then 1/sec.
Expect(trace).To(RespectRateLimit(isInfoRequest, infoAsset,
    traceassert.RateLimit{Burst: 5, Every: time.Second}))

// A nil grouping limits aggregate load instead; with NotTo, assert that some bucket exceeded it.
Expect(trace).NotTo(RespectRateLimit(isInfoRequest, nil,
    traceassert.RateLimit{Burst: 5, Every: time.Second}))

// CheckRate exposes the violations as data for a precise assertion.
report := traceassert.CheckRate(trace.Events, traceassert.RateCheck{
    Select: isInfoRequest, By: infoAsset,
    Limit:  traceassert.RateLimit{Burst: 5, Every: time.Second},
})
Expect(report.Violations[0].Key).To(Equal("stream/ORDERS"))
```

The [info_requests example](examples/info_requests) is a full walk-through, with common
patterns (requests per inbox, publishes per subject, all JS API calls combined, ...) and
gotchas.

### Flow control

Assert that the client never sent further ahead of the last acknowledgment than the
server's credit allowed. The matcher replays the events in trace order: an ack sets the
last acknowledged sequence and the window, and a send fails the match when its sequence
minus the last acknowledged sequence exceeds the window in force.

| Matcher                                                                          | Asserts                                                                                     |
|----------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------|
| `RespectCreditWindow(sends, acks, sendSeq, ackSeq, credit, initial, multiplier)` | on every `sends` event, `sendSeq` minus the last `ackSeq` is at most `credit * multiplier`; before the first ack the window is `initial` |

`sends` and `acks` are `Predicate`s; `sendSeq`, `ackSeq` and `credit` are `IntField`s
read from the events they match. Over a `*Session` the replay starts afresh on each
connection, so a window granted before a reconnect does not carry onto the next one. A
field that cannot be read from a matched event is an error rather than a skipped event, so
a grammar that stopped matching fails the spec rather than passing it.

Run it over the [client view](#views-what-each-side-saw), where an ack the proxy dropped
grants no credit, so a client that kept publishing past a lost ack fails. ADR-50's
`sent_seq - last_ack_seq <= msgs * outstanding` with the mandated initial window of one
is:

```go
Expect(trace.ClientView()).To(RespectCreditWindow(isBatchPub, isFlowAck,
    GrammarInt(fiReply, "seq"), PayloadInt("seq"), PayloadInt("msgs"), 1, outstanding))
```

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
One declaration gives validation, capture, correlation keys and field extractors, with no
bespoke parsing per ADR.

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
| `{name:rest}`                  | one or more tokens; at most one per grammar  |

Matching anchors the fixed tokens from both ends; the single `rest` token absorbs the
slack in the middle. Grammar API:

| Call                                | Returns                                      |
|-------------------------------------|----------------------------------------------|
| `g.Match(subject)`                  | `(Captures, bool)`, the bindings when it conforms |
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

`RequestReplies(isReq)` pairs requests with responses exactly: for each `ToServer` event
that matches `isReq` and carries a reply, it finds the first later `FromServer` event
delivered to that reply subject, and returns `[]ReqResp{ Request, Response }` with
`Response` nil when unanswered. It uses no inbox heuristics.

## The ta runner

`ta run` compiles and runs the Ginkgo suite in `--suite` with `go test`, one package at a
time, exports `--traces` to it as `TRACE_DIR`, prints a summary and exits 0 on a pass, 1 on
a failing spec and 2 when the suite could not run. `--report FILE` (or `--report=-` for
stdout, which then carries the JSON and no summary) writes the run as a
[CTRF](https://ctrf.io) document: one entry per spec under
`results.tests`, with `tags` carrying the Ginkgo labels, `rawStatus` the Ginkgo state before
it is mapped to the CTRF `status`, and `extra.skip_reason` the `Skip` message. The run's own
fields (`suite_dir`, `traces_dir`, `success`, and one `packages` entry per test package with
its `error` when it could not run) sit under `results.extra`.

```json
{
  "name": "ingest needs fast ingest",
  "status": "skipped",
  "duration": 0,
  "suite": ["ingest"],
  "filePath": "/suites/adr-50/ingest_test.go",
  "line": 15,
  "tags": ["ADR50", "ADR50-C-301"],
  "rawStatus": "skipped",
  "extra": { "skip_reason": "capability-absent: no fast ingest" }
}
```

## More examples

**Handshake & API level (INFO carries the server JSON):**

```go
info, ok := trace.First(func(e *traceassert.Event) bool { return e.Verb == "INFO" })
Expect(ok).To(BeTrue())
Expect(info).To(FromServer())
Expect(info).To(PayloadJSON("api_lvl", BeNumerically(">=", 4)))
```

**JetStream request and response shape:**

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
    Skip("trace was cut short (MaxSize/MaxTime), required evidence is absent")
}
Expect(trace).To(HaveFinalReply(BeValidJetStreamMessage()))
```
