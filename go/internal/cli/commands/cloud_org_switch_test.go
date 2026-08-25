package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func stubCloudOrgSwitchPicker(t *testing.T, wantCount int, selectedID int32, selectedName string) {
	t.Helper()
	orig := pickOrgInteractiveFn
	pickOrgInteractiveFn = func(orgs []*cloudpb.Organization, _ *config.Config, copyOnEnter bool) (int32, string, error) {
		if copyOnEnter {
			t.Error("cloud tab org switch must select, not copy")
		}
		if len(orgs) != wantCount {
			t.Errorf("picker org count = %d, want %d", len(orgs), wantCount)
		}
		return selectedID, selectedName, nil
	}
	t.Cleanup(func() { pickOrgInteractiveFn = orig })
}

func stubCloudOrgReload(t *testing.T, cfg *config.Config, err error) {
	t.Helper()
	orig := loadCloudOrgConfig
	loadCloudOrgConfig = func() (*config.Config, error) { return cfg, err }
	t.Cleanup(func() { loadCloudOrgConfig = orig })
}

func stubCloudOrgLogin(t *testing.T, fn func(context.Context, string, string) error) {
	t.Helper()
	orig := performLoginFn
	performLoginFn = fn
	t.Cleanup(func() { performLoginFn = orig })
}

func TestAccessibleCloudOrganizationsIncludesUnconnectedMemberships(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{*pickerAuth(7)}}
	stubListOrgs(t, []*cloudpb.Organization{
		makeOrg(7, "Connected"),
		makeOrg(9, "Permitted only"),
	}, nil)

	orgs, err := accessibleCloudOrganizations(context.Background(), cfg)
	if err != nil {
		t.Fatalf("accessibleCloudOrganizations: %v", err)
	}
	if len(orgs) != 2 {
		t.Fatalf("org count = %d, want 2", len(orgs))
	}
	if orgs[1].org.GetId() != 9 || orgs[1].source != &cfg.Auth[0] {
		t.Fatalf("unconnected org lost membership/source: %+v", orgs[1])
	}
	if cloudAuthForOrg(cfg, 9) != nil {
		t.Fatal("permitted-only org unexpectedly has local credentials")
	}
}

func TestSwitchCloudOrganizationConnectedOrgSkipsLogin(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{*pickerAuth(7), *pickerAuth(9)}}
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(7, "Seven"), makeOrg(9, "Nine")}, nil)
	stubCloudOrgSwitchPicker(t, 2, 9, "Nine")
	stubCloudOrgReload(t, cfg, nil)
	stubCloudOrgLogin(t, func(context.Context, string, string) error {
		t.Fatal("connected org should not start login")
		return nil
	})

	auth, _, err := switchCloudOrganization(context.Background(), cfg)
	if err != nil {
		t.Fatalf("switchCloudOrganization: %v", err)
	}
	if got := cloudAuthOrgID(auth); got != 9 {
		t.Fatalf("selected org = %d, want 9", got)
	}
}

func TestSwitchCloudOrganizationUnconnectedOrgLogsIn(t *testing.T) {
	source := pickerAuth(7)
	source.CloudDashboard = "https://tenant.example"
	source.CloudGRPC = "grpc.tenant.example:443"
	cfg := &config.Config{Auth: []config.AuthConfig{*source}}
	fresh := &config.Config{Auth: []config.AuthConfig{*source, {
		CloudDashboard: source.CloudDashboard,
		CloudGRPC:      source.CloudGRPC,
		Certificates:   []config.CertificateInfo{{OrganizationID: 9}},
	}}}
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(7, "Seven"), makeOrg(9, "Nine")}, nil)
	stubCloudOrgSwitchPicker(t, 2, 9, "Nine")
	stubCloudOrgReload(t, fresh, nil)
	loginCalled := false
	stubCloudOrgLogin(t, func(_ context.Context, dashboard, grpcEndpoint string) error {
		loginCalled = true
		if dashboard != source.CloudDashboard || grpcEndpoint != source.CloudGRPC {
			t.Errorf("login targets = (%q, %q), want (%q, %q)", dashboard, grpcEndpoint, source.CloudDashboard, source.CloudGRPC)
		}
		return nil
	})

	auth, gotCfg, err := switchCloudOrganization(context.Background(), cfg)
	if err != nil {
		t.Fatalf("switchCloudOrganization: %v", err)
	}
	if !loginCalled {
		t.Fatal("unconnected org did not start login")
	}
	if gotCfg != fresh || cloudAuthOrgID(auth) != 9 {
		t.Fatalf("post-login selection = cfg:%p org:%d, want cfg:%p org:9", gotCfg, cloudAuthOrgID(auth), fresh)
	}
}

func TestSwitchCloudOrganizationRequiresSelectedOrgCredentialsAfterLogin(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{*pickerAuth(7)}}
	stubListOrgs(t, []*cloudpb.Organization{makeOrg(7, "Seven"), makeOrg(9, "Nine")}, nil)
	stubCloudOrgSwitchPicker(t, 2, 9, "Nine")
	stubCloudOrgReload(t, cfg, nil)
	stubCloudOrgLogin(t, func(context.Context, string, string) error { return nil })

	_, _, err := switchCloudOrganization(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected missing selected-org credentials error")
	}
	if !strings.Contains(err.Error(), "credentials for the selected organization were not stored") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAccessibleCloudOrganizationsReportsListingFailure(t *testing.T) {
	cfg := &config.Config{Auth: []config.AuthConfig{*pickerAuth(7)}}
	stubListOrgs(t, nil, errors.New("offline"))
	_, err := accessibleCloudOrganizations(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error = %v, want listing failure", err)
	}
}
