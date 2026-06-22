//go:build linux

package commands

import (
	"fmt"
	"log"
	"strings"
)

func sendOSNotification(title, body string) error {
	if _, err := execLookPath("notify-send"); err != nil {
		log.Printf("wendy notify: notify-send not found, skipping OS notification: %v", err)
		return nil
	}
	cmd := execCommand("notify-send", title, body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify-send: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
