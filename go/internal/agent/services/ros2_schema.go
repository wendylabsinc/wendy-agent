package services

import "strings"

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

// assembleROS2MsgSchema joins a root message body and its dependency bodies into
// the concatenated ros2msg schema format Foxglove consumes. Dependencies are
// keyed by their 2-part name ("pkg/Type") and emitted in `order`.
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
		b.WriteString(depBodies[name])
	}
	return b.String()
}
