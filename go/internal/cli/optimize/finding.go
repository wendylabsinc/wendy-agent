package optimize

import (
	"fmt"
	"strconv"
)

// Severity ranks how strongly a finding should be acted on.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	default:
		return "unknown"
	}
}

// ParseSeverity maps a CLI string to a Severity.
func ParseSeverity(s string) (Severity, error) {
	switch s {
	case "info":
		return SeverityInfo, nil
	case "warning":
		return SeverityWarning, nil
	case "error":
		return SeverityError, nil
	default:
		return SeverityInfo, fmt.Errorf("invalid severity %q (want info, warning, or error)", s)
	}
}

// MarshalJSON encodes Severity as its lowercase string name.
func (s Severity) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(s.String())), nil
}

// UnmarshalJSON decodes a Severity from its string name.
func (s *Severity) UnmarshalJSON(data []byte) error {
	str, err := strconv.Unquote(string(data))
	if err != nil {
		return err
	}
	v, err := ParseSeverity(str)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Loc points at a source location for a finding.
type Loc struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// FixOp is the kind of edit a Fix performs.
type FixOp int

const (
	// FixCreateFile creates File with New as its full contents (skipped if File exists).
	FixCreateFile FixOp = iota
	// FixReplaceLine replaces line Line of File: it must currently equal Old, and becomes New.
	FixReplaceLine
)

// Fix is a deterministic, safe edit attached to a Finding. Nil means report-only.
type Fix struct {
	Description string `json:"description"`
	Op          FixOp  `json:"op"`
	File        string `json:"file"`
	Line        int    `json:"line,omitempty"`
	Old         string `json:"old,omitempty"`
	New         string `json:"new"`
}

// Finding is a single optimization issue.
type Finding struct {
	Analyzer string   `json:"analyzer"`
	Target   string   `json:"target,omitempty"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Location *Loc     `json:"location,omitempty"`
	Fix      *Fix     `json:"fix,omitempty"`
}
