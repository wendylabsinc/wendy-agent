package optimize

import "strings"

// Instruction is one parsed Dockerfile instruction.
type Instruction struct {
	Cmd   string   // upper-cased command, e.g. "RUN"
	Args  string   // argument text with line continuations joined by single spaces
	Flags []string // instruction flags, e.g. "--mount=type=cache,target=/x", "--platform=linux/amd64"
	Line  int      // 1-based line where the instruction starts
}

// Dockerfile is a parsed Dockerfile.
type Dockerfile struct {
	Path         string
	Lines        []string // raw lines, no trailing empty element
	Instructions []Instruction
}

// ParseDockerfile parses Dockerfile bytes. It is lenient: malformed lines are
// best-effort, never fatal.
func ParseDockerfile(path string, data []byte) *Dockerfile {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rawLines := strings.Split(text, "\n")
	// Drop a single trailing empty element from a final newline.
	if n := len(rawLines); n > 0 && rawLines[n-1] == "" {
		rawLines = rawLines[:n-1]
	}

	df := &Dockerfile{Path: path, Lines: rawLines}

	i := 0
	for i < len(rawLines) {
		startLine := i + 1 // 1-based
		line := rawLines[i]
		trimmed := strings.TrimSpace(line)
		i++

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Join continuation lines (current line ends with backslash).
		joined := strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\"))
		for strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") && i < len(rawLines) {
			next := rawLines[i]
			i++
			joined += " " + strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(next, " \t"), "\\"))
			line = next
		}
		joined = strings.TrimSpace(joined)
		if joined == "" {
			continue
		}

		fields := strings.Fields(joined)
		inst := Instruction{Cmd: strings.ToUpper(fields[0]), Line: startLine}
		rest := fields[1:]
		// Leading --flags belong to the instruction.
		j := 0
		for j < len(rest) && strings.HasPrefix(rest[j], "--") {
			inst.Flags = append(inst.Flags, rest[j])
			j++
		}
		inst.Args = strings.Join(rest[j:], " ")
		df.Instructions = append(df.Instructions, inst)
	}
	return df
}
