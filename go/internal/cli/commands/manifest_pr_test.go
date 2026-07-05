package commands

import "testing"

func TestPRBasePath(t *testing.T) {
	if got := prBasePath(123); got != "pr/123/" {
		t.Fatalf("prBasePath(123) = %q", got)
	}
}
