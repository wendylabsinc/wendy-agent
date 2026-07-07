package enrolltoken

import (
	"encoding/base64"
	"testing"
)

// makeToken builds a fake JWT-shaped token: "<header>.<payloadJSON>.<sig>".
func makeToken(t *testing.T, payloadJSON string) string {
	t.Helper()
	seg := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return "header." + seg + ".sig"
}

func TestParseAsset_Valid(t *testing.T) {
	tok := makeToken(t, `{"type":"asset_enrollment","org_id":7,"asset_id":42}`)
	orgID, assetID, err := ParseAsset(tok)
	if err != nil {
		t.Fatalf("ParseAsset: %v", err)
	}
	if orgID != 7 || assetID != 42 {
		t.Fatalf("got org=%d asset=%d, want 7/42", orgID, assetID)
	}
}

func TestParseAsset_RejectsUserToken(t *testing.T) {
	tok := makeToken(t, `{"type":"user_enrollment","org_id":1,"user_id":"u-1"}`)
	if _, _, err := ParseAsset(tok); err == nil {
		t.Fatal("expected error for user token, got nil")
	}
}

func TestParseAsset_Malformed(t *testing.T) {
	if _, _, err := ParseAsset("not-a-token"); err == nil {
		t.Fatal("expected error for malformed token, got nil")
	}
}

func TestParseAsset_MissingIDs(t *testing.T) {
	tok := makeToken(t, `{"type":"asset_enrollment","org_id":0,"asset_id":0}`)
	if _, _, err := ParseAsset(tok); err == nil {
		t.Fatal("expected error for missing org/asset, got nil")
	}
}
