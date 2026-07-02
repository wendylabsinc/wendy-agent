package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestOrgInCerts(t *testing.T) {
	certs := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	if !orgInCerts(3, certs) {
		t.Error("orgInCerts(3) = false, want true")
	}
	if orgInCerts(9, certs) {
		t.Error("orgInCerts(9) = true, want false")
	}
	if orgInCerts(3, nil) {
		t.Error("orgInCerts(3, nil) = true, want false")
	}
}

func TestOrgMismatchDeviceError_Message(t *testing.T) {
	userCerts := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	err := newOrgMismatchDeviceError(42, userCerts)
	msg := err.Error()

	for _, want := range []string{"org 42", "org 3", "org 8", "wendy cloud login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	// Must NOT recommend switching the default org — that does not help on this path.
	if strings.Contains(msg, "list-orgs") || strings.Contains(strings.ToLower(msg), "default org") {
		t.Errorf("message must not suggest switching default org: %q", msg)
	}
}

func TestOrgMismatchDeviceError_SingleUserOrg(t *testing.T) {
	err := newOrgMismatchDeviceError(42, []config.CertificateInfo{{OrganizationID: 3}})
	msg := err.Error()
	if !strings.Contains(msg, "org 42") || !strings.Contains(msg, "org 3") {
		t.Errorf("message %q missing device org 42 or user org 3", msg)
	}
}
