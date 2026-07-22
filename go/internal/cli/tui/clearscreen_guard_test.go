package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This guard exists because of a real incident: BubbleTable.Update once
// answered tea.WindowSizeMsg with tea.ClearScreen. bubbletea sends a
// WindowSizeMsg the moment every program starts, so every inline (non
// alt-screen) TUI — wendy discover, cloud discover, the wifi/bluetooth tables,
// and every picker — began by emitting CSI 2J, erasing the user's entire
// visible terminal in place. Content erased that way never reaches scrollback,
// so whatever the user was looking at was destroyed.
//
// Screen-clearing is almost never the right tool in this codebase:
//   - On resize, bubbletea's standard renderer already invalidates its cache
//     and repaints every line, erasing leftovers itself.
//   - Full-screen TUIs should use tea.WithAltScreen(); clearing the alternate
//     screen is then unnecessary, and the primary screen is restored on exit.
//
// If you believe you have a legitimate use (e.g. inside an alt-screen-only
// program), add the file to the allowlist below with a comment explaining why
// the user's terminal content is provably not at risk.
func TestNoScreenClearingInCLISource(t *testing.T) {
	forbidden := []string{
		"tea.ClearScreen",
		"EraseEntireScreen",
		"\\x1b[2J",
		"\\033[2J",
	}
	allowlist := map[string]bool{
		// (empty — nothing in internal/cli may clear the user's screen today)
	}

	cliRoot := ".." // this package lives at internal/cli/tui
	var violations []string
	err := filepath.Walk(cliRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(cliRoot, path)
		if err != nil {
			return err
		}
		if allowlist[filepath.ToSlash(rel)] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // comments may mention the forbidden names
			}
			for _, f := range forbidden {
				if strings.Contains(line, f) {
					violations = append(violations, fmt.Sprintf("%s:%d: %s (matched %q)",
						filepath.ToSlash(rel), i+1, strings.TrimSpace(line), f))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/cli: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("screen-clearing escape/command found in internal/cli source; "+
			"this destroys the user's visible terminal content in inline TUIs "+
			"(see this test's doc comment for the incident and alternatives):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
