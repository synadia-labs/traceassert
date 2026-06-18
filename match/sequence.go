package match

import (
	"fmt"

	"github.com/tidwall/gjson"

	"github.com/synadia-labs/traceassert"
	"github.com/synadia-labs/traceassert/subject"
)

// IntField extracts an integer from an event, reporting ok=false when absent.
type IntField func(*traceassert.Event) (int, bool)

// StrField extracts a string from an event, reporting ok=false when absent.
type StrField func(*traceassert.Event) (string, bool)

// GrammarInt extracts a named int capture from an event, trying its subject first
// then its reply (so a reply-encoded grammar like the fast-ingest $FI reply works on
// publishes).
func GrammarInt(g *subject.Grammar, name string) IntField {
	ints := g.Int(name)
	return func(e *traceassert.Event) (int, bool) {
		if v, ok := ints(e.Subject); ok {
			return v, true
		}
		return ints(e.Reply)
	}
}

// GrammarStr is GrammarInt for a string capture.
func GrammarStr(g *subject.Grammar, name string) StrField {
	strs := g.Str(name)
	return func(e *traceassert.Event) (string, bool) {
		if v, ok := strs(e.Subject); ok {
			return v, true
		}
		return strs(e.Reply)
	}
}

// PayloadField extracts a gjson path from the payload as a string.
func PayloadField(path string) StrField {
	return func(e *traceassert.Event) (string, bool) {
		r := gjson.GetBytes(e.Payload, path)
		if !r.Exists() {
			return "", false
		}
		return r.String(), true
	}
}

// BeContiguousFrom asserts that the field-bearing events form a strictly
// incrementing, gapless integer sequence beginning at start (in trace order).
func BeContiguousFrom(start int, f IntField) M {
	return sliceCheck(fmt.Sprintf("form a contiguous sequence from %d", start), func(evs []*traceassert.Event) (bool, string) {
		want := start
		seen := false
		for _, e := range evs {
			v, ok := f(e)
			if !ok {
				continue
			}
			seen = true
			if v != want {
				return false, fmt.Sprintf("line %d: got %d, want %d", e.Line, v, want)
			}
			want++
		}
		if !seen {
			return false, "no events carried the sequence field"
		}
		return true, ""
	})
}

// BeMonotonic asserts the field-bearing events are strictly increasing (gaps allowed).
func BeMonotonic(f IntField) M {
	return sliceCheck("be strictly increasing", func(evs []*traceassert.Event) (bool, string) {
		prev := 0
		have := false
		for _, e := range evs {
			v, ok := f(e)
			if !ok {
				continue
			}
			if have && v <= prev {
				return false, fmt.Sprintf("line %d: %d not greater than previous %d", e.Line, v, prev)
			}
			prev, have = v, true
		}
		if !have {
			return false, "no events carried the sequence field"
		}
		return true, ""
	})
}

// SameValue is a single-event matcher asserting two field extractors yield the same
// value on that event (e.g. a subject capture equals a payload field).
func SameValue(a, b StrField) M {
	return eventDetail("have two fields with the same value", func(e *traceassert.Event) (bool, string) {
		av, aok := a(e)
		bv, bok := b(e)
		if !aok || !bok {
			return false, fmt.Sprintf("missing field (a present=%v, b present=%v)", aok, bok)
		}
		if av != bv {
			return false, fmt.Sprintf("%q != %q", av, bv)
		}
		return true, ""
	})
}
