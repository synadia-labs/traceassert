package match

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/jsm.go/api"
	gtypes "github.com/onsi/gomega/types"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/synadia-labs/traceassert"
)

// SchemaValidator implements api.StructValidator using JSON Schema, turning jsm.go's
// otherwise no-op Validate() into real deep validation against api.Schema().
type SchemaValidator struct{}

func (v SchemaValidator) ValidateStruct(data any, schemaType string) (ok bool, errs []string) {
	s, err := api.Schema(schemaType)
	if err != nil {
		return false, []string{fmt.Sprintf("unknown schema type %s", schemaType)}
	}
	sch, err := jsonschema.CompileString("schema.json", string(s))
	if err != nil {
		return false, []string{fmt.Sprintf("could not load schema %s: %s", schemaType, err)}
	}

	// jsonschema only accepts basic primitives, so round-trip through JSON.
	var d any
	dj, err := json.Marshal(data)
	if err != nil {
		return false, []string{fmt.Sprintf("could not serialize data: %s", err)}
	}
	if err := json.Unmarshal(dj, &d); err != nil {
		return false, []string{fmt.Sprintf("could not de-serialize data: %s", err)}
	}

	if err := sch.Validate(d); err != nil {
		verr, ok := err.(*jsonschema.ValidationError)
		if !ok {
			return false, []string{fmt.Sprintf("could not validate: %s", err)}
		}
		for _, e := range verr.BasicOutput().Errors {
			if e.KeywordLocation == "" || e.Error == "oneOf failed" || e.Error == "allOf failed" {
				continue
			}
			if e.InstanceLocation == "" {
				errs = append(errs, e.Error)
			} else {
				errs = append(errs, fmt.Sprintf("%s: %s", e.InstanceLocation, e.Error))
			}
		}
		return false, errs
	}

	return true, nil
}

// decodeJS decodes an event's payload into its jsm.go type, reporting the schema
// type. Requests are typed from their subject; everything else is auto-detected from
// the payload's embedded `type` field.
func decodeJS(e *traceassert.Event) (msg any, schemaType string, err error) {
	if e.Dir == traceassert.ToServer && e.Subject != "" {
		if v, terr := api.TypeForRequestSubject(e.Subject); terr == nil {
			if uerr := json.Unmarshal(e.Payload, v); uerr != nil {
				return nil, "", fmt.Errorf("decode request payload: %w", uerr)
			}
			st := ""
			if sm, ok := v.(api.SchemaManagedType); ok {
				st = sm.SchemaType()
			}
			return v, st, nil
		}
	}
	st, m, perr := api.ParseMessage(e.Payload)
	if perr != nil {
		return nil, "", fmt.Errorf("parse message: %w", perr)
	}
	return m, st, nil
}

func validateJS(v any) (bool, string) {
	sm, ok := v.(api.SchemaManagedType)
	if !ok {
		return false, fmt.Sprintf("type %T is not schema-managed", v)
	}
	ok, errs := sm.Validate(SchemaValidator{})
	if !ok {
		return false, strings.Join(errs, "; ")
	}
	return true, ""
}

// BeValidJetStreamRequest matches a client→server event whose subject is a JetStream
// API request and whose payload is schema-valid for that request type. No schema name
// is given — the type is derived from the subject.
func BeValidJetStreamRequest() M {
	return eventDetail("be a valid JetStream API request", func(e *traceassert.Event) (bool, string) {
		v, err := api.TypeForRequestSubject(e.Subject)
		if err != nil {
			return false, fmt.Sprintf("subject %q is not a JetStream API request: %v", e.Subject, err)
		}
		if err := json.Unmarshal(e.Payload, v); err != nil {
			return false, fmt.Sprintf("payload did not decode: %v", err)
		}
		return validateJS(v)
	})
}

// BeValidJetStreamMessage matches any message whose embedded `type` identifies a
// jsm.go schema and whose payload is schema-valid (responses, events, advisories).
func BeValidJetStreamMessage() M {
	return eventDetail("be a valid JetStream message", func(e *traceassert.Event) (bool, string) {
		_, msg, err := api.ParseMessage(e.Payload)
		if err != nil {
			return false, fmt.Sprintf("could not parse message: %v", err)
		}
		return validateJS(msg)
	})
}

// BeJetStreamType matches when the event's derived/detected schema type equals
// schemaType (e.g. "io.nats.jetstream.api.v1.pub_ack_response").
func BeJetStreamType(schemaType string) M {
	return eventDetail(fmt.Sprintf("be JetStream type %q", schemaType), func(e *traceassert.Event) (bool, string) {
		_, st, err := decodeJS(e)
		if err != nil {
			return false, err.Error()
		}
		if st != schemaType {
			return false, fmt.Sprintf("got type %q", st)
		}
		return true, ""
	})
}

// DecodeJetStream decodes the event into its typed jsm.go struct (auto-detected from
// the subject or the payload's `type` field) and applies inner to it, so field
// assertions use the real Go type (e.g. HaveField("Config.Name", ...)).
func DecodeJetStream(inner gtypes.GomegaMatcher) M {
	return eventDetail("decode as a JetStream type and match its fields", func(e *traceassert.Event) (bool, string) {
		v, _, err := decodeJS(e)
		if err != nil {
			return false, err.Error()
		}
		return applyTyped(inner, v)
	})
}

// DecodeJetStreamAs decodes the event's payload into the explicitly named jsm.go type
// and applies inner to it. Use this for payloads that carry no `type` field and so
// cannot be auto-detected — e.g. a JetStream pub ack, whose type is known from context
// (the reply to a publish):
//
//	DecodeJetStreamAs("io.nats.jetstream.api.v1.pub_ack_response",
//		HaveField("BatchSize", Equal(5)))
func DecodeJetStreamAs(schemaType string, inner gtypes.GomegaMatcher) M {
	return eventDetail(fmt.Sprintf("decode as %s and match its fields", schemaType), func(e *traceassert.Event) (bool, string) {
		v, known := api.NewMessage(schemaType)
		if !known {
			return false, fmt.Sprintf("unknown jsm.go schema type %q", schemaType)
		}
		if err := json.Unmarshal(e.Payload, v); err != nil {
			return false, fmt.Sprintf("payload did not decode as %s: %v", schemaType, err)
		}
		return applyTyped(inner, v)
	})
}

func applyTyped(inner gtypes.GomegaMatcher, v any) (bool, string) {
	ok, merr := inner.Match(v)
	if merr != nil {
		return false, merr.Error()
	}
	if !ok {
		return false, inner.FailureMessage(v)
	}
	return true, ""
}
