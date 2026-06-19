package subject

import (
	"strings"
	"testing"
)

var fiReplyB = MustParse("{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")

func TestBuilder_RoundTrip(t *testing.T) {
	b := fiReplyB.Builder().
		Set("prefix", "_INBOX.us-east").
		Set("uuid", "batch1").
		SetInt("flow", 10).
		Set("gap", "fail").
		SetInt("seq", 42).
		SetInt("op", 2)

	subj, err := b.Subject()
	if err != nil {
		t.Fatalf("Subject() error: %v", err)
	}
	want := "_INBOX.us-east.batch1.10.fail.42.2.$FI"
	if subj != want {
		t.Fatalf("Subject() = %q, want %q", subj, want)
	}

	// The built subject must match the grammar it came from.
	caps, ok := fiReplyB.Match(subj)
	if !ok {
		t.Fatalf("Match(%q) failed; builder produced a subject its grammar rejects", subj)
	}
	for name, want := range map[string]string{
		"prefix": "_INBOX.us-east", "uuid": "batch1", "flow": "10",
		"gap": "fail", "seq": "42", "op": "2",
	} {
		if got := caps[name]; got != want {
			t.Fatalf("capture %q = %q, want %q", name, got, want)
		}
	}
}

func TestBuilder_NoRestNoCaptures(t *testing.T) {
	g := MustParse("$JS.API.STREAM.CREATE.{stream}")
	s, err := g.Builder().Set("stream", "ORDERS").Subject()
	if err != nil {
		t.Fatalf("Subject() error: %v", err)
	}
	if s != "$JS.API.STREAM.CREATE.ORDERS" {
		t.Fatalf("Subject() = %q", s)
	}

	// A purely literal grammar renders with no values set.
	lit := MustParse("$FI")
	if s := lit.Builder().MustSubject(); s != "$FI" {
		t.Fatalf("literal-only Subject() = %q, want $FI", s)
	}
}

func TestBuilder_Reuse(t *testing.T) {
	b := fiReplyB.Builder().
		Set("prefix", "_INBOX").
		Set("uuid", "batch1").
		SetInt("flow", 10).
		Set("gap", "ok").
		SetInt("op", 0)

	buf := make([]byte, 0, 64)
	for seq := 1; seq <= 5; seq++ {
		var err error
		buf, err = b.SetInt("seq", seq).AppendSubject(buf[:0])
		if err != nil {
			t.Fatalf("seq=%d AppendSubject error: %v", seq, err)
		}
		want := "_INBOX.batch1.10.ok." + itoa(seq) + ".0.$FI"
		if string(buf) != want {
			t.Fatalf("seq=%d got %q, want %q", seq, buf, want)
		}
	}
}

func TestBuilder_AppendPreservesPrefix(t *testing.T) {
	g := MustParse("{a}.{b}")
	dst := []byte("existing|")
	dst, err := g.Builder().Set("a", "x").Set("b", "y").AppendSubject(dst)
	if err != nil {
		t.Fatalf("AppendSubject error: %v", err)
	}
	if string(dst) != "existing|x.y" {
		t.Fatalf("AppendSubject = %q, want %q", dst, "existing|x.y")
	}
}

func TestBuilder_ValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Builder)
		want string // substring expected in the error
	}{
		{"unset capture", func(b *Builder) { b.Set("uuid", "x") }, "is not set"},
		{"unknown name", func(b *Builder) { b.Set("nope", "x") }, "unknown capture"},
		{"dot in named", func(b *Builder) { fill(b); b.Set("uuid", "a.b") }, "'.' separator"},
		{"space in named", func(b *Builder) { fill(b); b.Set("uuid", "a b") }, "whitespace"},
		{"wildcard in named", func(b *Builder) { fill(b); b.Set("uuid", ">") }, "wildcard"},
		{"empty named", func(b *Builder) { fill(b); b.Set("uuid", "") }, "empty token"},
		{"bad int string", func(b *Builder) { fill(b); b.Set("seq", "x") }, "is not an int"},
		{"enum miss", func(b *Builder) { fill(b); b.Set("gap", "maybe") }, "is not one of"},
		{"enum miss via int", func(b *Builder) { fill(b); b.SetInt("gap", 7) }, "is not one of"},
		{"empty rest segment", func(b *Builder) { fill(b); b.Set("prefix", "a..b") }, "empty token"},
		{"empty rest", func(b *Builder) { fill(b); b.Set("prefix", "") }, "empty rest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fiReplyB.Builder()
			tc.set(b)

			err := b.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %q, want substring %q", err, tc.want)
			}

			// Subject must agree with Validate and not render on error.
			s, serr := b.Subject()
			if serr == nil {
				t.Fatalf("Subject() = %q, want error", s)
			}
		})
	}
}

func TestBuilder_NegativeInt(t *testing.T) {
	g := MustParse("v.{n:int}")
	s, err := g.Builder().SetInt("n", -7).Subject()
	if err != nil {
		t.Fatalf("Subject() error: %v", err)
	}
	if s != "v.-7" {
		t.Fatalf("Subject() = %q, want v.-7", s)
	}
	if !g.Matches(s) {
		t.Fatalf("negative int subject %q does not round-trip", s)
	}
}

func TestBuilder_SetCaptures(t *testing.T) {
	orig := "_INBOX.us-east.batch1.10.fail.42.2.$FI"
	caps, ok := fiReplyB.Match(orig)
	if !ok {
		t.Fatalf("Match(%q) failed", orig)
	}

	// Re-render verbatim from captures.
	s, err := fiReplyB.Builder().SetCaptures(caps).Subject()
	if err != nil {
		t.Fatalf("Subject() error: %v", err)
	}
	if s != orig {
		t.Fatalf("round-trip via SetCaptures = %q, want %q", s, orig)
	}

	// Capture, change one field, re-emit.
	s, err = fiReplyB.Builder().SetCaptures(caps).SetInt("seq", 99).Subject()
	if err != nil {
		t.Fatalf("Subject() error: %v", err)
	}
	if s != "_INBOX.us-east.batch1.10.fail.99.2.$FI" {
		t.Fatalf("amended subject = %q", s)
	}
}

func TestBuilder_Reset(t *testing.T) {
	b := fiReplyB.Builder().Set("nope", "x") // sticky unknown-name error
	if b.Validate() == nil {
		t.Fatal("expected error before reset")
	}
	b.Reset()
	if b.Validate() == nil {
		t.Fatal("after Reset captures are unset, expected a not-set error")
	}
	if !strings.Contains(b.Validate().Error(), "is not set") {
		t.Fatalf("after Reset want not-set error, got %v", b.Validate())
	}
}

func TestBuilder_Clone(t *testing.T) {
	base := fiReplyB.Builder().
		Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("op", 0)

	a := base.Clone().SetInt("seq", 1)
	c := base.Clone().SetInt("seq", 2)

	sa, err := a.Subject()
	if err != nil {
		t.Fatalf("a.Subject() error: %v", err)
	}
	sc, err := c.Subject()
	if err != nil {
		t.Fatalf("c.Subject() error: %v", err)
	}
	if sa != "_INBOX.batch1.10.ok.1.0.$FI" || sc != "_INBOX.batch1.10.ok.2.0.$FI" {
		t.Fatalf("clones not independent: a=%q c=%q", sa, sc)
	}
	// Mutating a clone must not touch the base.
	if base.slots[base.g.names["seq"]].set {
		t.Fatal("Clone mutation leaked into base builder")
	}
}

func TestBuilder_MustSubjectPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustSubject() did not panic on an incomplete builder")
		}
	}()
	fiReplyB.Builder().MustSubject()
}

func TestBuilder_ZeroAllocAppend(t *testing.T) {
	b := fiReplyB.Builder().
		Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("op", 0)
	buf := make([]byte, 0, 64)

	allocs := testing.AllocsPerRun(200, func() {
		var err error
		buf, err = b.SetInt("seq", 7).AppendSubject(buf[:0])
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("AppendSubject reuse: got %.0f allocs/op, want 0", allocs)
	}
}

func TestBuilder_SubjectAllocs(t *testing.T) {
	b := fiReplyB.Builder().
		Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("op", 0)

	allocs := testing.AllocsPerRun(200, func() {
		_, err := b.SetInt("seq", 7).Subject()
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 2 {
		t.Fatalf("Subject(): got %.0f allocs/op, want <= 2", allocs)
	}
}

// fill binds every capture of fiReplyB to a valid value so a test can then
// override a single field to exercise one validation rule in isolation.
func fill(b *Builder) {
	b.Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("seq", 1).SetInt("op", 0)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func BenchmarkBuilder_AppendSubject(b *testing.B) {
	bld := fiReplyB.Builder().
		Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("op", 0)
	buf := make([]byte, 0, 64)
	seq := 0

	b.ReportAllocs()
	for b.Loop() {
		var err error
		buf, err = bld.SetInt("seq", seq).AppendSubject(buf[:0])
		if err != nil {
			b.Fatal(err)
		}
		seq++
	}
}

func BenchmarkBuilder_Subject(b *testing.B) {
	bld := fiReplyB.Builder().
		Set("prefix", "_INBOX").Set("uuid", "batch1").
		SetInt("flow", 10).Set("gap", "ok").SetInt("op", 0)

	seq := 0
	b.ReportAllocs()
	for b.Loop() {
		_, err := bld.SetInt("seq", seq).Subject()
		if err != nil {
			b.Fatal(err)
		}
		seq++
	}
}
