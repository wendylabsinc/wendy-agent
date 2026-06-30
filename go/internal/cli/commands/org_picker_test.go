//go:build darwin || linux || windows

package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func makeOrg(id int32, name string) *cloudpb.Organization {
	return &cloudpb.Organization{Id: id, Name: name}
}

func stubListOrgs(t *testing.T, orgs []*cloudpb.Organization, err error) {
	t.Helper()
	orig := listOrgsFromCloud
	listOrgsFromCloud = func(context.Context, *config.AuthConfig) ([]*cloudpb.Organization, error) {
		return orgs, err
	}
	t.Cleanup(func() { listOrgsFromCloud = orig })
}

func stubOrgPicker(t *testing.T, retID int32, retName string, retErr error) {
	t.Helper()
	orig := pickOrgInteractiveFn
	pickOrgInteractiveFn = func([]*cloudpb.Organization, int32) (int32, string, error) {
		return retID, retName, retErr
	}
	t.Cleanup(func() { pickOrgInteractiveFn = orig })
}

func noPickerAllowed(t *testing.T) {
	t.Helper()
	orig := pickOrgInteractiveFn
	pickOrgInteractiveFn = func([]*cloudpb.Organization, int32) (int32, string, error) {
		t.Fatal("picker should not be called")
		return 0, "", nil
	}
	t.Cleanup(func() { pickOrgInteractiveFn = orig })
}

func testAuth() *config.AuthConfig {
	return &config.AuthConfig{
		CloudGRPC:    "prod.example.com:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 2}},
	}
}

func TestOrgPickerItems(t *testing.T) {
	orgs := []*cloudpb.Organization{makeOrg(1, "Acme"), makeOrg(7, "Customer Co")}
	items := orgPickerItems(orgs)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Name != "Acme" {
		t.Errorf("item 0 name = %q", items[0].Name)
	}
	if items[0].DedupKey != "1" || items[0].Value.(string) != "1" {
		t.Errorf("item 0 key/value wrong: %+v", items[0])
	}
	if items[1].Name != "Customer Co" || items[1].DedupKey != "7" {
		t.Errorf("item 1 unexpected: %+v", items[1])
	}
}

// TestResolveOrgSingleOrg: one org -> use it, no picker.
func TestResolveOrgSingleOrg(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(5, "Only Org")}, nil)
	noPickerAllowed(t)

	res, err := resolveOrgWithConfig(context.Background(), &config.Config{}, testAuth(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ID != 5 || res.Name != "Only Org" {
		t.Errorf("got %+v; want {5, Only Org}", res)
	}
}

// TestResolveOrgDefault: default set -> use it, no picker.
func TestResolveOrgDefault(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(3, "Org A"), makeOrg(9, "Org B")}, nil)
	noPickerAllowed(t)

	res, err := resolveOrgWithConfig(context.Background(), &config.Config{DefaultOrgID: 9}, testAuth(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ID != 9 || res.Name != "Org B" {
		t.Errorf("got %+v; want {9, Org B}", res)
	}
}

// TestResolveOrgStaleDefault: default set but no longer in membership -> picker shown.
func TestResolveOrgStaleDefault(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(3, "Org A"), makeOrg(9, "Org B")}, nil)
	stubOrgPicker(t, 3, "Org A", nil)

	res, err := resolveOrgWithConfig(context.Background(), &config.Config{DefaultOrgID: 99}, testAuth(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ID != 3 {
		t.Errorf("got %+v; want ID=3", res)
	}
}

// TestResolveOrgForceShow: default set + forceShow -> picker shown regardless.
func TestResolveOrgForceShow(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(3, "Org A"), makeOrg(9, "Org B")}, nil)
	called := false
	orig := pickOrgInteractiveFn
	pickOrgInteractiveFn = func(_ []*cloudpb.Organization, _ int32) (int32, string, error) {
		called = true
		return 3, "Org A", nil
	}
	t.Cleanup(func() { pickOrgInteractiveFn = orig })

	res, err := resolveOrgWithConfig(context.Background(), &config.Config{DefaultOrgID: 9}, testAuth(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("picker was not called with forceShow=true")
	}
	if res.ID != 3 {
		t.Errorf("got %+v; want ID=3", res)
	}
}

// TestResolveOrgMultipleNoDefault: many orgs, no default -> picker shown.
func TestResolveOrgMultipleNoDefault(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(3, "Org A"), makeOrg(9, "Org B")}, nil)
	stubOrgPicker(t, 9, "Org B", nil)

	res, err := resolveOrgWithConfig(context.Background(), &config.Config{}, testAuth(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ID != 9 || res.Name != "Org B" {
		t.Errorf("got %+v; want {9, Org B}", res)
	}
}

// TestResolveOrgListFailureFallback: cloud call fails -> falls back to cert org.
func TestResolveOrgListFailureFallback(t *testing.T) {
	stubListOrgs(t, nil, errors.New("network error"))
	noPickerAllowed(t)

	auth := testAuth() // cert has OrganizationID: 2
	res, err := resolveOrgWithConfig(context.Background(), &config.Config{}, auth, false)
	if err != nil {
		t.Fatalf("unexpected error on fallback: %v", err)
	}
	if res.ID != 2 {
		t.Errorf("expected fallback to cert org 2, got %+v", res)
	}
}

// TestResolveOrgListFailureForceShowErrors: cloud call fails + forceShow -> error.
func TestResolveOrgListFailureForceShowErrors(t *testing.T) {
	stubListOrgs(t, nil, errors.New("network error"))

	_, err := resolveOrgWithConfig(context.Background(), &config.Config{}, testAuth(), true)
	if err == nil {
		t.Fatal("expected error when forceShow=true and list fails")
	}
}

// TestResolveOrgPickerCancelled: user presses Ctrl+C in picker.
func TestResolveOrgPickerCancelled(t *testing.T) {
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(3, "Org A"), makeOrg(9, "Org B")}, nil)
	stubOrgPicker(t, 0, "", ErrUserCancelled)

	_, err := resolveOrgWithConfig(context.Background(), &config.Config{}, testAuth(), false)
	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("expected ErrUserCancelled, got %v", err)
	}
}
