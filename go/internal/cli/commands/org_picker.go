//go:build darwin || linux || windows

package commands

import (
	"context"
	"fmt"
	"io"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// wendyInternalOrgID is Wendy Labs' own organization. Enrolling a customer
// device into it is almost always a mistake, so the picker surfaces an extra
// confirmation step when it is selected.
const wendyInternalOrgID int32 = 2

// listOrgsFromCloud fetches every organization the authenticated user belongs
// to, draining the server-streaming ListOrganizations RPC. Declared as a var
// so unit tests can stub it without a live cloud connection.
var listOrgsFromCloud = listOrgsFromCloudImpl

func listOrgsFromCloudImpl(ctx context.Context, auth *config.AuthConfig) ([]*cloudpb.Organization, error) {
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := cloudpb.NewOrganizationServiceClient(conn)
	stream, err := client.ListOrganizations(cloudContext(ctx, auth), &cloudpb.ListOrganizationsRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}

	var orgs []*cloudpb.Organization
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("receiving organizations: %w", err)
		}
		if org := resp.GetOrganization(); org != nil {
			orgs = append(orgs, org)
		}
	}
	return orgs, nil
}

// orgPickerItems converts a slice of organizations into picker rows. The Name
// column shows the org name, Description shows the numeric ID, DedupKey and
// Value both carry the ID as a string so selection and default-marking are
// stable across name changes.
func orgPickerItems(orgs []*cloudpb.Organization) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(orgs))
	for _, org := range orgs {
		id := strconv.Itoa(int(org.GetId()))
		items = append(items, tui.PickerItem{
			Name:        org.GetName(),
			Description: fmt.Sprintf("ID: %s", id),
			DedupKey:    id,
			Value:       id,
		})
	}
	return items
}

// pickOrgInteractive shows the interactive org picker. 'd' marks the
// highlighted org as the persisted default (written immediately), 'x' clears
// it, and Enter selects an org for this invocation only. Returns the selected
// org ID and name.
func pickOrgInteractive(orgs []*cloudpb.Organization, defaultOrgID int32) (int32, string, error) {
	picker := tui.NewPickerWithTitle("Select an organization")
	if defaultOrgID != 0 {
		picker.DefaultKey = strconv.Itoa(int(defaultOrgID))
	}
	picker.OnSetDefault = func(item tui.PickerItem) {
		id, _ := item.Value.(string)
		if id == "" {
			return
		}
		n, err := strconv.Atoi(id)
		if err != nil {
			return
		}
		if c, err := config.Load(); err == nil {
			c.DefaultOrgID = int32(n)
			_ = config.Save(c)
		}
	}
	picker.OnUnsetDefault = func() {
		if c, err := config.Load(); err == nil {
			c.DefaultOrgID = 0
			_ = config.Save(c)
		}
	}
	picker.OnRemoveItem = func(item tui.PickerItem) {
		idStr, _ := item.Value.(string)
		n, err := strconv.Atoi(idStr)
		if err != nil {
			return
		}
		orgID := int32(n)
		c, err := config.Load()
		if err != nil {
			return
		}
		filtered := c.Auth[:0]
		for _, a := range c.Auth {
			if len(a.Certificates) == 0 || int32(a.Certificates[0].OrganizationID) != orgID {
				filtered = append(filtered, a)
			}
		}
		c.Auth = filtered
		_ = config.Save(c)
	}

	p := tea.NewProgram(picker)
	go func() {
		p.Send(tui.PickerAddMsg{Items: orgPickerItems(orgs)})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return 0, "", fmt.Errorf("picker: %w", err)
	}
	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return 0, "", ErrUserCancelled
	}
	if pm.Selected() == nil {
		return 0, "", fmt.Errorf("no organization selected")
	}

	idStr := pm.Selected().Value.(string)
	n, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid org id %q", idStr)
	}
	return int32(n), pm.Selected().Name, nil
}

// OrgResolution holds the resolved organization and a flag indicating whether
// the org was resolved from config rather than a live cloud fetch.
type OrgResolution struct {
	ID   int32
	Name string
}

// resolveOrg determines which organization to use for an operation that must
// target a specific org. It implements the four selection scenarios:
//
//  1. No default + one org   -> use the sole org (no picker).
//  2. No default + many orgs -> show the picker; the user may set a new default.
//  3. Default set            -> use the default (no picker).
//  4. alwaysPickOrg == true      -> always show the picker regardless of a default;
//     the user may change or clear the default.
//
// If fetching the org list fails, resolveOrg falls back to the org embedded in
// the auth session's certificate and logs a warning.
func resolveOrg(ctx context.Context, auth *config.AuthConfig, alwaysPickOrg bool) (OrgResolution, error) {
	return resolveOrgFn(ctx, auth, alwaysPickOrg)
}

// resolveOrgFn is the indirection point so tests can stub resolveOrg without a
// live cloud connection.
var resolveOrgFn = resolveOrgImpl

func resolveOrgImpl(ctx context.Context, auth *config.AuthConfig, alwaysPickOrg bool) (OrgResolution, error) {
	cfg, err := config.Load()
	if err != nil {
		return OrgResolution{}, fmt.Errorf("loading config: %w", err)
	}
	return resolveOrgWithConfig(ctx, cfg, auth, alwaysPickOrg)
}

// resolveOrgWithConfig is the inner implementation that accepts an already-
// loaded config, making it directly testable without touching the config file.
func resolveOrgWithConfig(ctx context.Context, cfg *config.Config, auth *config.AuthConfig, alwaysPickOrg bool) (OrgResolution, error) {
	orgs, listErr := listOrgsFromCloud(ctx, auth)

	// Graceful fallback: if the cloud call fails, use the cert's org so the
	// command still works (with a warning), unless the picker was forced.
	if listErr != nil {
		if alwaysPickOrg {
			return OrgResolution{}, fmt.Errorf("fetching organizations: %w", listErr)
		}
		fmt.Printf("Warning: could not fetch organizations (%v). Falling back to certificate org.\n", listErr)
		certOrgID := int32(0)
		if len(auth.Certificates) > 0 {
			certOrgID = int32(auth.Certificates[0].OrganizationID)
		}
		return OrgResolution{ID: certOrgID, Name: fmt.Sprintf("org %d", certOrgID)}, nil
	}

	if len(orgs) == 0 {
		return OrgResolution{}, fmt.Errorf("your account belongs to no organizations; contact your administrator")
	}

	// Scenario 1: single org — use it without prompting.
	if len(orgs) == 1 && !alwaysPickOrg {
		return OrgResolution{ID: orgs[0].GetId(), Name: orgs[0].GetName()}, nil
	}

	// Scenario 3: valid default is set and picker not forced.
	if cfg.DefaultOrgID != 0 && !alwaysPickOrg {
		for _, org := range orgs {
			if org.GetId() == cfg.DefaultOrgID {
				return OrgResolution{ID: org.GetId(), Name: org.GetName()}, nil
			}
		}
		// Default no longer valid (org removed from membership); fall through to picker.
	}

	// Scenarios 2 and 4: show the interactive picker.
	id, name, err := pickOrgInteractiveFn(orgs, cfg.DefaultOrgID)
	if err != nil {
		return OrgResolution{}, err
	}
	return OrgResolution{ID: id, Name: name}, nil
}

// pickOrgInteractiveFn is stubbed in tests.
var pickOrgInteractiveFn = pickOrgInteractive
