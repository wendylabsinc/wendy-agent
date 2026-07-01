// Package foxglovecdr implements a minimal ROS 2 CDR (Common Data
// Representation) codec together with a ros2msg schema parser and a YAML
// bridge. It exists to translate the binary CDR service-call/publish payloads
// that Foxglove sends over its WebSocket protocol into the YAML message
// arguments that the `ros2` CLI accepts (and back again for responses).
//
// The codec implements OMG CDR version 1 in its PLAIN_CDR / XCDR1 form, which
// is what ROS 2's default (rmw) middleware uses on the wire for un-annotated
// messages. Only the subset needed for the bridge is implemented: primitives,
// strings, fixed arrays, sequences and nested (inlined) messages. Extended
// CDR / parameter-list (PL_CDR) encodings are rejected explicitly.
//
// All functions in this package are pure: they take their inputs as arguments
// and return values or errors, performing no I/O and touching no globals.
package foxglovecdr

import (
	"fmt"
	"strings"
)

// ArrayKind classifies how a field's elements are laid out on the wire.
type ArrayKind int

const (
	// ArrayNone is a scalar field (a single element).
	ArrayNone ArrayKind = iota
	// ArrayFixed is a fixed-length array `T[N]`: exactly N elements are laid
	// out back-to-back with NO count prefix on the wire.
	ArrayFixed
	// ArraySequence is a sequence `T[]` or bounded sequence `T[<=N]`: a uint32
	// element count (aligned to 4) precedes the elements. Bounded and unbounded
	// sequences share the same wire form; the bound is a validation hint only.
	ArraySequence
)

// Field is a single wire-visible member of a message. Constants (`T NAME=val`)
// are not wire-visible and are excluded during parsing; default values
// (`T name val`) are wire-visible and kept (the default itself is ignored).
type Field struct {
	// Name is the field's identifier.
	Name string
	// Type is the base type with any array / bound suffix stripped, e.g.
	// "float64", "string" or "geometry_msgs/Point". A Type containing '/' is a
	// nested-message reference keyed into the dependency map.
	Type string
	// Array classifies the field's cardinality.
	Array ArrayKind
	// ArrayLen is the element count when Array == ArrayFixed; otherwise 0.
	ArrayLen int
}

// Message is an ordered list of wire-visible fields.
type Message struct {
	Fields []Field
}

// primitiveTypes is the set of ROS 2 primitive type names. Any other bare
// (slash-free) token is treated as an error during parsing so that malformed
// schemas fail loudly rather than being silently decoded wrong.
var primitiveTypes = map[string]bool{
	"bool": true, "byte": true, "char": true,
	"int8": true, "uint8": true,
	"int16": true, "uint16": true,
	"int32": true, "uint32": true,
	"int64": true, "uint64": true,
	"float32": true, "float64": true,
	"string": true, "wstring": true,
}

const separator = "================================================================================" // 80 '='

// ParseSchema parses a concatenated ros2msg schema (as produced by the agent's
// GetMessageDefinition). The layout is: the root message body, then for each
// dependency a separator line of exactly 80 '=' characters, a `MSG: pkg/Type`
// header line, and that type's body.
//
// It returns the dependency map keyed by the 2-part "pkg/Type" name from each
// MSG: header, plus the parsed root message (the body before the first
// separator).
func ParseSchema(concatenated string) (schema map[string]*Message, root *Message, err error) {
	lines := strings.Split(concatenated, "\n")

	// Split into blocks at each separator line. Block 0 is the root body; each
	// subsequent block begins with a "MSG: pkg/Type" header.
	var blocks [][]string
	cur := []string{}
	for _, line := range lines {
		if strings.TrimSpace(line) == separator {
			blocks = append(blocks, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, line)
	}
	blocks = append(blocks, cur)

	root, err = parseBody(blocks[0])
	if err != nil {
		return nil, nil, fmt.Errorf("root message: %w", err)
	}

	schema = make(map[string]*Message)
	for _, block := range blocks[1:] {
		// The first non-blank line of a dependency block is its MSG: header.
		name := ""
		var body []string
		for i, line := range block {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if !strings.HasPrefix(t, "MSG:") {
				return nil, nil, fmt.Errorf("dependency block missing MSG: header, got %q", t)
			}
			name = strings.TrimSpace(strings.TrimPrefix(t, "MSG:"))
			body = block[i+1:]
			break
		}
		if name == "" {
			// Empty trailing block (e.g. a trailing newline); ignore.
			continue
		}
		msg, perr := parseBody(body)
		if perr != nil {
			return nil, nil, fmt.Errorf("message %q: %w", name, perr)
		}
		schema[name] = msg
	}

	return schema, root, nil
}

// parseBody parses the field lines of a single message body.
func parseBody(lines []string) (*Message, error) {
	msg := &Message{}
	for _, raw := range lines {
		line := stripComment(raw)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f, ok, err := parseField(line)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // constant line; not wire-visible
		}
		msg.Fields = append(msg.Fields, f)
	}
	return msg, nil
}

// stripComment removes a trailing `#` comment. It does not attempt to handle
// '#' inside string default values; ros2msg comments are line comments and
// string defaults with embedded '#' do not occur in the schemas we consume.
func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// parseField parses a single non-empty, comment-stripped field line. It returns
// ok=false (with no error) for constant declarations, which are not on the
// wire. A field line is `TYPE NAME [DEFAULT...]`; a constant is `TYPE NAME=VAL`
// (an '=' immediately attached to the name token).
func parseField(line string) (Field, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Field{}, false, fmt.Errorf("malformed field line %q", line)
	}
	typeTok := fields[0]
	nameTok := fields[1]

	// A constant has the form `NAME=VALUE`; the '=' is attached to the name
	// token (possibly with the value in the same or following tokens). Any '='
	// in the second token marks this as a constant, which we skip.
	if strings.Contains(nameTok, "=") {
		return Field{}, false, nil
	}

	base, kind, arrayLen, err := parseType(typeTok)
	if err != nil {
		return Field{}, false, err
	}
	return Field{Name: nameTok, Type: base, Array: kind, ArrayLen: arrayLen}, true, nil
}

// parseType decomposes a type token into its base type and array kind. It
// handles the ros2msg suffix forms:
//
//	T          -> scalar
//	T[N]       -> fixed array of N
//	T[]        -> unbounded sequence
//	T[<=N]     -> bounded sequence (same wire form as unbounded)
//	string<=N  -> bounded string (same wire form as string; no brackets)
func parseType(tok string) (base string, kind ArrayKind, arrayLen int, err error) {
	// Bounded string: `string<=N` or `wstring<=N`. The bound carries no wire
	// form, so strip it and treat as a scalar string.
	if i := strings.Index(tok, "<="); i >= 0 && !strings.Contains(tok, "[") {
		base = tok[:i]
		if !isKnownBase(base) {
			return "", 0, 0, fmt.Errorf("unknown type %q", tok)
		}
		return base, ArrayNone, 0, nil
	}

	// Array/sequence forms carry a '[' ... ']' suffix.
	if lb := strings.IndexByte(tok, '['); lb >= 0 {
		rb := strings.IndexByte(tok, ']')
		if rb < lb {
			return "", 0, 0, fmt.Errorf("malformed array type %q", tok)
		}
		base = tok[:lb]
		inner := tok[lb+1 : rb]
		if !isKnownBase(base) {
			return "", 0, 0, fmt.Errorf("unknown type %q", tok)
		}
		switch {
		case inner == "":
			// T[] : unbounded sequence.
			return base, ArraySequence, 0, nil
		case strings.HasPrefix(inner, "<="):
			// T[<=N] : bounded sequence, same wire form as unbounded.
			return base, ArraySequence, 0, nil
		default:
			// T[N] : fixed array.
			var n int
			if _, serr := fmt.Sscanf(inner, "%d", &n); serr != nil {
				return "", 0, 0, fmt.Errorf("malformed array length in %q", tok)
			}
			return base, ArrayFixed, n, nil
		}
	}

	// Bare scalar.
	if !isKnownBase(tok) {
		return "", 0, 0, fmt.Errorf("unknown type %q", tok)
	}
	return tok, ArrayNone, 0, nil
}

// isKnownBase reports whether base is a supported primitive or a nested message
// reference (a token containing '/').
func isKnownBase(base string) bool {
	if strings.Contains(base, "/") {
		return true
	}
	return primitiveTypes[base]
}
