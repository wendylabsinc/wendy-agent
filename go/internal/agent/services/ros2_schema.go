package services

import (
	"fmt"
	"strconv"
	"strings"
)

// ros2OwnFields returns a message type's own field lines from `ros2 interface
// show` output: the lines with no leading whitespace (nested types are emitted
// indented). Blank lines and pure comments are dropped; constants and field
// lines are kept verbatim.
func ros2OwnFields(show string) []string {
	var out []string
	for _, line := range strings.Split(show, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue // blank or nested-expansion line
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// ros2ComplexTypesIn returns the distinct non-primitive message types referenced
// by the given field lines, in first-seen order. A type is complex iff its type
// token contains '/'. Array and bounded suffixes ("[]", "[36]", "<=10") are
// stripped before testing.
func ros2ComplexTypesIn(fields []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		typeTok := f
		if i := strings.IndexAny(f, " \t"); i >= 0 {
			typeTok = f[:i]
		}
		// strip array / bound suffixes: "geometry_msgs/Point[]" -> "geometry_msgs/Point"
		if i := strings.IndexAny(typeTok, "[<"); i >= 0 {
			typeTok = typeTok[:i]
		}
		if !strings.Contains(typeTok, "/") {
			continue // primitive
		}
		if seen[typeTok] {
			continue
		}
		seen[typeTok] = true
		out = append(out, typeTok)
	}
	return out
}

// normalizeMsgType turns a 2-part field reference ("std_msgs/Header") into the
// 3-part form `ros2 interface show` expects ("std_msgs/msg/Header"). A type
// already carrying an interface kind segment (msg/srv/action) is returned as-is.
func normalizeMsgType(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) == 2 {
		return parts[0] + "/msg/" + parts[1]
	}
	return ref
}

// parsePythonBytesLiteral decodes a Python bytes repr such as b'\x00\x01ABC'
// (single quote or double quote) into its raw bytes. It handles the escape
// forms CPython emits for bytes: \xNN, \n, \r, \t, \\, \', \", and \0.
func parsePythonBytesLiteral(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) < 3 || s[0] != 'b' || (s[1] != '\'' && s[1] != '"') {
		return nil, fmt.Errorf("not a python bytes literal: %q", s)
	}
	quote := s[1]
	if s[len(s)-1] != quote {
		return nil, fmt.Errorf("unterminated python bytes literal: %q", s)
	}
	body := s[2 : len(s)-1]
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(body) {
			return nil, fmt.Errorf("trailing backslash in %q", s)
		}
		switch body[i] {
		case 'x':
			if i+2 >= len(body) {
				return nil, fmt.Errorf("truncated \\x escape in %q", s)
			}
			v, err := strconv.ParseUint(body[i+1:i+3], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad \\x escape in %q: %w", s, err)
			}
			out = append(out, byte(v))
			i += 2
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case '\\':
			out = append(out, '\\')
		case '\'':
			out = append(out, '\'')
		case '"':
			out = append(out, '"')
		case '0':
			out = append(out, 0)
		default:
			return nil, fmt.Errorf("unsupported escape \\%c in %q", body[i], s)
		}
	}
	return out, nil
}

// assembleROS2MsgSchema joins a root message body and its dependency bodies into
// the concatenated ros2msg schema format Foxglove consumes. Dependencies are
// keyed by their 2-part name ("pkg/Type") and emitted in `order`. Each
// dependency body should contain the type's own field lines with no leading or
// trailing blank lines; a trailing newline is trimmed defensively so it can
// never introduce a blank line before the next "====" separator.
func assembleROS2MsgSchema(rootBody string, depBodies map[string]string, order []string) string {
	sep := strings.Repeat("=", 80)
	var b strings.Builder
	b.WriteString(rootBody)
	for _, name := range order {
		b.WriteString("\n")
		b.WriteString(sep)
		b.WriteString("\nMSG: ")
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(depBodies[name], "\n"))
	}
	return b.String()
}
