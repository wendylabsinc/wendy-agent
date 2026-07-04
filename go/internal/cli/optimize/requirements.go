package optimize

import (
	"regexp"
	"strings"
)

// Requirement is one parsed line of requirements.txt.
type Requirement struct {
	Name        string // lower-cased package name
	VersionSpec string // e.g. "==2.3.0", ">=1.26", "" if none
	LocalLabel  string // PEP 440 local label after '+', e.g. "cpu", "cu118"
	Line        int
}

// Requirements is a parsed requirements.txt.
type Requirements struct {
	Path      string
	Raw       string
	Packages  []Requirement
	IndexURLs []string
}

// namePattern matches the leading distribution name of a requirement line.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// ParseRequirements parses requirements.txt bytes leniently.
func ParseRequirements(path string, data []byte) *Requirements {
	r := &Requirements{Path: path, Raw: string(data)}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip inline comments.
		if h := strings.Index(line, " #"); h >= 0 {
			line = strings.TrimSpace(line[:h])
		}

		if strings.HasPrefix(line, "--index-url") || strings.HasPrefix(line, "--extra-index-url") || strings.HasPrefix(line, "-i ") || strings.HasPrefix(line, "-i=") {
			fields := strings.Fields(line)
			first := fields[0]
			if eq := strings.IndexByte(first, '='); eq >= 0 {
				if url := first[eq+1:]; url != "" {
					r.IndexURLs = append(r.IndexURLs, url)
				}
			} else if len(fields) >= 2 {
				r.IndexURLs = append(r.IndexURLs, fields[len(fields)-1])
			}
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue // other pip options
		}

		name := namePattern.FindString(line)
		if name == "" {
			continue
		}
		rest := line[len(name):]
		req := Requirement{Name: strings.ToLower(name), Line: idx + 1}

		// Split off extras "[...]" if present.
		if strings.HasPrefix(rest, "[") {
			if end := strings.Index(rest, "]"); end >= 0 {
				rest = rest[end+1:]
			}
		}
		// Local label: "+label" appears within the version spec.
		spec := rest
		if plus := strings.Index(spec, "+"); plus >= 0 {
			label := spec[plus+1:]
			// label ends at first non [A-Za-z0-9.] char
			labelEnd := len(label)
			for k, c := range label {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.') {
					labelEnd = k
					break
				}
			}
			req.LocalLabel = label[:labelEnd]
			spec = spec[:plus]
		}
		req.VersionSpec = strings.TrimSpace(spec)
		r.Packages = append(r.Packages, req)
	}
	return r
}
