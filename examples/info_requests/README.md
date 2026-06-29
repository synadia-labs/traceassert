# INFO request rate-limiting example

A worked example of using [`traceassert`](../../) to assert that a NATS client did not
hammer a server's JetStream INFO endpoints — that it polled each stream's and consumer's
`STREAM.INFO` / `CONSUMER.INFO` no faster than a short burst followed by a sustained
rate.

It is built on traceassert's **generic** rate-limit assertion, `match.RespectRateLimit`.
The only JetStream-specific code in the suite is the selector and the grouping key; the
token-bucket machinery is the library's, and the same matcher rate-limits publishes,
inbox requests, or any repeating event (see [Common patterns](#common-patterns)).

This module has its own `go.mod` and uses a `replace` to build against the `traceassert`
checkout in the parent directory.

## The rule: a token-bucket ceiling

`RateLimit{Burst, Every}` is a [token bucket](https://en.wikipedia.org/wiki/Token_bucket):

- the bucket holds up to `Burst` tokens and starts full;
- each selected event spends one token;
- one token is added back every `Every`.

An event that arrives when the bucket is empty is a violation. So `{Burst: 5, Every:
time.Second}` permits **five back-to-back, then one per second** — exactly "a short
burst, then a sustained rate".

The budget is tracked **per key** (here, per asset), so each stream and each consumer
gets its own independent bucket.

### It is a ceiling, not an average

`RespectRateLimit` replays the timestamps the trace actually recorded through the bucket;
it answers *"did the client ever exceed this ceiling?"*, **not** *"was the client's
average rate X?"*. It applies no jitter tolerance, so set the limit **above** the
client's intended rate, with headroom — a limit pinned to the client's exact target will
flake on ordinary scheduling and capture-timestamp jitter.

| The client intends         | Don't set                  | Set (with headroom)        | Why                                                                                                              |
|----------------------------|----------------------------|----------------------------|------------------------------------------------------------------------------------------------------------------|
| ~1 INFO/sec per stream     | `{Burst: 1, Every: 1s}`    | `{Burst: 5, Every: 1s}`    | an exact-target bucket trips on jitter; a burst of 5 absorbs normal bunching while still catching a runaway loop |
| ~10 pulls/sec per consumer | `{Burst: 1, Every: 100ms}` | `{Burst: 15, Every: 80ms}` | the ceiling catches a 100/sec spin loop without flagging normal bursts                                           |

A passing test means the client never breached the ceiling; it does not certify the
client's exact rate. Choose the loosest limit that still fails the behavior you are
guarding against — then capture a bad run and confirm it fails (this example ships such a
capture, `bursty.expanded.json`).

## The fixtures

Two hand-written captures under `testdata/` carry the whole lesson; you can run the
suite with no server.

### `compliant.expanded.json`

Stream `ORDERS` opens with a burst of five, then settles to roughly one per second,
interleaved with an independent consumer poller and an untouched `EVENTS` stream. Walking
the `stream/ORDERS` bucket at `{Burst: 5, Every: 1s}`:

| t (s) | tokens before | tokens after |
|------:|--------------:|-------------:|
| 0.000 |          5.00 |         4.00 |
| 0.100 |          4.10 |         3.10 |
| 0.200 |          3.20 |         2.20 |
| 0.300 |          2.30 |         1.30 |
| 0.400 |          1.40 |         0.40 |
| 1.450 |          1.45 |         0.45 |
| 2.500 |          1.50 |         0.50 |
| 3.550 |          1.55 |         0.55 |

The bucket never drops below one token at a request (the tightest point is 1.40), so the
stream is within budget — and because each asset has its own bucket, the consumer poller
running concurrently does not count against it. The aggregate rate across all assets is
far above 1/sec, which the suite uses to demonstrate the per-key/aggregate distinction
(see [Gotchas](#gotchas)).

### `bursty.expanded.json`

Stream `ORDERS` is polled six times inside half a second:

| t (s) | tokens before |  tokens after |
|------:|--------------:|--------------:|
| 0.000 |          5.00 |          4.00 |
| 0.100 |          4.10 |          3.10 |
| 0.200 |          3.20 |          2.20 |
| 0.300 |          2.30 |          1.30 |
| 0.400 |          1.40 |          0.40 |
| 0.500 |          0.50 | **violation** |

The sixth poll finds 0.50 tokens — the bucket is empty — so it is a violation. A
well-behaved `EVENTS` stream in the same capture stays within budget, and the suite
asserts the violation is reported on `stream/ORDERS` alone.

## The assertion

The suite ([`info_requests_test.go`](info_requests_test.go)) is the whole application of
the generic matcher:

```go
// Two fixed-arity grammars, so a match is exact.
var (
	streamInfo   = subject.MustParse("$JS.API.STREAM.INFO.{stream}")
	consumerInfo = subject.MustParse("$JS.API.CONSUMER.INFO.{stream}.{consumer}")
)

// The grouping key: one independent bucket per stream and per consumer.
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

// The selector, derived from infoAsset so the two cannot drift apart.
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

Expect(trace).To(RespectRateLimit(isInfoRequest, infoAsset,
	traceassert.RateLimit{Burst: 5, Every: time.Second}))
```

When it fails, the message names the offending bucket, the line and time, how early the
request arrived, and the rule in words:

```
expected events to respect rate limit (burst 5, then 1 every 1s) per key
  key "stream/ORDERS": line 10 to_server PUB "$JS.API.STREAM.INFO.ORDERS" at 12:00:00.500
  arrived 500ms early (request 6 for this key)
```

## Common patterns

`RespectRateLimit` is generic: retarget it by changing the selector and the grouping
key. The grouping vocabulary (`ByReply`, `BySubjectToken`, `ByCapture`, …) lives in the
core `traceassert` package, and `By: nil` puts everything in one global bucket.

| Intent                              | `Select`                                  | `By`                                         | Example `Limit`                              |
|-------------------------------------|-------------------------------------------|----------------------------------------------|----------------------------------------------|
| INFO polls per asset (this example) | `isInfoRequest`                           | `infoAsset`                                  | `{Burst: 5, Every: time.Second}`             |
| Requests per reply inbox            | `(*traceassert.Event).IsRequest`          | `traceassert.ByReply()`                      | `{Burst: 10, Every: 100 * time.Millisecond}` |
| Publishes per subject               | a `PUB`/`HPUB` predicate                  | `traceassert.BySubjectToken(0)`              | `{Burst: 50, Every: 20 * time.Millisecond}`  |
| All JetStream API calls, combined   | a `$JS.API.>` predicate                   | `nil` (one global bucket)                    | `{Burst: 100, Every: 10 * time.Millisecond}` |
| Pull / next-msg per consumer        | a `$JS.API.CONSUMER.MSG.NEXT.>` predicate | `traceassert.ByCapture(grammar, "consumer")` | `{Burst: 15, Every: 80 * time.Millisecond}`  |

**Not expressible: reconnect-storm rate.** A trace is a single connection, so
cross-connection rates (reconnect frequency, new connections per second) have nothing to
group across — that belongs to a different tool.

## Gotchas

- **Empty selection fails closed.** If nothing matches `Select`, `RespectRateLimit`
  errors ("nothing was rate-checked") rather than passing vacuously — a green run always
  means real traffic was checked. The suite also guards each spec with
  `Expect(trace.Select(isInfoRequest)).NotTo(BeEmpty())`.
- **The limit is per key.** A per-asset ceiling only bounds each asset. A client polling
  50 streams at 1/sec each (50/sec aggregate) passes a per-asset limit — by design. To
  bound total load, add a second assertion with `By: nil`. The compliant fixture is
  per-asset clean yet exceeds an aggregate `By: nil` bucket, and the suite asserts both.
- **It's a ceiling, not an average** — see [above](#it-is-a-ceiling-not-an-average).
- **`Every` must be > 0 and `Burst` >= 1.** An invalid limit panics rather than silently
  allowing everything.
- **`STREAM.INFO` and `CONSUMER.INFO` on the same stream are separate buckets** here (the
  per-asset choice). If your server rate-limits per stream, key consumer info by its
  parent stream instead so they share one bucket.
- This example assumes the default `$JS.API` prefix; JetStream **domain**-prefixed API
  subjects (`$JS.<domain>.API.…`) are out of scope — extend the grammars to cover them.

## The CLI (recording your own capture)

You do not need a server to run the suite — the committed fixtures are hand-written. The
CLI ([`main.go`](main.go)) is only for recording a *real* capture through the
`tracecapture` proxy.

It reads JSON config (from a file argument or stdin) and polls a stream's (and optionally
a consumer's) INFO endpoint `count` times, issuing the opening `burst` rounds back-to-back
then spacing the rest by `interval`. See [`config.example.json`](config.example.json):

```json
{
  "server": "nats://127.0.0.1:4223",
  "name": "info-requests",
  "stream": "ORDERS",
  "consumer": "worker",
  "interval": "1s",
  "count": 12,
  "burst": 5
}
```

| Field                   | Default                 | Meaning                                                               |
|-------------------------|-------------------------|-----------------------------------------------------------------------|
| `server`                | `nats://127.0.0.1:4222` | NATS URL — point at the capturing proxy to record a trace             |
| `name`                  | `info-requests`         | connection name (lets the proxy isolate this run with `--match-name`) |
| `credentials`           | –                       | optional path to a `.creds` file                                      |
| `username` / `password` | –                       | optional username/password authentication                             |
| `stream`                | *(required)*            | the stream whose `STREAM.INFO` is polled                              |
| `consumer`              | –                       | optional consumer whose `CONSUMER.INFO` is also polled each round     |
| `interval`              | `1s`                    | spacing between rounds after the opening burst (a Go duration)        |
| `count`                 | `10`                    | total poll rounds                                                     |
| `burst`                 | `5`                     | opening rounds issued back-to-back before `interval` applies          |

```bash
# 1. capturing proxy in front of your server
tracecapture proxy \
    --to 127.0.0.1:4222 \
    --listen 127.0.0.1:4223 \
    --capture ./cap \
    --convert \
    --match-name info-requests

# 2. run the CLI against the proxy (server: nats://127.0.0.1:4223)
go run . config.example.json          # compliant: burst 5, then every 1s
#   ... or drive it past the ceiling for a violating capture:
echo '{"stream":"ORDERS","interval":"100ms","count":12,"burst":0}' | go run . -

# 3. move the converted "expanded" trace into testdata/
cp ./cap/*info-requests*.expanded.json testdata/compliant.expanded.json
```

## Running the suite

```bash
go test -v ./...
```

Each spec loads its capture with `MustLoadCapture`, which resolves the path from
`$TRACE_DIR/<file>` (the directory the [`ta`](../../cmd/ta) runner exports to the suite)
or `testdata/<file>` for a plain `go test`. When `TRACE_DIR` is set the `testdata/`
fallback is intentionally not used, so a green run always asserts the supplied capture. A
missing, unreadable, or truncated capture is a hard failure.

To run the suite through the [`ta`](../../cmd/ta) runner against a directory of traces
(from the repository root, so `ta`'s own module provides its dependencies):

```bash
go run ./cmd/ta run --suite examples/info_requests --traces examples/info_requests/testdata
```
