# Shaped session example

A shaping proxy dropped one flow ack of a fast-ingest client and closed its connection a few
messages later. The client reconnected and finished the batch, so the run left two captures.
This suite loads them as one [`traceassert`](../../) `Session` and asserts what the client did.

The client is an ADR-50 fast-ingest publisher, orbit.go's `FastPublisher`, sending a batch of 45
messages with an ack frequency of 10 and two ack windows outstanding. The proxy runs the shaping
set in [`gen/shaping.json`](gen/shaping.json), which holds two rules for connections named
`shaped-reconnect`:

| Rule                | Match                                                                          | Action       |
|---------------------|--------------------------------------------------------------------------------|--------------|
| `drop-ack-30`       | the server's flow ack (`MSG`, payload `type` `ack`) for batch sequence 30, once | `drop`       |
| `disconnect-pub-35` | the client's `PUB` of batch sequence 35, once                                   | `disconnect` |

`testdata/reconnect.expanded.json` is the first connection: the inbox `SUB`, publishes 1 to 35,
and the server's acks for 0, 10, 20 and 30. The ack for 30 is marked
`{"rule":"drop-ack-30","action":"drop"}` and never reached the client. The publish of 35 is
marked `{"rule":"disconnect-pub-35","action":"disconnect"}`; the proxy closed the connection on
it, and the capture ends there with a footer.

`testdata/reconnect-1.expanded.json` is the connection the client made next: `CONNECT`, the inbox
`SUB` again, then publishes 36 to 45. The server reports 35 as a gap when 36 arrives, acks 40,
and answers the commit on 45 with the pub ack, which stores 44 of the 45 messages.

Losing the ack for 30 did not stop the client. It published 31 to 40 on the credit of the ack for
20, and the server acked 40 on the new connection, well inside the client's 30 second ack timeout.

## The suite

`shaped_test.go` loads both captures with `MustLoadSession("reconnect")`. Each spec carries its
rule id as a label:

| Spec                                                    | Asserts                                                                                                                                       |
|---------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------|
| fired once, on the ack for sequence 30                   | `Exactly(1, ShapedBy("drop-ack-30"))` over the full session, one ack `Dropped()`, and the acks for 0, 10, 20 and 40 `Delivered()`               |
| left the request for sequence 30 unanswered              | `RequestReply` pairs it with the dropped ack over the session and finds no response over `ClientView()`                                         |
| did not stop the client publishing within its credit     | `RespectCreditWindow`, with `PayloadInt("msgs")` as the credit, over the client view                                                            |
| with the multiplier at one, 31 is past that credit       | the same matcher fails over the client view at the publish of 31 and passes over the full session, where the dropped ack counts                 |
| fired once, on the publish of 35                         | `Exactly(1, ShapedBy("disconnect-pub-35"))`; the first capture ends with that frame, is not truncated, and `ServerView()` has no publish of 35  |
| left the client re-subscribing before it published       | `HaveFirst(BeSub())` over the `SUB` and `PUB` frames with `Conn` 1                                                                              |
| resumed the sequence where the first connection stopped  | `BeContiguousFrom(1, GrammarInt(fiReply, "seq"))` over both connections                                                                         |
| took a publish the server never saw                      | the reply to 36 is a `gap` for 35                                                                                                               |
| needs a capability the client did not declare            | `Skip("capability-absent: no fast ingest")`, which `ta` reports as `skip_reason`                                                                |

The credit window runs over `Events()`, the events of both connections flattened, so the matcher
replays them as one flow. Passing the `*Session` instead starts the replay afresh on each
connection, which suits a client that begins a new batch after a reconnect rather than one that
resumes this batch.

Run the suite with `go test ./...` here, or through `ta` from the repository root:

```bash
go run ./cmd/ta run --suite examples/shaped --traces examples/shaped/testdata --report /tmp/shaped.json
```

Eight specs pass and one skips. In the CTRF report each entry's `tags` carry the rule ids and the
skipped entry has `extra.skip_reason`. To see how a failure names a frame, delete the `SUB` line
from a copy of `reconnect-1.expanded.json` and run `ta` over that directory: the re-subscribe spec
fails with `first event: line 10 to_server PUB "shaped.msgs"`.

## Regenerating the fixture

`gen/main.go` produced the captures against an
[ntf-server](https://github.com/synadia-labs/ntf-server) instance. With the management service
running on `nats://127.0.0.1:4222`:

```bash
# 1. a JetStream server fronted by a capturing proxy that runs the shaping set
ntf-server admin create server --jetstream --capture --shape gen/shaping.json --json
#    note the instance "id" and the server's "trace_url"

# 2. publish the batch through the proxy and fetch the two captures into testdata/
go run ./gen --instance <id> --proxy <trace_url>

# 3. tear the instance down
ntf-server admin destroy <id>
```

`gen` creates the stream on a connection the set does not match, then publishes the batch as
`shaped-reconnect`. It pauses briefly after every message, so each ack lands in the capture before
the next publish, and longer after 35, so the reconnect finishes before the client publishes
again. It then waits for the two captures of that connection name in the `TRACES` object store and
writes them in header timestamp order. `--mgmt` names the management server when it is not the
default, and `--out` the directory to write to.
