package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
)

func TestCliLoglnRecordsToDiag(t *testing.T) {
	diag.ResetForTesting()
	cliLogln("hello %s", "world")
	found := false
	for _, l := range diag.Recent() {
		if strings.Contains(l, "hello world") {
			found = true
		}
	}
	if !found {
		t.Error("cliLogln output should be recorded in diag ring")
	}
}
