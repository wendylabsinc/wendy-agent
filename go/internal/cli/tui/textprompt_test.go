package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func updateTextPrompt(m TextPromptModel, msg tea.Msg) TextPromptModel {
	result, _ := m.Update(msg)
	return result.(TextPromptModel)
}

func tkey(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestTextPromptModel_SubmitTrimmed(t *testing.T) {
	m := NewTextPrompt("Name", "", "", nil)
	for _, r := range " hello " {
		m = updateTextPrompt(m, tkey(string(r)))
	}
	m = updateTextPrompt(m, tkey("enter"))
	if m.Value() != "hello" {
		t.Fatalf("expected trimmed value %q, got %q", "hello", m.Value())
	}
}

func TestTextPromptModel_ValidationError(t *testing.T) {
	m := NewTextPrompt("Port", "", "", func(v string) error {
		if v == "" {
			return fmt.Errorf("port is required")
		}
		return nil
	})

	m = updateTextPrompt(m, tkey("enter"))
	if m.err == "" {
		t.Fatal("expected validation error on empty submit")
	}
	if m.done {
		t.Fatal("model should not be done after validation error")
	}
}

func TestTextPromptModel_ValidationErrorClears(t *testing.T) {
	m := NewTextPrompt("Port", "", "", func(v string) error {
		if v == "" {
			return fmt.Errorf("required")
		}
		return nil
	})

	m = updateTextPrompt(m, tkey("enter"))
	if m.err == "" {
		t.Fatal("expected error")
	}

	// Type a character — error should clear.
	m = updateTextPrompt(m, tkey("x"))
	if m.err != "" {
		t.Fatalf("expected error to clear after typing, got %q", m.err)
	}
}

func TestTextPromptModel_DefaultValue(t *testing.T) {
	m := NewTextPrompt("Path", "", "/data", nil)
	if m.input.Value() != "/data" {
		t.Fatalf("expected default value %q, got %q", "/data", m.input.Value())
	}

	m = updateTextPrompt(m, tkey("enter"))
	if m.Value() != "/data" {
		t.Fatalf("expected %q, got %q", "/data", m.Value())
	}
}

func TestNewPasswordPrompt_MasksInput(t *testing.T) {
	m := NewPasswordPrompt("Password", "", nil)
	if m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("expected EchoPassword, got %v", m.input.EchoMode)
	}
	if m.input.EchoCharacter != '•' {
		t.Fatalf("expected mask char '•', got %q", m.input.EchoCharacter)
	}

	// Masking is display-only: the real value must still be captured.
	for _, r := range "s3cret" {
		m = updateTextPrompt(m, tkey(string(r)))
	}
	m = updateTextPrompt(m, tkey("enter"))
	if m.Value() != "s3cret" {
		t.Fatalf("expected captured value %q, got %q", "s3cret", m.Value())
	}
}

func TestNewTextPrompt_DoesNotMask(t *testing.T) {
	// Guard the non-secret path from regressing into masked echo.
	m := NewTextPrompt("Name", "", "", nil)
	if m.input.EchoMode != textinput.EchoNormal {
		t.Fatalf("expected EchoNormal for text prompt, got %v", m.input.EchoMode)
	}
}

func TestTextPromptModel_Cancellation(t *testing.T) {
	m := NewTextPrompt("Name", "", "", nil)
	m = updateTextPrompt(m, tkey("ctrl+c"))
	if !m.Cancelled() {
		t.Fatal("expected Cancelled() after ctrl+c")
	}
}
