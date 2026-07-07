//go:build windows

package env

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	powershellOnce sync.Once
	powershellPath string
)

// PowershellExe returns the absolute path to powershell.exe, resolved lazily on
// first call. Looking it up via PATH is unsafe: in a 32-bit wendy.exe process
// running on 64-bit Windows, PATH-resolved `powershell` lands in SysWOW64,
// which ships a legacy Storage module that rejects modern parameters like
// -Confirm on Set-Disk. Resolving through System32 (or Sysnative when running
// under WoW64) ensures we always invoke the host-architecture PowerShell with
// the current Storage module.
func PowershellExe() string {
	powershellOnce.Do(func() {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		// Sysnative is a virtual alias that exists only inside a 32-bit (WoW64)
		// process and points at the real System32. Prefer it so 32-bit builds of
		// wendy.exe still launch 64-bit PowerShell.
		candidates := []string{
			filepath.Join(systemRoot, "Sysnative", "WindowsPowerShell", "v1.0", "powershell.exe"),
			filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		}
		for _, p := range candidates {
			if _, err := os.Stat(p); err == nil {
				powershellPath = p
				return
			}
		}
		powershellPath = "powershell"
	})
	return powershellPath
}
