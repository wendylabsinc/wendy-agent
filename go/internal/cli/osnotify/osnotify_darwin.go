package osnotify

import "fmt"

func notify(title, body string) {
	if _, err := lookPath("osascript"); err == nil {
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = runner("osascript", "-e", script)
		return
	}
	if _, err := lookPath("terminal-notifier"); err == nil {
		_ = runner("terminal-notifier", "-title", title, "-message", body)
	}
}
