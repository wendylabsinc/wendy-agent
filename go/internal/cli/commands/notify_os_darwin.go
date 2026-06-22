//go:build darwin

package commands

import (
	"fmt"
	"strings"
)

func sendOSNotification(title, body string) error {
	script := fmt.Sprintf(
		`display notification %s with title %s`,
		quoteAppleScript(body),
		quoteAppleScript(title),
	)
	cmd := execCommand("osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// quoteAppleScript wraps s in AppleScript double-quoted string literals,
// escaping backslashes and double-quotes.
func quoteAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
