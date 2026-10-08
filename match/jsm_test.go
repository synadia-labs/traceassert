package match_test

import (
	"encoding/json"
	"testing"

	"github.com/nats-io/jsm.go/api"
	"github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
)

func TestHaveAPILevel(t *testing.T) {
	lvl4 := map[string]string{api.JSMetaCurrentServerLevel: "4"}

	// Every stream/consumer create and info response carries the hosted level the same way.
	responses := map[string][]byte{
		"stream_create": mustJSON(t, &api.JSApiStreamCreateResponse{
			JSApiResponse: api.JSApiResponse{Type: "io.nats.jetstream.api.v1.stream_create_response"},
			StreamInfo:    &api.StreamInfo{Config: api.StreamConfig{Name: "T", Metadata: lvl4}},
		}),
		"stream_info": mustJSON(t, &api.JSApiStreamInfoResponse{
			JSApiResponse: api.JSApiResponse{Type: "io.nats.jetstream.api.v1.stream_info_response"},
			StreamInfo:    &api.StreamInfo{Config: api.StreamConfig{Name: "T", Metadata: lvl4}},
		}),
		"consumer_create": mustJSON(t, &api.JSApiConsumerCreateResponse{
			JSApiResponse: api.JSApiResponse{Type: "io.nats.jetstream.api.v1.consumer_create_response"},
			ConsumerInfo:  &api.ConsumerInfo{Config: api.ConsumerConfig{Metadata: lvl4}},
		}),
	}
	for name, payload := range responses {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			e := fromServer(payload)
			g.Expect(e).To(HaveAPILevel(gomega.Equal(4)))
			g.Expect(e).To(HaveAPILevel(gomega.BeNumerically(">=", 4)))
			g.Expect(e).NotTo(HaveAPILevel(gomega.BeNumerically(">", 4)))
		})
	}

	t.Run("missing level metadata fails cleanly", func(t *testing.T) {
		g := gomega.NewWithT(t)
		noMeta := mustJSON(t, &api.JSApiStreamCreateResponse{
			JSApiResponse: api.JSApiResponse{Type: "io.nats.jetstream.api.v1.stream_create_response"},
			StreamInfo:    &api.StreamInfo{Config: api.StreamConfig{Name: "T"}},
		})
		g.Expect(fromServer(noMeta)).NotTo(HaveAPILevel(gomega.BeNumerically(">=", 4)))
	})

	t.Run("a non stream/consumer event fails cleanly", func(t *testing.T) {
		g := gomega.NewWithT(t)
		other := &traceassert.Event{Dir: traceassert.ToServer, Verb: "PUB", Subject: "x.y", Payload: []byte(`{}`)}
		g.Expect(other).NotTo(HaveAPILevel(gomega.BeNumerically(">=", 4)))
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fromServer(payload []byte) *traceassert.Event {
	return &traceassert.Event{Dir: traceassert.FromServer, Verb: "MSG", Subject: "_INBOX.x", Payload: payload}
}

const (
	microInfoValid       = `{"type":"io.nats.micro.v1.info_response","name":"orders","id":"NWm3LHxVcVxXWJ7ofoDqcK","version":"1.2.3","metadata":{"team":"core"},"description":"Order service","endpoints":[{"name":"create","subject":"orders.create","queue_group":"q","metadata":null}]}`
	microInfoNullDesc    = `{"type":"io.nats.micro.v1.info_response","name":"orders","id":"NWm3LHxVcVxXWJ7ofoDqcK","version":"1.2.3","metadata":null,"description":null,"endpoints":[]}`
	microInfoMissingDesc = `{"type":"io.nats.micro.v1.info_response","name":"orders","id":"NWm3LHxVcVxXWJ7ofoDqcK","version":"1.2.3","metadata":null,"endpoints":[]}`
	microPingValid       = `{"type":"io.nats.micro.v1.ping_response","name":"orders","id":"NWm3LHxVcVxXWJ7ofoDqcK","version":"1.2.3","metadata":{}}`
	microStatsValid      = `{"type":"io.nats.micro.v1.stats_response","name":"orders","id":"NWm3LHxVcVxXWJ7ofoDqcK","version":"1.2.3","metadata":null,"started":"2026-10-08T12:00:00Z","endpoints":[{"name":"create","subject":"orders.create","queue_group":"q","num_requests":10,"num_errors":1,"last_error":"boom","processing_time":12345,"average_processing_time":1234,"data":null}]}`
)

// schemaMessageDetail runs BeValidSchemaMessage against a server-delivered payload,
// returning whether it matched and the failure message.
func schemaMessageDetail(t *testing.T, payload string) (bool, string) {
	t.Helper()
	m := BeValidSchemaMessage()
	e := fromServer([]byte(payload))
	ok, err := m.Match(e)
	if err != nil {
		t.Fatal(err)
	}
	return ok, m.FailureMessage(e)
}

func TestBeValidSchemaMessage(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		valid   bool
		reason  string
	}{
		{name: "micro info", payload: microInfoValid, valid: true},
		{name: "micro ping", payload: microPingValid, valid: true},
		{name: "micro stats", payload: microStatsValid, valid: true},
		{name: "jetstream error response", payload: `{"type":"io.nats.jetstream.api.v1.stream_create_response","error":{"code":404,"description":"stream not found"}}`, valid: true},
		{name: "micro info with null description", payload: microInfoNullDesc, reason: "/description: expected string, but got null"},
		{name: "micro info without description", payload: microInfoMissingDesc, reason: "missing properties: 'description'"},
		{name: "no type", payload: `{"name":"orders"}`, reason: `payload has no "type" field`},
		{name: "unknown type", payload: `{"type":"io.nats.bogus.v1.thing"}`, reason: `no schema for type "io.nats.bogus.v1.thing"`},
		{name: "unknown message type", payload: `{"type":"io.nats.unknown_message"}`, reason: `no schema for type "io.nats.unknown_message"`},
		{name: "non io.nats type", payload: `{"type":"com.example.thing"}`, reason: `no schema for type "com.example.thing"`},
		{name: "numeric type", payload: `{"type":1}`, reason: `payload "type" is a JSON number, not a string`},
		{name: "null type", payload: `{"type":null}`, reason: `payload "type" is a JSON null, not a string`},
		{name: "not json", payload: `not json`, reason: "payload did not decode"},
		{name: "empty", payload: ``, reason: "payload did not decode"},
		{name: "trailing data", payload: microPingValid + `{}`, reason: "unexpected data after the JSON value"},
		{name: "array", payload: `[` + microPingValid + `]`, reason: "payload is a JSON array, not an object"},
		{name: "string", payload: `"io.nats.micro.v1.ping_response"`, reason: "payload is a JSON string, not an object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			ok, msg := schemaMessageDetail(t, c.payload)
			if c.valid {
				g.Expect(ok).To(gomega.BeTrue(), msg)
				return
			}
			g.Expect(ok).To(gomega.BeFalse())
			g.Expect(msg).To(gomega.ContainSubstring("expected event to be a valid schema message"))
			g.Expect(msg).To(gomega.ContainSubstring(c.reason))
		})
	}
}

func TestSchemaMessageType(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
		ok      bool
	}{
		{name: "micro info", payload: microInfoValid, want: "io.nats.micro.v1.info_response", ok: true},
		// selection does not validate: a schema-invalid payload with a known type is selected.
		{name: "micro info with null description", payload: microInfoNullDesc, want: "io.nats.micro.v1.info_response", ok: true},
		{name: "micro info without description", payload: microInfoMissingDesc, want: "io.nats.micro.v1.info_response", ok: true},
		{name: "micro ping", payload: microPingValid, want: "io.nats.micro.v1.ping_response", ok: true},
		{name: "micro stats", payload: microStatsValid, want: "io.nats.micro.v1.stats_response", ok: true},
		{name: "no type", payload: `{"name":"orders"}`},
		{name: "unknown type", payload: `{"type":"io.nats.bogus.v1.thing"}`},
		{name: "unknown message type", payload: `{"type":"io.nats.unknown_message"}`},
		{name: "numeric type", payload: `{"type":1}`},
		{name: "not json", payload: `not json`},
		{name: "empty", payload: ``},
		{name: "array", payload: `[` + microPingValid + `]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			st, ok := SchemaMessageType([]byte(c.payload))
			g.Expect(ok).To(gomega.Equal(c.ok))
			g.Expect(st).To(gomega.Equal(c.want))
		})
	}
}
