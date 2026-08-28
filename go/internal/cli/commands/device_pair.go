package commands

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// sensorlinkPort is the fixed port a sensorlink-capable device's SensorPairing
// agent listens on.
const sensorlinkPort = 50060

// sensorSourceItems filters discovered devices to sensor-source-capable ones
// (advertising sensorlink=true) and builds picker rows for them.
func sensorSourceItems(devs []models.DiscoveredDevice) []tui.PickerItem {
	var items []tui.PickerItem
	for i := range devs {
		d := devs[i]
		if !d.Sensorlink {
			continue
		}
		items = append(items, tui.PickerItem{
			Name:     d.DisplayName,
			Address:  d.IPAddress,
			DedupKey: fmt.Sprintf("asset-%d", d.AssetID),
			Value:    &devs[i],
		})
	}
	return items
}

// sameOrg rejects pairing across organizations: sensor pairing is only
// allowed between devices in the same Wendy Cloud org.
func sameOrg(cliOrg, sourceOrg int32) error {
	if cliOrg != sourceOrg {
		return fmt.Errorf("device is in a different organization (yours: %d, device: %d); pairing is only allowed within one organization", cliOrg, sourceOrg)
	}
	return nil
}

// discoverSensorSources runs LAN discovery and returns the merged device list
// for the caller to filter down to sensor sources.
func discoverSensorSources(ctx context.Context) ([]models.DiscoveredDevice, error) {
	collection, err := discovery.Discover(ctx, discovery.DiscoveryOptions{
		Types: []models.InterfaceType{models.InterfaceLAN},
	})
	if err != nil {
		return nil, fmt.Errorf("discovering devices: %w", err)
	}
	return collection.MergedDevices(), nil
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

// currentCLIOrgID returns the organization ID of the CLI's own auth session,
// resolved the same way other commands pick a default session.
func currentCLIOrgID(_ context.Context) (int32, error) {
	auth, err := pickAuthEntry("")
	if err != nil {
		return 0, err
	}
	return int32(auth.Certificates[0].OrganizationID), nil
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
		return fmt.Errorf("sensor pairing request failed: %v", status.Convert(err).Message())
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
		listOnly bool
		name     string
		sensors  []string
	)
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair a sensor-source device (e.g. an ESP32) to this device",
		Long:  "Select a sensor-source device on your network and mount its cameras, microphones, and sensors locally. Both devices must be in the same organization.",
		RunE: func(cmd *cobra.Command, args []string) error {
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
					status := "disconnected"
					if p.Connected {
						status = "connected"
					}
					cliLogln("%-20s asset %d  %s", p.Name, p.SourceAssetId, status)
				}
				return nil
			}

			// Discover, filter to sensor sources, pick one.
			devs, err := discoverSensorSources(ctx) // wraps discovery.Discover + MergedDevices
			if err != nil {
				return err
			}
			items := sensorSourceItems(devs)
			if len(items) == 0 {
				return fmt.Errorf("no sensor-source devices found on your network")
			}
			sel, err := runPicker("Select a sensor source", items)
			if err != nil {
				return err
			}
			source := sel.Value.(*models.DiscoveredDevice)

			cliOrg, err := currentCLIOrgID(ctx) // auth.Certificates[0].OrganizationID
			if err != nil {
				return err
			}
			if err := sameOrg(cliOrg, source.OrgID); err != nil {
				return err
			}

			addr := fmt.Sprintf("%s:%d", source.IPAddress, sensorlinkPort)
			_, err = conn.SensorPairingService.AddSensorPairing(ctx, &agentpbv2.AddSensorPairingRequest{
				SourceAssetId:   source.AssetID,
				SourceAddress:   addr,
				Name:            pairingName(name, source),
				SensorAllowlist: sensors,
			})
			if err != nil {
				return cleanRPCError(err)
			}
			cliSuccess("Paired %s. Its sensors will appear on this device.", source.DisplayName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&listOnly, "list", false, "list current pairings")
	cmd.Flags().StringVar(&name, "name", "", "friendly name for the pairing")
	cmd.Flags().StringSliceVar(&sensors, "sensors", nil, "limit to these sensor names (default: all)")
	return cmd
}

// newDeviceUnpairCmd removes a sensor pairing by source asset ID.
func newDeviceUnpairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unpair <source-asset-id>",
		Short: "Remove a sensor-source pairing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			var assetID int32
			if _, err := fmt.Sscanf(args[0], "%d", &assetID); err != nil {
				return fmt.Errorf("invalid source asset id %q: %w", args[0], err)
			}

			if _, err := conn.SensorPairingService.RemoveSensorPairing(ctx, &agentpbv2.RemoveSensorPairingRequest{
				SourceAssetId: assetID,
			}); err != nil {
				return cleanRPCError(err)
			}
			cliSuccess("Unpaired asset %d.", assetID)
			return nil
		},
	}
	return cmd
}
