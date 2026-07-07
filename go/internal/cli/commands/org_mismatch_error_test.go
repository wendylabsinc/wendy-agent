package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// stubOrgNameResolver replaces the live ListOrganizations lookup with a fixed
// map (or nil) so tests never touch the network, restoring the original when
// the test finishes.
func stubOrgNameResolver(t *testing.T, names map[int32]string) {
	t.Helper()
	orig := resolveUserOrgNamesFn
	t.Cleanup(func() { resolveUserOrgNamesFn = orig })
	resolveUserOrgNamesFn = func(context.Context, []config.CertificateInfo) map[int32]string {
		return names
	}
}

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
	err := newOrgMismatchDeviceError(42, userCerts, nil)
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
	err := newOrgMismatchDeviceError(42, []config.CertificateInfo{{OrganizationID: 3}}, nil)
	msg := err.Error()
	if !strings.Contains(msg, "org 42") || !strings.Contains(msg, "org 3") {
		t.Errorf("message %q missing device org 42 or user org 3", msg)
	}
}

// TestOrgMismatchDeviceError_ShowsOrgNames verifies that resolved org names are
// rendered as "Name (org N)" for the user's own orgs, while an org with no
// resolved name falls back to the bare ID and the device's org stays numeric.
func TestOrgMismatchDeviceError_ShowsOrgNames(t *testing.T) {
	userCerts := []config.CertificateInfo{{OrganizationID: 57}, {OrganizationID: 2}}
	names := map[int32]string{57: "Acme Robotics", 2: "Contoso"}
	err := newOrgMismatchDeviceError(9, userCerts, names)
	msg := err.Error()

	for _, want := range []string{"Acme Robotics (org 57)", "Contoso (org 2)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}

	// A partial map: only one name resolved. The other renders as a bare ID.
	partial := newOrgMismatchDeviceError(9, userCerts, map[int32]string{57: "Acme Robotics"})
	pmsg := partial.Error()
	if !strings.Contains(pmsg, "Acme Robotics (org 57)") {
		t.Errorf("message %q missing resolved name", pmsg)
	}
	if !strings.Contains(pmsg, "org 2") || strings.Contains(pmsg, "(org 2)") {
		t.Errorf("unresolved org should render as bare %q, got %q", "org 2", pmsg)
	}
}

func TestChooseRejectionError_CrossOrgMismatch(t *testing.T) {
	stubOrgNameResolver(t, map[int32]string{3: "Acme", 8: "Contoso"})
	certs := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	err := chooseRejectionError(context.Background(), 42, certs, errors.New("boom"))
	var mismatch orgMismatchDeviceError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected orgMismatchDeviceError, got %T: %v", err, err)
	}
	if mismatch.deviceOrg != 42 {
		t.Errorf("deviceOrg = %d, want 42", mismatch.deviceOrg)
	}
	// The resolved names must flow through to the rendered message.
	if msg := err.Error(); !strings.Contains(msg, "Acme (org 3)") {
		t.Errorf("message %q missing resolved org name", msg)
	}
}

func TestChooseRejectionError_SameOrgFallsThrough(t *testing.T) {
	stubOrgNameResolver(t, nil)
	certs := []config.CertificateInfo{{OrganizationID: 3}, {OrganizationID: 8}}
	err := chooseRejectionError(context.Background(), 3, certs, errors.New("boom"))
	if !errors.Is(err, errTLSHandshakeRejected) {
		t.Errorf("same-org failure: expected errTLSHandshakeRejected, got %T: %v", err, err)
	}
	var mismatch orgMismatchDeviceError
	if errors.As(err, &mismatch) {
		t.Error("same-org failure must not produce orgMismatchDeviceError")
	}
}

func TestChooseRejectionError_NoObservedOrgFallsThrough(t *testing.T) {
	stubOrgNameResolver(t, nil)
	certs := []config.CertificateInfo{{OrganizationID: 3}}
	err := chooseRejectionError(context.Background(), 0, certs, errors.New("boom"))
	if !errors.Is(err, errTLSHandshakeRejected) {
		t.Errorf("no observed org: expected errTLSHandshakeRejected, got %T: %v", err, err)
	}
}
