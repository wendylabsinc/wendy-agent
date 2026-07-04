package commands

import (
	"strings"
	"testing"
)

func TestScanBmapProgress(t *testing.T) {
	var last int64
	scanBmapProgress(strings.NewReader("100\n2048\n65536\n"), func(n int64) { last = n })
	if last != 65536 {
		t.Fatalf("last progress = %d, want 65536", last)
	}
}
