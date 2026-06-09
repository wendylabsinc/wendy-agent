//go:build windows

package commands

import (
	"fmt"
	"os"
	"os/exec"
)

func launchAssistantWithPrompt(choice, prompt string) error {
	tmpPath, cleanup, err := writePromptForAssistant(prompt)
	if err != nil {
		return err
	}
	defer cleanup()

	short := fmt.Sprintf("Read the Markdown file at this absolute path: %s for project context, then help me get started building this project.", tmpPath)

	cmd := exec.Command(choice, short)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writePromptForAssistant(prompt string) (string, func(), error) {
	f, err := os.CreateTemp("", "wendy-init-prompt-*.md")
	if err != nil {
		return "", nil, fmt.Errorf("creating prompt temp file: %w", err)
	}
	path := f.Name()

	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("writing prompt temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("closing prompt temp file: %w", err)
	}

	return path, func() { os.Remove(path) }, nil
}
