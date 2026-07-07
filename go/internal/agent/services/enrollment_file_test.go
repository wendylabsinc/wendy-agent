package services

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// assetToken builds a fake asset-enrollment token with the given org/asset.
func assetToken(t *testing.T, org, asset int32) string {
	t.Helper()
	payload := []byte(`{"type":"asset_enrollment","org_id":` +
		strconv.Itoa(int(org)) + `,"asset_id":` + strconv.Itoa(int(asset)) + `}`)
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

func TestApplyEnrollmentFile_EnrollsAndDeletes(t *testing.T) {
	svc, tmpDir := newTestProvisioningService(t) // fake cloud dialer returns a canned cert
	path := filepath.Join(tmpDir, "enrollment.json")
	tok := assetToken(t, 7, 42)
	if err := os.WriteFile(path, []byte(`{"token":"`+tok+`","cloudHost":"cloud.example:443"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	svc.ApplyEnrollmentFile(context.Background())

	if _, _, _, enrolled := svc.ProvisioningInfo(); !enrolled {
		t.Fatal("expected agent to be enrolled after ApplyEnrollmentFile")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected enrollment.json to be deleted, stat err = %v", err)
	}
}

func TestApplyEnrollmentFile_MalformedDeletesNoCrash(t *testing.T) {
	svc, tmpDir := newTestProvisioningService(t)
	path := filepath.Join(tmpDir, "enrollment.json")
	if err := os.WriteFile(path, []byte(`{"token":"garbage","cloudHost":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	svc.ApplyEnrollmentFile(context.Background())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected malformed enrollment.json to be deleted, stat err = %v", err)
	}
	if _, _, _, enrolled := svc.ProvisioningInfo(); enrolled {
		t.Fatal("agent must not be enrolled from a malformed token")
	}
}

func TestApplyEnrollmentFile_AbsentIsNoop(t *testing.T) {
	svc, _ := newTestProvisioningService(t)
	svc.ApplyEnrollmentFile(context.Background()) // must not panic
	if _, _, _, enrolled := svc.ProvisioningInfo(); enrolled {
		t.Fatal("no enrollment file: agent must stay unenrolled")
	}
}
