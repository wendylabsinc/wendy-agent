package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

// Register this constructor under the device command's manage group.
func newDeviceLocalMeshCmd() *cobra.Command {
	root := &cobra.Command{Use: "wendy-net", Aliases: []string{"local-mesh", "nan-mesh"}, Short: "Configure wendy net carriers and Internet sharing"}
	configure := &cobra.Command{Use: "configure", Short: "Update selected wendy net settings", Args: cobra.NoArgs}
	configure.Flags().Bool("participate", false, "Participate in the wendy net")
	configure.Flags().Bool("roam", false, "Use signed wendy net Internet when an independent uplink is unavailable")
	configure.Flags().Bool("share-uplink", false, "Offer an independent uplink to the mesh")
	configure.Flags().Bool("nan", false, "Enable Wi-Fi Aware NAN as a mesh carrier")
	configure.Flags().Bool("ble", false, "Enable BLE L2CAP as a mesh carrier")
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
			return fmt.Errorf("wendy net settings require a WendyOS agent")
		}
		client := pb.NewWendyLocalMeshAdminServiceClient(target.Agent.Conn)
		response, err := client.ConfigureLocalMesh(cmd.Context(), req)
		if err != nil {
			return err
		}
		return printLocalMeshStatus(cmd, response)
	}
	inspect := &cobra.Command{Use: "status", Short: "Show saved wendy net settings and runtime state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := resolveTarget(cmd.Context(), SuppressUpdateCheck())
		if err != nil {
			return err
		}
		defer target.Close()
		if target.Agent == nil || target.Agent.Conn == nil {
			return fmt.Errorf("wendy net status requires a WendyOS agent")
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
	}{{"participate", &req.Participate}, {"roam", &req.Roam}, {"share-uplink", &req.ShareUplink}, {"nan", &req.Nan}, {"ble", &req.Ble}} {
		if cmd.Flags().Changed(field.name) {
			value, err := cmd.Flags().GetBool(field.name)
			if err != nil {
				return nil, err
			}
			*field.dest = &value
		}
	}
	if req.Participate == nil && req.Roam == nil && req.ShareUplink == nil && req.Nan == nil && req.Ble == nil {
		return nil, fmt.Errorf("specify --participate, --roam, --share-uplink, --nan or --ble; omitted flags preserve saved settings")
	}
	return req, nil
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
	cmd.Printf("Participate: %t\nRoam: %t\nShare uplink: %t\nNAN: %t\nBLE: %t\nTCP listen: %s\nConfigured TCP peers: %d\nAuthenticated peers: %d\nGateway principal: %s\nState: %s\n%s\n",
		configured.GetParticipate(), configured.GetRoam(), configured.GetShareUplink(), configured.GetNan(), configured.GetBle(), configured.GetTcpListen(), configured.GetConfiguredTcpPeers(),
		status.GetAuthenticatedPeers(), status.GetGatewayPrincipal(), status.GetState(), status.GetDetail())
	return nil
}
