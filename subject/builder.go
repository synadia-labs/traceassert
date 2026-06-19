package subject

import (
	"fmt"
	"slices"
	"strconv"
)

// maxIntChars is an upper bound on the rendered width of an int, used to size
// the output buffer. It covers the widest value, len("-9223372036854775808").
const maxIntChars = 20

// slot holds a single set capture value for a Builder. A value is either a
// string (str) or an int (ival, with isInt set); err records why the value is
// invalid, computed once at set time so the render path never re-validates.
type slot struct {
	str   string
	ival  int
	isInt bool
	set   bool
	err   error
}

// Builder is a reusable subject builder ("prepared statement") derived from a
// Grammar via Grammar.Builder. It is the inverse of Grammar.Match: set capture
// values and render a subject, with literal tokens filled automatically.
//
// The intended pattern is to set the stable fields once and then vary the
// remaining field(s) in a loop, rendering each iteration:
//
//	b := g.Builder().Set("uuid", id).SetInt("flow", flow)
//	buf := make([]byte, 0, 64)
//	for seq := 1; seq <= n; seq++ {
//		buf, err = b.SetInt("seq", seq).AppendSubject(buf[:0])
//		// ... use buf ...
//	}
//
// A Builder is mutable and NOT safe for concurrent use. To fan a configured
// builder out across goroutines, Clone it per goroutine.
type Builder struct {
	g        *Grammar
	slots    []slot
	fixedLen int   // literal bytes + dot separators, known at construction
	err      error // sticky error from a Set/SetInt with an unknown capture name
}

// Builder returns a reusable Builder for g. Set capture values with Set,
// SetInt, or SetCaptures and render with Subject or the zero-allocation
// AppendSubject.
func (g *Grammar) Builder() *Builder {
	b := &Builder{
		g:     g,
		slots: make([]slot, len(g.tokens)),
	}
	if len(g.tokens) > 0 {
		b.fixedLen = len(g.tokens) - 1 // dot separators between tokens
	}
	for i := range g.tokens {
		if g.tokens[i].kind == kindLiteral {
			b.fixedLen += len(g.tokens[i].literal)
		}
	}
	return b
}

// Set binds the named capture to a string value. It is chainable. An unknown
// capture name and an invalid value are not reported here; they surface from
// Validate, Subject, and AppendSubject so the fluent chain stays clean.
func (b *Builder) Set(name, value string) *Builder {
	idx, ok := b.g.names[name]
	if !ok {
		b.recordUnknown(name)
		return b
	}
	b.slots[idx] = slot{str: value, set: true, err: b.validateValue(idx, value)}
	return b
}

// SetInt binds the named capture to an int value. The int is stored as an int
// and rendered directly into the output, so it costs no allocation. It is
// chainable; see Set for error handling.
func (b *Builder) SetInt(name string, n int) *Builder {
	idx, ok := b.g.names[name]
	if !ok {
		b.recordUnknown(name)
		return b
	}
	s := slot{ival: n, isInt: true, set: true}
	if b.g.tokens[idx].kind == kindEnum {
		s.err = b.validateIntEnum(idx, n)
	}
	b.slots[idx] = s
	return b
}

// SetCaptures binds every capture present in c, the typical source being a
// prior Grammar.Match. This closes the match/build round-trip: capture a
// subject, change a field, re-render. It is chainable.
func (b *Builder) SetCaptures(c Captures) *Builder {
	for name, value := range c {
		b.Set(name, value)
	}
	return b
}

// Reset clears all set values and any sticky error so the Builder can be reused
// from scratch. It is chainable.
func (b *Builder) Reset() *Builder {
	for i := range b.slots {
		b.slots[i] = slot{}
	}
	b.err = nil
	return b
}

// Clone returns an independent copy of the Builder, including all set values.
// Because a Builder is not safe for concurrent use, Clone is how a configured
// base builder is shared across goroutines.
func (b *Builder) Clone() *Builder {
	return &Builder{
		g:        b.g,
		slots:    slices.Clone(b.slots),
		fixedLen: b.fixedLen,
		err:      b.err,
	}
}

// Validate reports the first reason the builder cannot render a valid subject:
// an unknown capture name, a capture that was never set, or a value that is not
// a valid token for its position. It returns nil when Subject would succeed.
// The success path performs no allocation.
func (b *Builder) Validate() error {
	if b.err != nil {
		return b.err
	}
	for i := range b.g.tokens {
		tok := b.g.tokens[i]
		if tok.kind == kindLiteral {
			continue
		}
		s := b.slots[i]
		if !s.set {
			return fmt.Errorf("subject %q: capture %q is not set", b.g.spec, tok.name)
		}
		if s.err != nil {
			return fmt.Errorf("subject %q: %w", b.g.spec, s.err)
		}
	}
	return nil
}

// Subject renders the bound subject, or returns an error from Validate. It
// allocates the returned string (two allocations: the buffer and the string
// copy); for a hot loop prefer AppendSubject with a reused buffer.
func (b *Builder) Subject() (string, error) {
	err := b.Validate()
	if err != nil {
		return "", err
	}
	buf := b.appendUnchecked(make([]byte, 0, b.size()))
	return string(buf), nil
}

// MustSubject is Subject but panics on error. It is intended for call sites that
// bind only static values, where a failure is a programmer error.
func (b *Builder) MustSubject() string {
	s, err := b.Subject()
	if err != nil {
		panic(fmt.Sprintf("subject: %v", err))
	}
	return s
}

// AppendSubject validates the builder and appends the rendered subject to dst,
// returning the (possibly reallocated) extended slice. It is the building block
// for high-throughput rendering.
//
// dst does not need to be pre-sized: if its spare capacity is too small it is
// grown automatically, exactly like the builtin append. Use the standard append
// idiom and always reassign the result:
//
//	buf, err = b.AppendSubject(buf[:0])
//
// Passing buf[:0] reuses the buffer's existing storage (resetting length while
// keeping capacity); passing buf without [:0] appends after the current
// contents. Once the buffer is large enough to hold the subject, and with int
// fields set via SetInt, rendering performs no allocation. A nil or undersized
// dst still renders correctly, paying only the allocation that grows it.
//
// On error dst is returned unchanged.
func (b *Builder) AppendSubject(dst []byte) ([]byte, error) {
	err := b.Validate()
	if err != nil {
		return dst, err
	}
	return b.appendUnchecked(dst), nil
}

// size returns the number of bytes the rendered subject will occupy. int values
// are over-estimated at maxIntChars so the single Grow in appendUnchecked never
// has to reallocate mid-render.
func (b *Builder) size() int {
	n := b.fixedLen
	for i := range b.slots {
		s := b.slots[i]
		if !s.set {
			continue
		}
		if s.isInt {
			n += maxIntChars
			continue
		}
		n += len(s.str)
	}
	return n
}

// appendUnchecked renders the subject into dst. Callers must have validated the
// builder first.
func (b *Builder) appendUnchecked(dst []byte) []byte {
	dst = slices.Grow(dst, b.size())
	for i := range b.g.tokens {
		if i > 0 {
			dst = append(dst, '.')
		}
		tok := b.g.tokens[i]
		if tok.kind == kindLiteral {
			dst = append(dst, tok.literal...)
			continue
		}
		s := b.slots[i]
		if s.isInt {
			dst = strconv.AppendInt(dst, int64(s.ival), 10)
			continue
		}
		dst = append(dst, s.str...)
	}
	return dst
}

// recordUnknown stores the first unknown-capture error. A bad name is a static
// programmer error, so the sticky semantics are intended: it persists until
// Reset.
func (b *Builder) recordUnknown(name string) {
	if b.err == nil {
		b.err = fmt.Errorf("subject %q: unknown capture %q", b.g.spec, name)
	}
}

// validateValue checks that value is a legal binding for the token at idx. The
// rules are deliberately stricter than Grammar.Match (which accepts any string
// for a named position): a built subject must round-trip back through Match, so
// every non-rest value must be a single concrete token.
func (b *Builder) validateValue(idx int, value string) error {
	tok := b.g.tokens[idx]
	if tok.kind == kindRest {
		err := validateRest(value)
		if err != nil {
			return fmt.Errorf("capture %q: %w", tok.name, err)
		}
		return nil
	}

	err := validateToken(value)
	if err != nil {
		return fmt.Errorf("capture %q: %w", tok.name, err)
	}

	switch tok.kind {
	case kindInt:
		_, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("capture %q: %q is not an int", tok.name, value)
		}
	case kindEnum:
		if !slices.Contains(tok.enum, value) {
			return fmt.Errorf("capture %q: %q is not one of %v", tok.name, value, tok.enum)
		}
	}
	return nil
}

// validateIntEnum checks that the decimal rendering of n is a member of the
// enum at idx, without allocating.
func (b *Builder) validateIntEnum(idx, n int) error {
	tok := b.g.tokens[idx]
	var buf [maxIntChars]byte
	rendered := strconv.AppendInt(buf[:0], int64(n), 10)
	for _, e := range tok.enum {
		if bytesEqualString(rendered, e) {
			return nil
		}
	}
	return fmt.Errorf("capture %q: %d is not one of %v", tok.name, n, tok.enum)
}

// validateToken reports whether s is a single concrete NATS subject token: it
// must be non-empty and free of the dot separator, whitespace, ASCII control
// characters, and the '*' / '>' wildcards.
func validateToken(s string) error {
	if s == "" {
		return fmt.Errorf("empty token")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			return fmt.Errorf("token %q contains a '.' separator", s)
		case c <= ' ' || c == 0x7f:
			return fmt.Errorf("token %q contains whitespace or a control character", s)
		case c == '*' || c == '>':
			return fmt.Errorf("token %q contains the wildcard %q", s, string(c))
		}
	}
	return nil
}

// validateRest validates a value bound to a {…:rest} token. Such a value may
// span several dot-separated tokens, but every segment must itself be a valid
// token, which rules out empty segments (leading, trailing, or doubled dots).
func validateRest(s string) error {
	if s == "" {
		return fmt.Errorf("empty rest value")
	}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '.' {
			continue
		}
		err := validateToken(s[start:i])
		if err != nil {
			return fmt.Errorf("rest value %q: %w", s, err)
		}
		start = i + 1
	}
	return nil
}

// bytesEqualString reports whether b and s hold the same bytes, without the
// allocation that string(b) == s would incur inside a loop.
func bytesEqualString(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}
