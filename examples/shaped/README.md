# Shaped session example

A worked example of a [`traceassert`](../../) suite over a shaped session: the captures
of one client whose traffic a shaping proxy interfered with, loaded as a `Session` of two
connections and asserted through the client's view.

The client is an ADR-50 fast-ingest publisher (orbit.go's `FastPublisher`) sending a batch
of 45 messages, with the ack frequency at 10 and two ack windows outstanding. The proxy
runs the shaping set in [`gen/shaping.json`](gen/shaping.json), two rules on connections
named `shaped-reconnect`:

| Rule                | Match                                                     | Action       |
|---------------------|-----------------------------------------------------------|--------------|
| `drop-ack-30`       | the server's flow ack (`MSG`, payload `type` `ack`) for batch sequence 30, once | `drop`       |
| `disconnect-pub-35` | the client's `PUB` of batch sequence 35, once             | `disconnect` |

What the two captures hold:

- `testdata/reconnect.expanded.json`, the first connection: the inbox `SUB`, publishes 1 to
  35 and the server's acks for 0, 10, 20 and 30. The ack for 30 is marked
  `{"rule":"drop-ack-30","action":"drop"}` and never reached the client. The publish of 35
  is marked `{"rule":"disconnect-pub-35","action":"disconnect"}`, the proxy closed the
  connection on it, and the capture ends there with a footer.
- `testdata/reconnect-1.expanded.json`, the reconnect: `CONNECT`, the inbox `SUB` again, then
  publishes 36 to 45 that nats.go buffered while disconnected. The server reports 35 as a
  gap on 36, acks 40, and answers the commit on 45 with the pub ack.

The client never pinged for the lost ack: it ran to 40 on the credit of the ack for 20,
stalled there, and the reconnect brought the ack for 40 before its ping interval.

## The suite

`shaped_test.go` loads both captures with `MustLoadSession("reconnect")` and has these specs,
labelled with the rule ids:

| Spec                                         | Asserts                                                                                                      |
|----------------------------------------------|--------------------------------------------------------------------------------------------------------------|
| the rule fired once                          | `Exactly(1, ShapedBy("drop-ack-30"))` over the full session; one ack `Dropped()`, the rest `Delivered()`     |
| the request for 30 is unanswered             | `RequestReply` pairs it with the dropped ack over the session and finds no response over `ClientView()`        |
| the client stayed within its credit          | `RespectCreditWindow` with `PayloadInt("msgs")` as the credit over the client view's events                   |
| the multiplier at one                        | the same matcher fails over the client view at the publish of 31 and passes over the full session, where the dropped ack counts |
| the disconnect ends the first capture        | `Exactly(1, ShapedBy("disconnect-pub-35"))`; the first trace ends with that frame, is not truncated, and `ServerView()` has no publish of 35 |
| the inbox is re-subscribed before publishing | `HaveFirst(BeSub())` over the `SUB` and `PUB` frames with `Conn` 1                                            |
| the sequence resumes                         | `BeContiguousFrom(1, GrammarInt(fiReply, "seq"))` over both connections                                       |
| the server reports the gap                   | the reply to 36 is a `gap` for 35                                                                             |
| capability gate                              | `Skip("capability-absent: no fast ingest")`, which `ta` reports as `skip_reason`                             |

The credit window replays the events of both connections as one flow (`Events()`) rather
than the `*Session`, because the batch resumes on the second connection with the sequence it
left at; over a `*Session` the replay starts afresh on each connection, which fits a client
that starts a new batch after a reconnect.

Run it with a plain `go test ./...` here, or through `ta` from the repository root:

```bash
go run ./cmd/ta run --suite examples/shaped --traces examples/shaped/testdata --report /tmp/shaped.json
```

The summary shows eight specs passed and one skipped; in the CTRF report each entry's `tags`
carry the rule ids and the skipped entry has `extra.skip_reason`. To see a failure name a
frame, delete the `SUB` line from a copy of `reconnect-1.expanded.json` and run `ta` over
that directory: the re-subscribe spec fails with `first event: line 10 to_server PUB
"shaped.msgs"`.

## Regenerating the fixture

The captures come from a real run through the shaping proxy of an
[ntf-server](https://github.com/synadia-labs/ntf-server) instance, driven by `gen/main.go`.
With the management service running on `nats://127.0.0.1:4222`:

```bash
# 1. a JetStream server fronted by a capturing proxy that runs the shaping set
ntf-server admin create server --jetstream --capture --shape gen/shaping.json --json
#    note the instance "id" and the server's "trace_url"

# 2. publish the batch through the proxy and fetch the two captures into testdata/
go run ./gen --instance <id> --proxy <trace_url>

# 3. tear the instance down
ntf-server admin destroy <id>
```

`gen` creates the stream on a connection the set does not match, publishes the batch as
`shaped-reconnect` with a short pause after every message so each ack lands in the capture
before the next publish, and a longer one after 35 so the client notices the close before it
publishes 36. It then waits for the two captures of that connection name on the instance in
the `TRACES` object store and writes them in header timestamp order. `--mgmt` names the
management server when it is not the default, and `--out` the directory.
