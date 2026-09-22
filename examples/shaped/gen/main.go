// Command gen records the fixture of the shaped example. It publishes one ADR-50
// fast-ingest batch through the capture proxy of an ntf-server instance whose shaping
// set (shaping.json) drops the flow ack for batch sequence 30 and closes the
// connection on the publish of sequence 35. The client stalls when it runs out of
// credit, reconnects, re-subscribes its inbox and resumes the batch, so the proxy
// stores two captures. The command then fetches both from the TRACES object store on
// the management server and writes them, in header timestamp order, as
// reconnect.expanded.json and reconnect-1.expanded.json under --out.
//
// Create the instance first and pass its id and proxy URL:
//
//	ntf-server admin create server --jetstream --capture --shape shaping.json --json
//	go run ./gen --instance <id> --proxy <trace_url>
//	ntf-server admin destroy <id>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"
)

const (
	// connectionName is what shaping.json's connection_name matches.
	connectionName = "shaped-reconnect"
	setupName      = "shaped-setup"
	stream         = "SHAPED"
	subject        = "shaped.msgs"
	scenario       = "reconnect"
	bucket         = "TRACES"

	// messages is the batch size; the last one is the commit. The disconnect rule
	// fires on sequence 35 and the client stalls at 40, so the batch must run past 40.
	messages = 45
	// flow is the ack frequency the client asks for; a single batch on the stream
	// is granted exactly that, so the server acks at 10, 20, 30 and 40.
	flow = 10
	// outstanding is how many ack windows the client may run ahead of its last ack.
	// With the ack for 30 lost the client sends up to 40 on the credit of ack 20 and
	// stalls there until the ack for 40 arrives on the new connection.
	outstanding = 2

	// pace is the gap between publishes: each ack reaches the proxy before the next
	// publish, so every run orders acks and publishes the same way. closePace follows
	// the publish the proxy closes on: the client notices the close before it
	// publishes again, so the next publish is buffered and sent on the new connection.
	pace      = 5 * time.Millisecond
	closePace = 500 * time.Millisecond
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	proxy := flag.String("proxy", "", "URL of the instance's capture proxy (trace_url from admin create)")
	mgmt := flag.String("mgmt", nats.DefaultURL, "URL of the ntf-server management service holding the TRACES object store")
	instance := flag.String("instance", "", "id of the ntf-server instance the proxy belongs to")
	out := flag.String("out", "testdata", "directory the two captures are written to")
	flag.Parse()

	if *proxy == "" || *instance == "" {
		return errors.New("--proxy and --instance are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	err := createStream(ctx, *proxy)
	if err != nil {
		return err
	}

	started := time.Now()
	err = publishBatch(ctx, *proxy)
	if err != nil {
		return err
	}

	captures, err := fetchCaptures(ctx, *mgmt, *instance, started)
	if err != nil {
		return err
	}

	return writeCaptures(*out, captures)
}

// createStream makes the target stream with fast-ingest batch publishing enabled,
// on a connection whose name the shaping set does not match.
func createStream(ctx context.Context, proxy string) error {
	nc, err := nats.Connect(proxy, nats.Name(setupName))
	if err != nil {
		return fmt.Errorf("connect to proxy %s: %w", proxy, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              stream,
		Subjects:          []string{"shaped.>"},
		Storage:           jetstream.FileStorage,
		AllowBatchPublish: true,
	})
	if err != nil {
		return fmt.Errorf("create stream %s: %w", stream, err)
	}
	return nil
}

// publishBatch drives the shaped connection: one fast-ingest batch of messages
// publishes with the last one committing. nats.go reconnects on the induced close
// with its default options and resends the inbox subscription before the publishes
// it buffered while disconnected.
func publishBatch(ctx context.Context, proxy string) error {
	nc, err := nats.Connect(proxy,
		nats.Name(connectionName),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			fmt.Fprintf(os.Stderr, "client disconnected: %v\n", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Fprintf(os.Stderr, "client reconnected to %s\n", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return fmt.Errorf("connect to proxy %s: %w", proxy, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	fp, err := jetstreamext.NewFastPublisher(js, jetstreamext.FastPublishFlowControl{
		Flow:               flow,
		MaxOutstandingAcks: outstanding,
		// Long enough that the client does not ping for lost acks during the
		// reconnect, so the captures hold only the frames the scenario is about.
		AckTimeout: 30 * time.Second,
	}, jetstreamext.WithFastPublisherContinueOnGap(true))
	if err != nil {
		return fmt.Errorf("create fast publisher: %w", err)
	}

	for seq := 1; seq < messages; seq++ {
		_, err = fp.Add(subject, fmt.Appendf(nil, "msg-%d", seq))
		if err != nil {
			return fmt.Errorf("add message %d: %w", seq, err)
		}
		time.Sleep(pace)
		if seq == 35 {
			time.Sleep(closePace)
		}
	}

	ack, err := fp.Commit(ctx, subject, fmt.Appendf(nil, "msg-%d", messages))
	if err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}
	fmt.Fprintf(os.Stderr, "committed batch %s: stream seq %d, count %d\n", ack.BatchID, ack.Sequence, ack.BatchSize)
	return nil
}

// capture is one object from the TRACES store with the header timestamp its first
// line carries, which is the order LoadSession uses.
type capture struct {
	name string
	ts   time.Time
	body []byte
}

// fetchCaptures waits for the proxy to store the shaped connection's two captures
// and returns them in header timestamp order. Objects of other instances, other
// connection names and earlier runs on the same instance are left out.
func fetchCaptures(ctx context.Context, mgmt, instance string, since time.Time) ([]capture, error) {
	nc, err := nats.Connect(mgmt, nats.Name("shaped-fetch"))
	if err != nil {
		return nil, fmt.Errorf("connect to management server %s: %w", mgmt, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	store, err := js.ObjectStore(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("object store %s: %w", bucket, err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		infos, err := store.List(ctx)
		if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
			return nil, fmt.Errorf("list %s: %w", bucket, err)
		}

		var captures []capture
		for _, info := range infos {
			if info.Metadata["instance_id"] != instance || info.Metadata["client_name"] != connectionName {
				continue
			}
			capturedAt, err := time.Parse(time.RFC3339, info.Metadata["captured_at"])
			if err != nil || capturedAt.Before(since) {
				continue
			}
			body, err := store.GetBytes(ctx, info.Name)
			if err != nil {
				return nil, fmt.Errorf("get %s: %w", info.Name, err)
			}
			ts, err := headerTimestamp(body)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", info.Name, err)
			}
			captures = append(captures, capture{name: info.Name, ts: ts, body: body})
		}

		if len(captures) == 2 {
			sort.Slice(captures, func(i, j int) bool { return captures[i].ts.Before(captures[j].ts) })
			return captures, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("expected 2 captures of %s on instance %s, found %d", connectionName, instance, len(captures))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// headerTimestamp reads the ts of the header on the first line of an expanded capture.
func headerTimestamp(body []byte) (time.Time, error) {
	first, _, _ := strings.Cut(string(body), "\n")
	var doc struct {
		Header struct {
			Timestamp time.Time `json:"ts"`
		} `json:"header"`
	}
	err := json.Unmarshal([]byte(first), &doc)
	if err != nil {
		return time.Time{}, fmt.Errorf("capture header: %w", err)
	}
	if doc.Header.Timestamp.IsZero() {
		return time.Time{}, errors.New("capture header has no timestamp")
	}
	return doc.Header.Timestamp, nil
}

// writeCaptures stores the captures under the scenario's names: the first as
// reconnect.expanded.json, the next as reconnect-1.expanded.json.
func writeCaptures(dir string, captures []capture) error {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return err
	}
	for i, c := range captures {
		name := scenario + ".expanded.json"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.expanded.json", scenario, i)
		}
		path := filepath.Join(dir, name)
		err = os.WriteFile(path, c.body, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s from %s (%d lines)\n", path, c.name, strings.Count(string(c.body), "\n"))
	}
	return nil
}
