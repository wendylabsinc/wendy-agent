package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

// Register this constructor under the device command's manage group.
func newDeviceLocalMeshCmd() *cobra.Command {
	root := &cobra.Command{Use: "wendy-net", Aliases: []string{"local-mesh", "nan-mesh"}, Short: "Configure Wendy Net carriers and Internet sharing"}
	configure := &cobra.Command{Use: "configure", Short: "Update selected Wendy Net settings", Args: cobra.NoArgs}
	configure.Flags().Bool("participate", false, "Join and relay Wendy Net (false stops all carriers; saved carrier choices are retained)")
	configure.Flags().Bool("roam", false, "Use Wendy Net Internet when an independent uplink is unavailable")
	configure.Flags().Bool("share-uplink", false, "Offer an independent uplink to the net")
	configure.Flags().Bool("nan", false, "Enable Wi-Fi Aware NAN as a net carrier")
	configure.Flags().Bool("ble", false, "Enable BLE L2CAP as a net carrier")
	configure.Flags().Bool("ethernet", false, "Discover local net peers over Ethernet")
	configure.Flags().Bool("infrastructure-wifi", false, "Discover local net peers over infrastructure Wi-Fi")
	configure.RunE = func(cmd *cobra.Command, _ []string) error {
		req, err := localMeshRequest(cmd)
		if err != nil {
			return err
		}
		target, err := resolveTarget(cmd.Context(), SuppressUpdateCheck())
		if err != nil {
			return err
		}
		defer target.Close()
		if target.Agent == nil || target.Agent.Conn == nil {
			return fmt.Errorf("Wendy Net settings require a WendyOS agent")
		}
		client := pb.NewWendyLocalMeshAdminServiceClient(target.Agent.Conn)
		response, err := client.ConfigureLocalMesh(cmd.Context(), req)
		if err != nil {
			return err
		}
		return printLocalMeshStatus(cmd, response)
	}
	inspect := &cobra.Command{Use: "status", Short: "Show saved Wendy Net settings and runtime state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := resolveTarget(cmd.Context(), SuppressUpdateCheck())
		if err != nil {
			return err
		}
		defer target.Close()
		if target.Agent == nil || target.Agent.Conn == nil {
			return fmt.Errorf("Wendy Net status requires a WendyOS agent")
		}
		client := pb.NewWendyLocalMeshAdminServiceClient(target.Agent.Conn)
		response, err := client.GetLocalMeshStatus(cmd.Context(), &pb.GetLocalMeshStatusRequest{})
		if err != nil {
			return err
		}
		return printLocalMeshStatus(cmd, response)
	}}
	root.AddCommand(configure, inspect)
	return root
}

func localMeshRequest(cmd *cobra.Command) (*pb.ConfigureLocalMeshRequest, error) {
	req := &pb.ConfigureLocalMeshRequest{}
	for _, field := range []struct {
		name string
		dest **bool
	}{{"participate", &req.Participate}, {"roam", &req.Roam}, {"share-uplink", &req.ShareUplink}, {"nan", &req.Nan}, {"ble", &req.Ble}, {"ethernet", &req.Ethernet}, {"infrastructure-wifi", &req.InfrastructureWifi}} {
		if cmd.Flags().Changed(field.name) {
			value, err := cmd.Flags().GetBool(field.name)
			if err != nil {
				return nil, err
			}
			*field.dest = &value
		}
	}
	if req.Participate == nil && req.Roam == nil && req.ShareUplink == nil && req.Nan == nil && req.Ble == nil && req.Ethernet == nil && req.InfrastructureWifi == nil {
		return nil, fmt.Errorf("specify --participate, --roam, --share-uplink, --nan, --ble, --ethernet or --infrastructure-wifi; omitted flags preserve saved settings")
	}
	return req, nil
}

func localNetStateLabel(state string) string {
	if state == "mesh-active" {
		return "Local peers active"
	}
	return state
}

func printLocalMeshStatus(cmd *cobra.Command, status *pb.LocalMeshStatus) error {
	if jsonOutput {
		data, err := (protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: true}).Marshal(status)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	configured := status.GetConfigured()
	cmd.Printf("Participate: %t\nRoam: %t\nShare uplink: %t\nNAN: %t\nBLE: %t\nEthernet: %t\nInfrastructure Wi-Fi: %t\nTCP listen: %s\nConfigured TCP peers: %d\nAuthenticated peers: %d\nGateway principal: %s\nState: %s\n%s\n",
		configured.GetParticipate(), configured.GetRoam(), configured.GetShareUplink(), configured.GetNan(), configured.GetBle(), configured.GetEthernet(), configured.GetInfrastructureWifi(), configured.GetTcpListen(), configured.GetConfiguredTcpPeers(),
		status.GetAuthenticatedPeers(), status.GetGatewayPrincipal(), localNetStateLabel(status.GetState()), status.GetDetail())
	return nil
}
