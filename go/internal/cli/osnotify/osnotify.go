// Package osnotify sends best-effort OS-level desktop notifications.
//
// This is currently a no-op stub; a follow-up task wires it up to real
// platform notification mechanisms (e.g. notify-send on Linux,
// osascript/UserNotifications on macOS).
package osnotify

// Notify shows a best-effort OS notification with the given title and body.
// It is currently a no-op.
func Notify(title, body string) {}
