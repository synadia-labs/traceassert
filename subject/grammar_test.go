package subject

import (
	"reflect"
	"testing"
)

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		spec string
	}{
		{"empty spec", ""},
		{"two rest tokens", "{a:rest}.{b:rest}.$X"},
		{"unknown type", "{x:float}"},
		{"empty literal between dots", "a..b"},
		{"duplicate capture name", "{x}.{x}"},
		{"missing capture name", "{:int}"},
		{"empty enum value", "{g:enum(ok,)}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.spec); err == nil {
				t.Fatalf("Parse(%q) expected error, got nil", tc.spec)
			}
		})
	}
}

func TestParseValid(t *testing.T) {
	specs := []string{
		"$FI",
		"{uuid}",
		"{seq:int}",
		"{gap:enum(ok,fail)}",
		"{prefix:rest}.{uuid}.>",
		"$JS.API.STREAM.CREATE.{stream}",
		"{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI",
	}
	for _, s := range specs {
		if _, err := Parse(s); err != nil {
			t.Fatalf("Parse(%q) unexpected error: %v", s, err)
		}
	}
}

var fiReply = MustParse("{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

func TestMatch_FastIngestReply(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		ok      bool
		want    Captures
	}{
		{
			name:    "single-token prefix",
			subject: "_INBOX.batch1.10.ok.1.0.$FI",
			ok:      true,
			want:    Captures{"prefix": "_INBOX", "uuid": "batch1", "flow": "10", "gap": "ok", "seq": "1", "op": "0"},
		},
		{
			name:    "multi-token (dotted) prefix",
			subject: "_INBOX.us-east.node7.batch1.10.fail.42.2.$FI",
			ok:      true,
			want:    Captures{"prefix": "_INBOX.us-east.node7", "uuid": "batch1", "flow": "10", "gap": "fail", "seq": "42", "op": "2"},
		},
		{name: "missing $FI anchor", subject: "_INBOX.batch1.10.ok.1.0", ok: false},
		{name: "non-int seq", subject: "_INBOX.batch1.10.ok.x.0.$FI", ok: false},
		{name: "bad gap enum", subject: "_INBOX.batch1.10.maybe.1.0.$FI", ok: false},
		{name: "too short for rest>=1", subject: "batch1.10.ok.1.0.$FI", ok: false},
		{name: "empty", subject: "", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fiReply.Match(tc.subject)
			if ok != tc.ok {
				t.Fatalf("Match(%q) ok=%v, want %v (caps=%v)", tc.subject, ok, tc.ok, got)
			}
			if tc.ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Match(%q) caps=%v, want %v", tc.subject, got, tc.want)
			}
		})
	}
}

func TestMatch_NoRest_ArityStrict(t *testing.T) {
	g := MustParse("$JS.API.STREAM.CREATE.{stream}")

	if caps, ok := g.Match("$JS.API.STREAM.CREATE.ORDERS"); !ok || caps["stream"] != "ORDERS" {
		t.Fatalf("expected stream=ORDERS, got ok=%v caps=%v", ok, caps)
	}
	// extra trailing token must not match (no rest token to absorb it).
	if _, ok := g.Match("$JS.API.STREAM.CREATE.ORDERS.extra"); ok {
		t.Fatalf("expected no match for over-long subject")
	}
	// wrong literal.
	if _, ok := g.Match("$JS.API.STREAM.DELETE.ORDERS"); ok {
		t.Fatalf("expected no match for wrong literal")
	}
}

func TestMatch_InboxWildcardLiteral(t *testing.T) {
	// '>' is a literal token here: asserts the SUB used a tail wildcard.
	g := MustParse("{prefix:rest}.{uuid}.>")

	caps, ok := g.Match("_INBOX.batch1.>")
	if !ok {
		t.Fatalf("expected match for inbox subscription subject")
	}
	if caps["prefix"] != "_INBOX" || caps["uuid"] != "batch1" {
		t.Fatalf("unexpected captures: %v", caps)
	}
	// a concrete (non-wildcard) subject must not match the literal '>'.
	if _, ok := g.Match("_INBOX.batch1.data"); ok {
		t.Fatalf("expected no match: concrete token where '>' literal required")
	}
}

func TestExtractors(t *testing.T) {
	seqOf := fiReply.Int("seq")
	uuidOf := fiReply.Str("uuid")

	if n, ok := seqOf("_INBOX.b.10.ok.42.1.$FI"); !ok || n != 42 {
		t.Fatalf("Int(seq) = %d,%v want 42,true", n, ok)
	}
	if s, ok := uuidOf("_INBOX.b.10.ok.42.1.$FI"); !ok || s != "b" {
		t.Fatalf("Str(uuid) = %q,%v want \"b\",true", s, ok)
	}
	// extractor on a non-matching subject reports ok=false rather than a zero value.
	if _, ok := seqOf("not.a.batch.subject"); ok {
		t.Fatalf("Int(seq) expected ok=false for non-matching subject")
	}
	// unknown capture name.
	if _, ok := fiReply.Str("nope")("_INBOX.b.10.ok.1.0.$FI"); ok {
		t.Fatalf("Str(nope) expected ok=false for unknown capture")
	}
}

func TestCapturesInt(t *testing.T) {
	caps, ok := fiReply.Match("_INBOX.b.10.ok.7.3.$FI")
	if !ok {
		t.Fatal("expected match")
	}
	if n, ok := caps.Int("op"); !ok || n != 3 {
		t.Fatalf("caps.Int(op) = %d,%v want 3,true", n, ok)
	}
	if _, ok := caps.Int("uuid"); ok {
		t.Fatalf("caps.Int(uuid) expected ok=false for non-numeric capture")
	}
}
