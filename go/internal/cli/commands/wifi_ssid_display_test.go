package commands

import "testing"

func TestQuoteSSIDForPromptSanitizesControls(t *testing.T) {
	got := quoteSSIDForPrompt("Home\nOffice\x1b[31m")
	want := `"Home?Office?[31m"`
	if got != want {
		t.Fatalf("quoteSSIDForPrompt = %q; want %q", got, want)
	}
}

func TestQuoteSSIDForPromptSanitizesBidiControls(t *testing.T) {
	got := quoteSSIDForPrompt("abc\u202edef")
	want := `"abc?def"`
	if got != want {
		t.Fatalf("quoteSSIDForPrompt = %q; want %q", got, want)
	}
}
