// Command adr-50 is a tiny, JSON-configured client that drives the **fast-ingest**
// batch publishing protocol from ADR-50.
//
// It uses orbit.go's jetstreamext.NewFastPublisher — the fast-ingest publisher.
//
// The program reads its configuration as JSON (from a file given as the first
// argument, or from stdin), optionally creates the stream with AllowBatchPublish
// enabled, then publishes the requested number of messages as a single fast-ingest
// batch and prints the commit ack.
//
// Point it at a capturing proxy (see ../README and the tracecapture tool) to record
// the wire trace the companion conformance test asserts against.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"
)

// Config is the JSON the CLI consumes. Only Stream, Subjects and Messages are
// really required; everything else has a sensible default.
type Config struct {
	// Server is the NATS server URL to connect to. Point this at the capturing
	// proxy (e.g. nats://127.0.0.1:4223) to record a trace.
	Server string `json:"server"`
	// Name is the connection name. The capture proxy can isolate this run with
	// --match-name, so keep it distinctive.
	Name string `json:"name"`
	// Credentials is an optional path to a NATS credentials (.creds) file.
	Credentials string `json:"credentials,omitempty"`
	// Username and Password enable username/password authentication. Ignored when
	// Username is empty.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// Stream is the target stream name.
	Stream string `json:"stream"`
	// Subjects are the stream's subjects (used only when Create is true; may be
	// wildcards such as "TEST.>").
	Subjects []string `json:"subjects"`
	// PublishSubjects are the concrete subjects to publish to, round-robin. When
	// empty they are derived from Subjects by replacing every "*"/">" token with
	// "x" (so "TEST.>" becomes "TEST.x").
	PublishSubjects []string `json:"publish_subjects,omitempty"`

	// Messages is the total number of messages to store in the stream. The final
	// message is the batch commit (commit-store). Must be >= 1; Messages == 1
	// exercises the single-message immediate-commit path (FB-201).
	Messages int `json:"messages"`
	// Create, when true, creates/updates the stream with AllowBatchPublish: true
	// before publishing.
	Create bool `json:"create"`

	// Gap selects the fast-ingest gap mode: "ok" (continue on gaps) or "fail"
	// (abandon on gaps). Encoded into the reply subject. Default "ok".
	Gap string `json:"gap"`
	// Flow is the requested initial/maximum ack frequency (the upper bound the
	// server must not exceed). Default 100.
	Flow uint16 `json:"flow"`
	// MaxOutstandingAcks bounds how many unacked messages the client allows before
	// stalling for an ack. Default 2.
	MaxOutstandingAcks uint16 `json:"max_outstanding_acks"`
	// AckTimeout is an optional Go duration (e.g. "10s") for ack waits. Empty uses
	// the JetStream context default.
	AckTimeout string `json:"ack_timeout,omitempty"`

	// Payload is the base message body; each message gets "<payload>-<seq>".
	Payload string `json:"payload"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	nc, err := connect(cfg)
	if err != nil {
		return fmt.Errorf("connect to %q: %w", cfg.Server, err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if cfg.Create {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     cfg.Stream,
			Subjects: cfg.Subjects,
			Storage:  jetstream.FileStorage,
			// AllowBatchPublish enables ADR-50 *fast-ingest* batch publishing
			// (allow_batched). This is the stream flag the FB-* conformance
			// tests require; it is distinct from AllowAtomicPublish.
			AllowBatchPublish: true,
		}); err != nil {
			return fmt.Errorf("create stream %q: %w", cfg.Stream, err)
		}
	}

	fp, err := newPublisher(cfg, js)
	if err != nil {
		return fmt.Errorf("create fast publisher: %w", err)
	}

	subjects := cfg.publishSubjects()

	// All but the final message are appended; the last one commits the batch and
	// is stored. Messages == 1 falls straight through to the commit, exercising the
	// single-message immediate-commit shortcut.
	for seq := 1; seq < cfg.Messages; seq++ {
		subj := subjects[(seq-1)%len(subjects)]
		if _, err := fp.Add(subj, payload(cfg, seq)); err != nil {
			return fmt.Errorf("add message %d (%s): %w", seq, subj, err)
		}
	}

	commitSubj := subjects[(cfg.Messages-1)%len(subjects)]
	ack, err := fp.Commit(ctx, commitSubj, payload(cfg, cfg.Messages))
	if err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}

	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	out, _ := json.MarshalIndent(ack, "", "  ")
	fmt.Printf("committed fast-ingest batch:\n%s\n", out)
	return nil
}

// loadConfig reads the JSON config from argv[1] (or stdin) and applies defaults.
func loadConfig() (Config, error) {
	var raw []byte
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] != "-":
		raw, err = os.ReadFile(os.Args[1])
	default:
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Server == "" {
		cfg.Server = nats.DefaultURL
	}
	if cfg.Name == "" {
		cfg.Name = "adr50-fast-ingest"
	}
	if cfg.Gap == "" {
		cfg.Gap = "ok"
	}
	if cfg.Flow == 0 {
		cfg.Flow = 100
	}
	if cfg.MaxOutstandingAcks == 0 {
		cfg.MaxOutstandingAcks = 2
	}
	if cfg.Payload == "" {
		cfg.Payload = "msg"
	}

	if cfg.Stream == "" {
		return Config{}, fmt.Errorf("config: stream is required")
	}
	if cfg.Messages < 1 {
		return Config{}, fmt.Errorf("config: messages must be >= 1")
	}
	if cfg.Gap != "ok" && cfg.Gap != "fail" {
		return Config{}, fmt.Errorf("config: gap must be %q or %q", "ok", "fail")
	}
	if len(cfg.publishSubjects()) == 0 {
		return Config{}, fmt.Errorf("config: provide subjects or publish_subjects")
	}
	return cfg, nil
}

func connect(cfg Config) (*nats.Conn, error) {
	opts := []nats.Option{nats.Name(cfg.Name)}
	if cfg.Credentials != "" {
		opts = append(opts, nats.UserCredentials(cfg.Credentials))
	}
	if cfg.Username != "" {
		opts = append(opts, nats.UserInfo(cfg.Username, cfg.Password))
	}
	return nats.Connect(cfg.Server, opts...)
}

func newPublisher(cfg Config, js jetstream.JetStream) (jetstreamext.FastPublisher, error) {
	fc := jetstreamext.FastPublishFlowControl{
		Flow:               cfg.Flow,
		MaxOutstandingAcks: cfg.MaxOutstandingAcks,
	}
	if cfg.AckTimeout != "" {
		d, err := time.ParseDuration(cfg.AckTimeout)
		if err != nil {
			return nil, fmt.Errorf("ack_timeout: %w", err)
		}
		fc.AckTimeout = d
	}
	return jetstreamext.NewFastPublisher(js, fc,
		// gap "ok" => continue on gaps; gap "fail" => abandon on gaps.
		jetstreamext.WithFastPublisherContinueOnGap(cfg.Gap == "ok"))
}

// publishSubjects returns the concrete subjects to publish to, deriving them from
// the (possibly wildcard) stream subjects when PublishSubjects is not set.
func (c Config) publishSubjects() []string {
	if len(c.PublishSubjects) > 0 {
		return c.PublishSubjects
	}
	out := make([]string, 0, len(c.Subjects))
	for _, s := range c.Subjects {
		if cs := concreteSubject(s); cs != "" {
			out = append(out, cs)
		}
	}
	return out
}

// concreteSubject turns a (possibly wildcard) stream subject into a concrete
// publishable one by replacing each "*"/">" token with "x".
func concreteSubject(s string) string {
	toks := strings.Split(s, ".")
	for i, t := range toks {
		if t == "*" || t == ">" {
			toks[i] = "x"
		}
	}
	return strings.Join(toks, ".")
}

func payload(cfg Config, seq int) []byte {
	return fmt.Appendf(nil, "%s-%d", cfg.Payload, seq)
}
