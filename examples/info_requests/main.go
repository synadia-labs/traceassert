// Command info_requests is a tiny, JSON-configured client that polls a stream's (and,
// optionally, a consumer's) JetStream INFO endpoint at a configurable rate.
//
// Its only purpose is to produce a wire trace for the companion conformance suite
// (info_requests_test.go): point it at a capturing proxy, and it emits a series of
// STREAM.INFO / CONSUMER.INFO requests whose timing the suite asserts against a token
// bucket. Drive it with a short Interval (or a large Burst) to record a violating
// capture; drive it at or above the limit's spacing to record a compliant one.
//
// The program reads its configuration as JSON (from a file given as the first argument,
// or from stdin), then polls until it has issued Count rounds.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Config is the JSON the CLI consumes. Only Stream is required; everything else has a
// sensible default.
type Config struct {
	// Server is the NATS URL to connect to. Point this at the capturing proxy (e.g.
	// nats://127.0.0.1:4223) to record a trace.
	Server string `json:"server"`
	// Name is the connection name, so the proxy can isolate this run with --match-name.
	Name string `json:"name"`
	// Credentials is an optional path to a NATS credentials (.creds) file.
	Credentials string `json:"credentials,omitempty"`
	// Username and Password enable username/password authentication. Ignored when
	// Username is empty.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// Stream is the stream whose STREAM.INFO is polled.
	Stream string `json:"stream"`
	// Consumer is an optional consumer on Stream whose CONSUMER.INFO is also polled each
	// round.
	Consumer string `json:"consumer,omitempty"`

	// Interval is the spacing between poll rounds, as a Go duration (e.g. "1s"). Rounds
	// in the opening Burst are issued back-to-back with no spacing.
	Interval string `json:"interval"`
	// Count is the total number of poll rounds to issue.
	Count int `json:"count"`
	// Burst is how many opening rounds are issued back-to-back before Interval spacing
	// applies. Use it to record the "short burst" the limit tolerates.
	Burst int `json:"burst"`
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

	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return fmt.Errorf("invalid interval %q: %w", cfg.Interval, err)
	}

	nc, err := connect(cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer nc.Close()

	ctx := context.Background()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}

	stream, err := js.Stream(ctx, cfg.Stream)
	if err != nil {
		return fmt.Errorf("open stream %q: %w", cfg.Stream, err)
	}

	var consumer jetstream.Consumer
	if cfg.Consumer != "" {
		consumer, err = stream.Consumer(ctx, cfg.Consumer)
		if err != nil {
			return fmt.Errorf("open consumer %q: %w", cfg.Consumer, err)
		}
	}

	for i := range cfg.Count {
		// Space rounds after the opening burst; the first round never sleeps.
		if i >= cfg.Burst && i > 0 {
			time.Sleep(interval)
		}

		if _, err := stream.Info(ctx); err != nil {
			return fmt.Errorf("stream info (round %d): %w", i+1, err)
		}
		if consumer != nil {
			if _, err := consumer.Info(ctx); err != nil {
				return fmt.Errorf("consumer info (round %d): %w", i+1, err)
			}
		}
	}

	fmt.Printf("polled %s", cfg.Stream)
	if cfg.Consumer != "" {
		fmt.Printf(" and consumer %s", cfg.Consumer)
	}
	fmt.Printf(": %d rounds (burst %d, then every %s)\n", cfg.Count, cfg.Burst, interval)
	return nil
}

// loadConfig reads the JSON config from the file named as the first argument, or from
// stdin when none is given, and applies defaults.
func loadConfig() (Config, error) {
	var raw []byte
	var err error
	if len(os.Args) > 1 && os.Args[1] != "-" {
		raw, err = os.ReadFile(os.Args[1])
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Config{
		Server:   nats.DefaultURL,
		Name:     "info-requests",
		Interval: "1s",
		Count:    10,
		Burst:    5,
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Stream == "" {
		return Config{}, fmt.Errorf("config: stream is required")
	}
	return cfg, nil
}

// connect opens the NATS connection with whatever authentication the config supplies.
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
