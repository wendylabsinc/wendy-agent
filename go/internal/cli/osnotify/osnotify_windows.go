package osnotify

import "fmt"

func notify(title, body string) {
	if _, err := lookPath("powershell"); err == nil {
		script := fmt.Sprintf(
			`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');`+
				`$n=New-Object System.Windows.Forms.NotifyIcon;$n.Icon=[System.Drawing.SystemIcons]::Information;`+
				`$n.Visible=$true;$n.ShowBalloonTip(5000,%q,%q,[System.Windows.Forms.ToolTipIcon]::Info)`,
			title, body)
		_ = runner("powershell", "-NoProfile", "-Command", script)
	}
}
