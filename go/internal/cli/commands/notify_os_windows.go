//go:build windows

package commands

import (
	"fmt"
	"strings"
)

func sendOSNotification(title, body string) error {
	// Use PowerShell Windows.UI.Notifications to display a toast.
	script := fmt.Sprintf(`
$null = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
$template = [Windows.UI.Notifications.ToastTemplateType]::ToastText02
$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent($template)
$xml.GetElementsByTagName('text').Item(0).InnerText = %s
$xml.GetElementsByTagName('text').Item(1).InnerText = %s
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Wendy').Show($toast)
`, psPquote(title), psPquote(body))

	cmd := execCommand("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell toast: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psPquote wraps s in PowerShell single-quoted string literals, escaping
// single-quotes by doubling them.
func psPquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
