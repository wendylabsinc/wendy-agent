package optimize

import (
	"fmt"
	"os"
	"strings"
)

// AppliedFix records the outcome of applying one Fix.
type AppliedFix struct {
	Fix     Fix
	Applied bool
	Reason  string // populated when Applied is false
}

// ApplyFixes applies every finding's non-nil Fix. It is idempotent.
func ApplyFixes(findings []Finding) ([]AppliedFix, error) {
	var results []AppliedFix
	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
		fx := *f.Fix
		switch fx.Op {
		case FixCreateFile:
			if fileExists(fx.File) {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "file already exists"})
				continue
			}
			if err := os.WriteFile(fx.File, []byte(fx.New), 0o644); err != nil {
				return results, fmt.Errorf("creating %s: %w", fx.File, err)
			}
			results = append(results, AppliedFix{Fix: fx, Applied: true})

		case FixReplaceLine:
			data, err := os.ReadFile(fx.File)
			if err != nil {
				return results, fmt.Errorf("reading %s: %w", fx.File, err)
			}
			text := strings.ReplaceAll(string(data), "\r\n", "\n")
			trailingNL := strings.HasSuffix(text, "\n")
			body := text
			if trailingNL {
				body = strings.TrimSuffix(text, "\n")
			}
			lines := strings.Split(body, "\n")
			idx := fx.Line - 1
			if idx < 0 || idx >= len(lines) || lines[idx] != fx.Old {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "already applied or line changed"})
				continue
			}
			lines[idx] = fx.New
			out := strings.Join(lines, "\n")
			if trailingNL {
				out += "\n"
			}
			if err := os.WriteFile(fx.File, []byte(out), 0o644); err != nil {
				return results, fmt.Errorf("writing %s: %w", fx.File, err)
			}
			results = append(results, AppliedFix{Fix: fx, Applied: true})

		default:
			results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "unknown fix op"})
		}
	}
	return results, nil
}
