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
