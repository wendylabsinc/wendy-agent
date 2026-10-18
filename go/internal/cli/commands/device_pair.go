package commands

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// transportForDevice returns the sensor pairing transport to use for a
// discovered source: "wendycom" for a Wendy Lite board (WendyCom, never
// gRPC or raw sensorlink), "grpc" for a WendyOS mTLS agent advertising the
// "sensors" capability, "tcp" for a legacy/MCU sensorlink device.
func transportForDevice(d models.DiscoveredDevice) string {
	if d.WendyLite {
		return "wendycom"
	}
	if d.IsMTLS {
		for _, c := range d.Caps {
			if c == "sensors" {
				return "grpc"
			}
		}
	}
	return "tcp"
}

// orgAllowed permits sensor pairing when the source device's org is one the
// CLI holds an authenticated session in. The user may be logged into several
// orgs at once (e.g. org 2 and org 77); pairing must succeed for a device in
// ANY of them, not just whichever session happens to be the default — the mTLS
// connection to the consumer already used the right org's cert.
func orgAllowed(cliOrgs map[int32]bool, sourceOrg int32) error {
	if cliOrgs[sourceOrg] {
		return nil
	}
	have := make([]int, 0, len(cliOrgs))
	for org := range cliOrgs {
		have = append(have, int(org))
	}
	sort.Ints(have)
	labels := make([]string, len(have))
	for i, org := range have {
		labels[i] = orgLabel("", int32(org))
	}
	yours := "no organizations"
	if len(labels) == 1 {
		yours = "organization " + labels[0]
	} else if len(labels) > 1 {
		yours = "organizations " + strings.Join(labels, ", ")
	}
	return fmt.Errorf("device is in organization %s, but you are logged in to %s; pairing is only allowed within an organization you belong to", orgLabel("", sourceOrg), yours)
}

// discoverLocalSensorSources scans this computer's network for sensor sources:
// WendyOS agents (_wendyos._udp) and Wendy Lite boards (_wendy-lite._tcp). The
// SensorLink tab uses it only when the device's agent predates
// DiscoverSensorSources. Its results may name sources the device cannot
// reach, so the tab labels them.
func discoverLocalSensorSources(ctx context.Context) ([]models.DiscoveredDevice, error) {
	lan, err := discovery.DiscoverSensorSources(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("discovering devices: %w", err)
	}
	return (&models.DevicesCollection{LANDevices: lan}).MergedDevices(), nil
}

// sensorSourcesFromDevice maps a DiscoverSensorSources reply into the shape
// the SensorLink tab consumes, exactly as if the device's sightings had been
// found locally. The tenant and principal hints are rechecked the way
// discovery.LANDeviceFromWendyLiteService checks them, so the tenant gate in
// sensorPairHandler.pair always compares the tenant the principal names.
func sensorSourcesFromDevice(sources []*agentpbv2.DiscoveredSensorSource) []models.DiscoveredDevice {
	lan := make([]models.LANDevice, 0, len(sources))
	for _, s := range sources {
		if s == nil {
			continue
		}
		d := models.LANDevice{
			ID:            s.GetId(),
			DisplayName:   s.GetDisplayName(),
			Hostname:      s.GetHostname(),
			IPAddress:     s.GetIpAddress(),
			Addresses:     s.GetAddresses(),
			Port:          int(s.GetPort()),
			IsMTLS:        s.GetIsMtls(),
			Caps:          s.GetCaps(),
			WendyLite:     s.GetWendyLite(),
			InterfaceType: string(models.InterfaceLAN),
			IsWendyDevice: true,
		}
		d.Sensorlink = slices.Contains(d.Caps, "sensors")
		if s.GetAssetId() > 0 {
			d.AssetID = s.GetAssetId()
		}
		if s.GetOrgId() > 0 {
			d.OrgID = s.GetOrgId()
		}
		if tenant, err := uuid.Parse(s.GetTenantUuid()); err == nil && tenant.String() == s.GetTenantUuid() {
			d.TenantUUID = s.GetTenantUuid()
		}
		if principal := s.GetPrincipalUri(); d.TenantUUID != "" {
			identity, err := certs.ParseDevicePrincipal(principal)
			if err == nil && identity.TenantUUID == d.TenantUUID &&
				principal == certs.DeviceSPIFFEURI(identity.TenantUUID, identity.EntityID) {
				d.PrincipalURI = principal
			}
		}
		lan = append(lan, d)
	}
	return (&models.DevicesCollection{LANDevices: lan}).MergedDevices()
}

// runPicker shows an interactive picker over items and returns the selection,
// mirroring the tea.NewProgram/Selected() pattern in pickCamera (camera_picker.go).
func runPicker(title string, items []tui.PickerItem) (*tui.PickerItem, error) {
	picker := tui.NewPickerWithTitle(title)
	p := tea.NewProgram(picker)

	go func() {
		p.Send(tui.PickerAddMsg{Items: items})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	pm, ok := finalModel.(tui.PickerModel)
	if !ok {
		return nil, fmt.Errorf("picker: unexpected model type")
	}
	if pm.Cancelled() {
		return nil, ErrUserCancelled
	}
	sel := pm.Selected()
	if sel == nil {
		return nil, ErrUserCancelled
	}
	return sel, nil
}

// cliOrgIDs returns the set of organization IDs the CLI holds an authenticated
// certificate for, across EVERY stored session. Pairing is allowed with a
// device in any of them, so unlike currentCLIOrgID (which read only the default
// session) this never rejects a device the user is legitimately logged in to.
func cliOrgIDs() (map[int32]bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	orgs := make(map[int32]bool)
	for i := range cfg.Auth {
		for _, c := range cfg.Auth[i].Certificates {
			if c.OrganizationID > 0 {
				orgs[int32(c.OrganizationID)] = true
			}
		}
	}
	if len(orgs) == 0 {
		return nil, config.ErrNotLoggedIn
	}
	return orgs, nil
}

// cleanRPCError maps a gRPC error to a human-readable message so a raw
// "rpc error: code = ..." string never reaches the user.
func cleanRPCError(err error) error {
	switch status.Code(err) {
	case codes.Unavailable:
		return fmt.Errorf("device is not reachable")
	case codes.PermissionDenied:
		return fmt.Errorf("not authorized to pair with this device")
	default:
		return fmt.Errorf("sensor pairing request failed: %s", userFacingGRPCError(err))
	}
}

// pairingName returns the user-supplied name, or a sensible default derived
// from the source device's display name.
func pairingName(name string, source *models.DiscoveredDevice) string {
	if name != "" {
		return name
	}
	return source.DisplayName
}

func newDevicePairCmd() *cobra.Command {
	var (
		listOnly      bool
		name          string
		sensors       []string
		unitreeTab    bool
		unitreeIP     string
		unitreeRegion string
	)
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair or forget Bluetooth, SensorLink, IP cameras, and Unitree robots",
		Long:  "Discover, pair, and forget devices in the Bluetooth, SensorLink, IP Cameras, and Unitree tabs. Bluetooth, SensorLink sources, IP cameras, and Unitree Go2 robots are discovered on the selected WendyOS device's network, so you can pair from anywhere; if the device's agent is too old to scan for SensorLink sources, that tab scans your local network instead and says so. Unitree login retrieves your robot's key, which the selected WendyOS device stores and uses to connect. Use --ip to enter a robot's IPv4 address. SensorLink devices must be in an organization you belong to.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("ip") {
				var err error
				unitreeIP, err = unitreePairIPv4(unitreeIP)
				if err != nil {
					return fmt.Errorf("--ip: %w", err)
				}
			}
			if unitreeRegion != "global" && unitreeRegion != "cn" {
				return fmt.Errorf("--unitree-region must be global or cn")
			}
			if listOnly && (unitreeTab || cmd.Flags().Changed("ip") || cmd.Flags().Changed("unitree-region")) {
				return fmt.Errorf("--list prints SensorLink pairings and cannot be combined with Unitree flags")
			}
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			if listOnly {
				resp, err := conn.SensorPairingService.ListSensorPairings(ctx, &agentpbv2.ListSensorPairingsRequest{})
				if err != nil {
					return cleanRPCError(err) // maps codes to human text, never raw "rpc error: code ="
				}
				for _, p := range resp.Pairings {
					state := "disconnected"
					if p.Connected {
						state = "connected"
					}
					cliLogln("%-20s %s  %s", p.Name, sensorPairLabel(p.SourceAssetId, p.SourcePrincipalUri), state)
				}
				return nil
			}

			pairCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			model := newDevicePairModel(
				&btTUIHandler{ctx: pairCtx, agent: conn.AgentService},
				&sensorPairHandler{
					ctx: pairCtx, client: conn.SensorPairingService,
					discoverLocal: discoverLocalSensorSources, orgIDs: cliOrgIDs, tenantIDs: cliTenantIDs,
					name: name, sensors: sensors,
				},
				&cameraPairHandler{ctx: pairCtx, client: conn.VideoService, preflight: func(ctx context.Context) error {
					return cameraServicePreflight(ctx, conn, cameraNetworkManagement)
				}},
				&unitreePairHandler{ctx: pairCtx, client: conn.UnitreeService, region: unitreeRegion, initialIP: unitreeIP, transportError: unitreePairTransportError(conn)},
			)
			if cmd.Flags().Changed("name") || cmd.Flags().Changed("sensors") {
				model.active = pairSensorLinkTab
			}
			if unitreeTab || cmd.Flags().Changed("ip") || cmd.Flags().Changed("unitree-region") {
				model.active = pairUnitreeTab
			}
			if _, err := tea.NewProgram(model, tea.WithContext(pairCtx)).Run(); err != nil {
				return fmt.Errorf("device pairing: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&listOnly, "list", false, "list current SensorLink pairings")
	cmd.Flags().StringVar(&name, "name", "", "friendly name for a SensorLink pairing")
	cmd.Flags().StringSliceVar(&sensors, "sensors", nil, "limit a SensorLink pairing to these sensor names (default: all)")
	cmd.Flags().BoolVar(&unitreeTab, "unitree", false, "open the Unitree Go2 pairing tab")
	cmd.Flags().StringVar(&unitreeIP, "ip", "", "Unitree Go2 IPv4 address on the selected WendyOS device's network")
	cmd.Flags().StringVar(&unitreeRegion, "unitree-region", "global", "Unitree account region: global or cn")
	return cmd
}

// newDeviceUnpairCmd removes a sensor pairing. With no argument it shows a
// picker of the device's current pairings; with a source asset id it removes
// that pairing directly (handy for scripts).
func newDeviceUnpairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Hidden: true,
		Use:    "unpair [source-asset-id-or-principal]",
		Short:  "Remove a sensor-source pairing",
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			var assetID int32
			var principal string
			if len(args) == 1 {
				if strings.HasPrefix(args[0], "spiffe://") {
					if _, err := certs.ParseDevicePrincipal(args[0]); err != nil {
						return err
					}
					principal = args[0]
				} else if _, err := fmt.Sscanf(args[0], "%d", &assetID); err != nil {
					return fmt.Errorf("invalid source asset id %q: %w", args[0], err)
				}
			} else {
				// No id given: pick from the device's current pairings.
				resp, err := conn.SensorPairingService.ListSensorPairings(ctx, &agentpbv2.ListSensorPairingsRequest{})
				if err != nil {
					return cleanRPCError(err)
				}
				if len(resp.Pairings) == 0 {
					return fmt.Errorf("no sensor pairings to remove")
				}
				items := make([]tui.PickerItem, 0, len(resp.Pairings))
				for _, p := range resp.Pairings {
					state := "disconnected"
					if p.Connected {
						state = "connected"
					}
					items = append(items, tui.PickerItem{
						Name:     p.Name,
						Address:  fmt.Sprintf("%s · %s", sensorPairLabel(p.SourceAssetId, p.SourcePrincipalUri), state),
						DedupKey: sensorPairKey(p.SourceAssetId, p.SourcePrincipalUri),
						Value:    p,
					})
				}
				sel, err := runPicker("Select a pairing to remove", items)
				if err != nil {
					return err
				}
				selected := sel.Value.(*agentpbv2.SensorPairing)
				assetID, principal = selected.SourceAssetId, selected.SourcePrincipalUri
			}

			if _, err := conn.SensorPairingService.RemoveSensorPairing(ctx, &agentpbv2.RemoveSensorPairingRequest{
				SourceAssetId: assetID, SourcePrincipalUri: principal,
			}); err != nil {
				return cleanRPCError(err)
			}
			cliSuccess("Unpaired %s.", sensorPairLabel(assetID, principal))
			return nil
		},
	}
	return cmd
}

func cliTenantIDs() (map[string]bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	tenants := make(map[string]bool)
	for _, auth := range cfg.Auth {
		for _, cert := range auth.Certificates {
			if tenant := cert.TenantUUID(); tenant != "" {
				tenants[tenant] = true
			}
		}
	}
	if len(tenants) == 0 {
		return nil, config.ErrNotLoggedIn
	}
	return tenants, nil
}
