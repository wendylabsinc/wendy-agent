//go:build linux

package diskspace

import (
	"os"
	"testing"
)

func TestFreePercent_RealPath(t *testing.T) {
	pct, ok := FreePercent(os.TempDir())
	if !ok {
		t.Fatal("FreePercent on TempDir returned ok=false")
	}
	if pct < 0 || pct > 100 {
		t.Fatalf("FreePercent = %v, want within [0,100]", pct)
	}
}

func TestFreePercent_BogusPath(t *testing.T) {
	if _, ok := FreePercent("/nonexistent/path/that/should/not/resolve"); ok {
		t.Fatal("FreePercent on a bogus path returned ok=true")
	}
}
