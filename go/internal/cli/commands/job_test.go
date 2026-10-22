package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestJobInspectInvalidWaitFailsWithoutDeviceDiscovery(t *testing.T) {
	c := newJobCmd()
	c.SetArgs([]string{"inspect", "/tmp/job", "--wait", "1"})
	c.SetContext(context.Background())
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "requires a since cursor") {
		t.Fatalf("invalid wait reached connection: %v", err)
	}
}
func TestJobCommandsDocumentAgentFacingFlags(t *testing.T) {
	c := newJobCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"inspect", "--help"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--since") || !strings.Contains(out.String(), "--wait") {
		t.Fatal(out.String())
	}
	c = newJobCmd()
	out.Reset()
	c.SetOut(&out)
	c.SetArgs([]string{"artifacts", "pull", "--help"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--file") || !strings.Contains(out.String(), "--output") {
		t.Fatal(out.String())
	}
	c = newJobCmd()
	out.Reset()
	c.SetOut(&out)
	c.SetArgs([]string{"submit", "--help"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--wait", "--artifact", "--output", "--report-field", "--workflow"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatal(out.String())
		}
	}
}

func TestJobSubmitRejectsBadCollectionBeforeReadingSpecOrConnecting(t *testing.T) {
	c := newJobCmd()
	c.SetArgs([]string{"submit", "does-not-exist.json", "--artifact", "../escape", "--output", "/tmp/results", "--wait", "1"})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "safe relative paths") {
		t.Fatalf("invalid collection reached file/device access: %v", err)
	}
}

func TestCheckpointInspectValidatesRuntimeBeforeDeviceDiscovery(t *testing.T) {
	cmd := newJobCmd()
	cmd.SetArgs([]string{"checkpoint", "inspect", "/tmp/checkpoint.pt", "--sha256", strings.Repeat("a", 64), "--python", "python3"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "absolute remote paths") {
		t.Fatalf("invalid runtime reached device discovery: %v", err)
	}
}
