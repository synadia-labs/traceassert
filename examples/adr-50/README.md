# ADR-50 fast-ingest example

A worked example of using [`traceassert`](../../) to check a client against the
**fast-ingest** portion of
[ADR-50 — JetStream Batch Publishing](https://github.com/nats-io/nats-architecture-and-design/blob/main/adr/ADR-50.md).

It has two halves:

1. **A small CLI** (`main.go`) that publishes a fast-ingest batch to a stream,
   driven by JSON configuration. You run it through a capturing proxy to record a
   wire trace.
2. **A conformance suite** (`fastbatch_test.go`) that loads that captured trace and
   asserts the client used the protocol correctly, with each spec named for a test
   in
   [`conformance/ADR-50-fast-batch.md`](https://github.com/nats-io/nats-architecture-and-design/blob/main/conformance/ADR-50-fast-batch.md)
   (the `FB-NNN` IDs).

This module has its own `go.mod` and uses a `replace` to build against the
`traceassert` checkout in the parent directory.

## The CLI

### Configuration

The CLI reads JSON from the file named as its first argument, or from stdin. See
[`config.example.json`](config.example.json):

```json
{
  "server": "nats://127.0.0.1:4223",
  "name": "adr50-fast-ingest",
  "stream": "TEST",
  "subjects": ["TEST.>"],
  "messages": 50,
  "create": true,
  "gap": "ok",
  "flow": 100,
  "max_outstanding_acks": 2,
  "payload": "hello"
}
```

| Field                   | Default                 | Meaning                                                                                                                                              |
|-------------------------|-------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| `server`                | `nats://127.0.0.1:4222` | NATS URL — point at the capturing proxy to record a trace                                                                                            |
| `name`                  | `adr50-fast-ingest`     | connection name (lets the proxy isolate this run with `--match-name`)                                                                                |
| `credentials`           | –                       | optional path to a `.creds` file                                                                                                                     |
| `username` / `password` | –                       | optional username/password authentication (used when `username` is set)                                                                              |
| `stream`                | *(required)*            | target stream                                                                                                                                        |
| `subjects`              | –                       | stream subjects, used when `create` is true (wildcards allowed)                                                                                      |
| `publish_subjects`      | derived                 | concrete subjects to publish to, round-robin; when absent, derived from `subjects` by replacing each `*`/`>` token with `x` (so `TEST.>` → `TEST.x`) |
| `messages`              | *(required, ≥1)*        | total messages stored; the last one commits the batch. `1` exercises the single-message immediate-commit path (FB-201)                               |
| `create`                | `false`                 | create/update the stream with `AllowBatchPublish: true` first                                                                                        |
| `gap`                   | `ok`                    | gap mode: `ok` (continue on gaps) or `fail` (abandon on gaps)                                                                                        |
| `flow`                  | `100`                   | requested initial/maximum ack frequency (upper bound)                                                                                                |
| `max_outstanding_acks`  | `2`                     | unacked messages allowed before the client stalls for an ack                                                                                         |
| `ack_timeout`           | js default              | optional Go duration, e.g. `"10s"`                                                                                                                   |
| `payload`               | `msg`                   | base body; each message body is `<payload>-<seq>`                                                                                                    |

### Running it

```bash
go run . config.example.json
# or: echo '{...}' | go run . -
```

It optionally creates the stream, publishes `messages-1` appends followed by a
commit-store, and prints the commit ack (`stream`, `seq`, `batch`, `count`).

## Capturing a trace

The conformance suite is a **passive, offline** analyzer — it never talks to a
server. You feed it a capture produced by the `tracecapture` proxy
(`../../../testing.go/tracecapture`, distributed as a binary), which sits between
the client and a real `nats-server` (2.14.0+ / API level 4).

```bash
# 1. capturing proxy in front of your server
tracecapture proxy \
    --to 127.0.0.1:4222 \
    --listen 127.0.0.1:4223 \
    --capture ./cap \
    --convert \
    --match-name adr50-fast-ingest      # keep only this client's connection

# 2. run the CLI against the proxy (server: nats://127.0.0.1:4223)
go run . config.example.json

# 3. move the converted "expanded" trace into testdata/
cp ./cap/*adr50-fast-ingest*.expanded.json testdata/fastbatch.expanded.json
```

For the single-message case (FB-201), capture a second run with `messages: 1` and
save it as `testdata/single.expanded.json`.

## Running the conformance suite

```bash
go test -v ./...
```

Each spec loads its capture from `testdata/` (override with `ADR50_CAPTURE` /
`ADR50_CAPTURE_SINGLE`). **A missing, unreadable, or truncated capture is a hard
failure**, not a skip — a green run always means real evidence was asserted, never
that the capture was quietly absent. The failure message tells you how to produce
the capture.

The one remaining *skip* is **FB-101**, and only when the capture was taken against
a pre-existing stream (`create:false`) so there is no stream-create to inspect.
Capture with `create:true` to exercise it. (The inconclusive/not-producible
checks — FB-703 flow ramp, FB-902 ping, FB-202 single-EOB — have been removed for
now; see *Not covered* below.)

## What is and isn't covered

The `FB-*` document specifies an *adversarial, server-side* harness. This suite
extracts the subset observable purely from a well-behaved client's wire trace.

### Covered (asserted against the captured trace)

| Test               | Asserted from the trace                                                                                                                      |
|--------------------|----------------------------------------------------------------------------------------------------------------------------------------------|
| Control channel    | old-style inbox subscribed before publishing; all batch traffic is one inbox/batch (ADR-50 §Control Channel — "MUST use an old-style inbox") |
| **FB-101**         | stream create carries `allow_batched: true`; negotiated server `api_lvl ≥ 4`                                                                 |
| **FB-201**         | single commit-store (`op 2`, `seq 1`) → a normal `PubAck`, no preceding flow ack                                                             |
| **FB-301**         | op sequence start→append*→commit; gapless `seq` 1..N; final `PubAck` with `batch` + `count`                                                  |
| **FB-303**         | every `BatchFlowAck` has `msgs ≥ 1`, `seq ≤ sent`, non-decreasing seq                                                                        |
| **FB-401/402/406** | client only emits valid ops, a `gap` in `{ok,fail}`, and the `$FI` terminator                                                                |
| **FB-403/404**     | batch identifier (the inbox nuid) stays within the 64-char limit                                                                             |
| **FB-701/702**     | server flow (`msgs`) starts in `[1, flow]` and never exceeds the requested `flow`                                                            |
| **FB-1401**        | final `PubAck` carries `batch` and the correct `count`                                                                                       |
| **FB-1402**        | the `PubAck` is the only control-channel message without a `type` field                                                                      |

### Not covered (and why)

These require an adversarial or specially-configured **server**, so a conformant
client cannot produce them from a normal run:

- **FB-102/103** (toggling `AllowBatchPublish`, `10205` errors), **FB-104/105**
  (`PersistMode: async`, coexistence with atomic) — need server reconfiguration.
- **FB-106/107, FB-1301/1302** — mirrors/sources topology.
- **FB-202** — a single commit-eob; orbit.go's `FastPublisher.Close` requires ≥1
  prior `Add` (`ErrEmptyBatch`), so a conformant client can't emit it.
- **FB-405** (append to unknown batch), **FB-401/402/404/406 *errors*** — require
  injecting malformed protocol a correct client never sends.
- **FB-500/600** (gap injection), **FB-800** (header-mismatch errors),
  **FB-1000** (idle abandonment), **FB-1100** (limits), **FB-1200** (leader
  change) — require server-side fault injection, timing, scale, or a cluster.
- **FB-703** (flow ramp) and **FB-902** (ping) — only observable under specific
  server load or lost-ack conditions; removed for now rather than reported as
  inconclusive on a clean capture.
- **FB-704** — asserts in-process client ack-tracking, not wire-observable.
