// Package subject implements a declarative grammar for NATS subjects.
//
// An ADR's positional subject encoding (e.g. the fast-ingest reply subject
// "<prefix>.<uuid>.<flow>.<gap>.<seq>.<op>.$FI") is declared once as a one-line
// pattern string. From that single declaration the engine provides:
//
//   - structural validation + capture:  g.Match(subject) -> (Captures, ok)
//   - typed field extraction:            g.Int("seq") / g.Str("uuid")
//
// which higher layers turn into matchers, correlation keys, and ordering checks
// without any bespoke Go per ADR.
//
// Grammar string syntax (one token per dot-separated position):
//
//	literal        $FI   $JS   STREAM   >        matched exactly
//	named capture  {uuid}                          one token, any value
//	typed capture  {seq:int}                        one token, must parse as int
//	enum capture   {gap:enum(ok,fail)}              one token, must be in the set
//	rest capture   {prefix:rest}                    one-or-more leading/middle tokens
//
// At most one {…:rest} token is permitted (more would be ambiguous). Every other
// token has arity one. Matching anchors the fixed tokens from both ends and lets
// the single rest token absorb the slack in the middle.
package subject

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type kind int

const (
	kindLiteral kind = iota
	kindNamed
	kindInt
	kindEnum
	kindRest
)

type token struct {
	kind    kind
	name    string   // capture name (non-literal tokens)
	literal string   // kindLiteral
	enum    []string // kindEnum
}

// Grammar is a compiled subject pattern. Construct with Parse or MustParse; it is
// immutable and safe for concurrent use.
type Grammar struct {
	spec   string
	tokens []token
	restAt int // index of the rest token, or -1
}

// MustParse is Parse but panics on an invalid spec. Intended for package-level vars:
//
//	var FIReply = subject.MustParse("{prefix:rest}.{uuid}.{flow:int}.{gap:enum(ok,fail)}.{seq:int}.{op:int}.$FI")
func MustParse(spec string) *Grammar {
	g, err := Parse(spec)
	if err != nil {
		panic(fmt.Sprintf("subject.MustParse(%q): %v", spec, err))
	}
	return g
}

// Parse compiles a grammar spec.
func Parse(spec string) (*Grammar, error) {
	if spec == "" {
		return nil, fmt.Errorf("empty subject grammar")
	}

	parts := strings.Split(spec, ".")
	g := &Grammar{spec: spec, restAt: -1}
	seen := make(map[string]bool, len(parts))

	for i, p := range parts {
		tok, err := parseToken(p)
		if err != nil {
			return nil, fmt.Errorf("token %d (%q): %w", i+1, p, err)
		}

		if tok.kind == kindRest {
			if g.restAt != -1 {
				return nil, fmt.Errorf("grammar may contain at most one {…:rest} token")
			}
			g.restAt = len(g.tokens)
		}

		if tok.kind != kindLiteral {
			if tok.name == "" {
				return nil, fmt.Errorf("token %d: capture name is required", i+1)
			}
			if seen[tok.name] {
				return nil, fmt.Errorf("duplicate capture name %q", tok.name)
			}
			seen[tok.name] = true
		}

		g.tokens = append(g.tokens, tok)
	}

	return g, nil
}

func parseToken(p string) (token, error) {
	if len(p) >= 2 && p[0] == '{' && p[len(p)-1] == '}' {
		inner := p[1 : len(p)-1]
		name, typ, _ := strings.Cut(inner, ":")
		name = strings.TrimSpace(name)
		typ = strings.TrimSpace(typ)

		switch {
		case typ == "":
			return token{kind: kindNamed, name: name}, nil
		case typ == "int":
			return token{kind: kindInt, name: name}, nil
		case typ == "rest":
			return token{kind: kindRest, name: name}, nil
		case strings.HasPrefix(typ, "enum(") && strings.HasSuffix(typ, ")"):
			raw := typ[len("enum(") : len(typ)-1]
			set := strings.Split(raw, ",")
			for i := range set {
				set[i] = strings.TrimSpace(set[i])
				if set[i] == "" {
					return token{}, fmt.Errorf("enum contains an empty value")
				}
			}
			if len(set) == 0 {
				return token{}, fmt.Errorf("enum requires at least one value")
			}
			return token{kind: kindEnum, name: name, enum: set}, nil
		default:
			return token{}, fmt.Errorf("unknown capture type %q", typ)
		}
	}

	if p == "" {
		return token{}, fmt.Errorf("empty literal token")
	}
	return token{kind: kindLiteral, literal: p}, nil
}

// Captures holds the bindings produced by a successful Match.
type Captures map[string]string

// Str returns the captured string value for name.
func (c Captures) Str(name string) (string, bool) {
	v, ok := c[name]
	return v, ok
}

// Int returns the captured value for name parsed as an int.
func (c Captures) Int(name string) (int, bool) {
	v, ok := c[name]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Match reports whether subject conforms to the grammar and, if so, returns the
// captured fields. A subject is split on '.'; literals must match exactly, int
// tokens must parse, enum tokens must be in the set, and the single rest token (if
// any) absorbs one-or-more tokens between the head and tail anchors.
func (g *Grammar) Match(subject string) (Captures, bool) {
	if subject == "" {
		return nil, false
	}

	subj := strings.Split(subject, ".")
	caps := make(Captures)

	if g.restAt == -1 {
		if len(subj) != len(g.tokens) {
			return nil, false
		}
		for i, tok := range g.tokens {
			if !matchOne(tok, subj[i], caps) {
				return nil, false
			}
		}
		return caps, true
	}

	prefix := g.tokens[:g.restAt]
	suffix := g.tokens[g.restAt+1:]

	// rest must absorb at least one token.
	if len(subj) < len(prefix)+len(suffix)+1 {
		return nil, false
	}

	for i, tok := range prefix {
		if !matchOne(tok, subj[i], caps) {
			return nil, false
		}
	}
	tailStart := len(subj) - len(suffix)
	for j, tok := range suffix {
		if !matchOne(tok, subj[tailStart+j], caps) {
			return nil, false
		}
	}

	rest := subj[len(prefix):tailStart]
	caps[g.tokens[g.restAt].name] = strings.Join(rest, ".")
	return caps, true
}

func matchOne(tok token, s string, caps Captures) bool {
	switch tok.kind {
	case kindLiteral:
		return s == tok.literal
	case kindNamed:
		caps[tok.name] = s
		return true
	case kindInt:
		if _, err := strconv.Atoi(s); err != nil {
			return false
		}
		caps[tok.name] = s
		return true
	case kindEnum:
		if slices.Contains(tok.enum, s) {
			caps[tok.name] = s
			return true
		}
		return false
	default:
		return false
	}
}

// Matches is a convenience reporting only whether subject conforms.
func (g *Grammar) Matches(subject string) bool {
	_, ok := g.Match(subject)
	return ok
}

// Str returns an extractor that pulls the named capture (as a string) from a
// subject, reporting ok=false if the subject does not match or lacks the capture.
// Feeds correlation keys and cross-field checks in higher layers.
func (g *Grammar) Str(name string) func(subject string) (string, bool) {
	return func(subject string) (string, bool) {
		caps, ok := g.Match(subject)
		if !ok {
			return "", false
		}
		return caps.Str(name)
	}
}

// Int returns an extractor that pulls the named capture as an int. Feeds the
// numeric sequence combinators (BeMonotonic / BeContiguousFrom).
func (g *Grammar) Int(name string) func(subject string) (int, bool) {
	return func(subject string) (int, bool) {
		caps, ok := g.Match(subject)
		if !ok {
			return 0, false
		}
		return caps.Int(name)
	}
}

// String returns the original grammar spec (useful in failure messages).
func (g *Grammar) String() string { return g.spec }
