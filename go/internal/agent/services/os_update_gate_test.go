package services

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

func TestRecordPendingOSUpdateRedactsURLCredentials(t *testing.T) {
	dir := t.TempDir()

	recordPendingOSUpdate(zap.NewNop(), dir, "https://user:hunter2@artifacts.example.com/os.wendy", updaterNameWendyOS)

	marker, found, err := oshealth.ReadPendingMarker(dir)
	if err != nil || !found {
		t.Fatalf("marker should be written (found=%v err=%v)", found, err)
	}
	if strings.Contains(marker.ArtifactURL, "hunter2") {
		t.Errorf("persisted ArtifactURL %q must not contain credentials", marker.ArtifactURL)
	}
	if !strings.Contains(marker.ArtifactURL, "artifacts.example.com/os.wendy") {
		t.Errorf("persisted ArtifactURL %q should keep host and path for debugging", marker.ArtifactURL)
	}
}

func TestRedactURLCredentials(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		secrets []string // must NOT survive in the output
		keep    []string // must survive, so the URL stays useful for debugging
	}{
		{
			name:    "presigned query signature",
			raw:     "https://bucket.s3.amazonaws.com/os.wendy?X-Amz-Signature=deadbeefsecret&X-Amz-Expires=900",
			secrets: []string{"deadbeefsecret"},
			keep:    []string{"bucket.s3.amazonaws.com/os.wendy", "X-Amz-Signature"},
		},
		{
			name:    "opaque token query param",
			raw:     "https://example.com/a.wendy?token=abc123secret",
			secrets: []string{"abc123secret"},
			keep:    []string{"example.com/a.wendy", "token"},
		},
		{
			name:    "userinfo password",
			raw:     "https://user:hunter2@artifacts.example.com/os.wendy",
			secrets: []string{"hunter2"},
			keep:    []string{"artifacts.example.com/os.wendy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURLCredentials(tc.raw)
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Errorf("redacted URL %q must not contain secret %q", got, s)
				}
			}
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("redacted URL %q should keep %q for debugging", got, k)
				}
			}
		})
	}
}

func TestRedactURLCredentialsFailsClosed(t *testing.T) {
	// A control character makes url.Parse fail. The raw string still embeds a
	// password and a query token, so it must be dropped, not echoed.
	raw := "https://user:hunter2@example.com/os.wendy?sig=topsecret\x7f"
	got := redactURLCredentials(raw)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "topsecret") {
		t.Errorf("unparseable URL was echoed with credentials: %q", got)
	}
}

func TestRecordPendingOSUpdateClearsPreviousResult(t *testing.T) {
	dir := t.TempDir()
	prev := oshealth.UpdateResult{
		Outcome:   oshealth.OutcomeCommitted,
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}
	if err := oshealth.WriteUpdateResult(dir, prev); err != nil {
		t.Fatal(err)
	}

	recordPendingOSUpdate(zap.NewNop(), dir, "http://example/artifact.wendy", updaterNameWendyOS)

	if _, found, err := oshealth.ReadUpdateResult(dir); err != nil || found {
		t.Errorf("previous update result must be cleared so it cannot be mistaken for this attempt's outcome (found=%v err=%v)", found, err)
	}
	marker, found, err := oshealth.ReadPendingMarker(dir)
	if err != nil || !found {
		t.Fatalf("marker should be written (found=%v err=%v)", found, err)
	}
	if marker.ArtifactURL != "http://example/artifact.wendy" {
		t.Errorf("ArtifactURL = %q", marker.ArtifactURL)
	}
	if marker.Backend != updaterNameWendyOS {
		t.Errorf("marker.Backend = %q, want %q so the next boot commits with the same backend", marker.Backend, updaterNameWendyOS)
	}
}
