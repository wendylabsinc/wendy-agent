package osnotify

func notify(title, body string) {
	if _, err := lookPath("notify-send"); err == nil {
		_ = runner("notify-send", title, body)
	}
}
