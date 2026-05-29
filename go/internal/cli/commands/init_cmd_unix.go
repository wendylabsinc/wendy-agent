//go:build darwin || linux

package commands

import (
	"os"
	"os/exec"
	"syscall"
)

// launchAssistantWithPrompt invokes the AI assistant with the prompt passed
// as a single argv element. On Unix shells this round-trips intact.
func launchAssistantWithPrompt(choice, prompt string) error {
	cmd := exec.Command(choice, prompt)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func windowsRootDir() string {
	panic("windowsRootDir called on non-Windows")
}

func isRootOwned(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
