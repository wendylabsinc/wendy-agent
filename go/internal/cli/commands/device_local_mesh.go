package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/browserauth"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	pb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

var connectLocalMeshTopology = connectToAgent
var resolveLocalMeshTarget = resolveTarget
var errNoLocalMeshSettings = errors.New("specify --participate, --roam, --share-uplink, --nan, --ble, --ethernet or --infrastructure-wifi; omitted flags preserve saved settings; run without flags in an interactive terminal to choose settings")
var observedLocalMeshTopologyIdentity = func(conn *grpcclient.AgentConnection) (certs.WendyIdentity, bool) {
	return conn.ObservedServerIdentity()
}

const localMeshTopologyTimeout = 15 * time.Second

// Keep the previous path available to desktop clients and existing scripts.
func newLegacyDeviceLocalMeshCmd() *cobra.Command {
	cmd := newNetCmd()
	cmd.Use = "wendy-net"
	cmd.Aliases = []string{"local-mesh", "nan-mesh"}
	cmd.Hidden = true
	return cmd
}

func newNetCmd() *cobra.Command {
	root := &cobra.Command{Use: "net", Short: "Inspect and configure Wendy Net"}
	configure := &cobra.Command{
		Use: "configure", Short: "Update selected Wendy Net settings", Args: cobra.NoArgs,
		Long: "Update Wendy Net settings. Without settings flags, choose a device and edit its current settings interactively. Use arrows to navigate, space to toggle, and Enter to save. Explicit flags update only the specified settings.",
	}
	configure.Flags().Bool("participate", false, "Join and relay Wendy Net (false stops all carriers; saved carrier choices are retained)")
	configure.Flags().Bool("roam", false, "Use Wendy Net Internet when an independent uplink is unavailable")
	configure.Flags().Bool("share-uplink", false, "Offer an independent uplink to the net")
	configure.Flags().Bool("nan", false, "Enable Wi-Fi Aware NAN as a net carrier")
	configure.Flags().Bool("ble", false, "Enable BLE L2CAP as a net carrier")
	configure.Flags().Bool("ethernet", false, "Discover local net peers over Ethernet")
	configure.Flags().Bool("infrastructure-wifi", false, "Discover local net peers over infrastructure Wi-Fi")
	configure.RunE = func(cmd *cobra.Command, _ []string) error {
		req, err := localMeshRequest(cmd)
		interactive := errors.Is(err, errNoLocalMeshSettings) && !jsonOutput && isInteractiveTerminal()
		if err != nil && !interactive {
			return err
		}
		target, err := resolveLocalMeshTarget(cmd.Context(), SuppressUpdateCheck(), SuppressPickerEnroll())
		if err != nil {
			return err
		}
		defer target.Close()
		if target.Agent == nil || target.Agent.Conn == nil {
			return fmt.Errorf("Wendy Net settings require a WendyOS agent")
		}
		client := pb.NewWendyLocalMeshAdminServiceClient(target.Agent.Conn)
		if interactive {
			req, err = interactiveLocalMeshRequest(cmd.Context(), client)
			if err != nil {
				return err
			}
			if req == nil {
				cmd.Println("Wendy Net settings unchanged.")
				return nil
			}
		}
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
	root.AddCommand(configure, inspect, newDeviceLocalMeshTopologyCmd())
	return root
}

func newDeviceLocalMeshTopologyCmd() *cobra.Command {
	var observation bool
	cmd := &cobra.Command{
		Use:   "topology",
		Short: "Show the selected device's observed local net peers, routes and route proofs",
		Long:  "Reads one live topology snapshot from the selected agent. Observations are local to that device; they do not prove fleet-wide reachability. Use --json for the complete snapshot.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if observation && !jsonOutput {
				return fmt.Errorf("--observation requires --json")
			}
			topology, err := readLocalMeshTopology(cmd.Context())
			if observation {
				return printLocalMeshObservation(cmd, topology, err)
			}
			if err != nil {
				return err
			}
			return printLocalMeshTopology(cmd, topology)
		},
	}
	cmd.Flags().BoolVar(&observation, "observation", false, "With --json, emit a typed observation result including unsupported, unavailable and unauthorized states")
	return cmd
}

func readLocalMeshTopology(parent context.Context) (*pb.LocalMeshTopology, error) {
	interactive := !jsonOutput && isInteractiveTerminal()
	connectCtx := parent
	if !interactive {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(parent, localMeshTopologyTimeout)
		defer cancel()
	}
	// Allow initial device selection in a terminal. Named targets still use
	// monitoring's connection policy, without updates or recovery to another device.
	conn, err := connectLocalMeshTopology(connectCtx, ReadOnlyMonitoring(), func(c *resolveConfig) {
		c.nonInteractive = !interactive
	})
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("local topology requires a WendyOS agent")
	}
	defer conn.Close()
	if conn.Conn == nil {
		return nil, fmt.Errorf("local topology requires a WendyOS agent")
	}
	// Choosing a device must not consume the snapshot's time budget.
	ctx, cancel := context.WithTimeout(connectCtx, localMeshTopologyTimeout)
	defer cancel()
	client := pb.NewWendyLocalMeshAdminServiceClient(conn.Conn)
	topology, err := client.GetLocalMeshTopology(ctx, &pb.GetLocalMeshTopologyRequest{})
	if err != nil {
		return nil, fmt.Errorf("reading local topology: %w", err)
	}
	peer, verified := observedLocalMeshTopologyIdentity(conn)
	if !verified {
		return nil, status.Error(codes.Unauthenticated, "topology reporter identity was not verified")
	}
	if err := browserauth.ValidateTopologyReporter(topology, peer); err != nil {
		return nil, err
	}
	return topology, nil
}

func printLocalMeshObservation(cmd *cobra.Command, topology *pb.LocalMeshTopology, readErr error) error {
	var observation browserauth.TopologyObservation
	if readErr != nil {
		observation = browserauth.TopologyFailure(readErr)
	} else {
		data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(topology)
		if err != nil {
			observation = browserauth.TopologyFailure(err)
		} else {
			observation = browserauth.TopologyObservation{Status: "ok", Topology: data, ObservedAt: time.Now().UTC()}
		}
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(observation)
}

func printLocalMeshTopology(cmd *cobra.Command, topology *pb.LocalMeshTopology) error {
	if topology == nil {
		return fmt.Errorf("agent returned no local topology snapshot")
	}
	if jsonOutput {
		data, err := (protojson.MarshalOptions{Indent: "  ", EmitUnpopulated: true}).Marshal(topology)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	captured := "unknown"
	if topology.GetCapturedAtUnixMs() > 0 {
		captured = time.UnixMilli(topology.GetCapturedAtUnixMs()).UTC().Format(time.RFC3339Nano)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Device: %s\nNet address: %s\nCaptured: %s\nRuntime available: %t\nState: %s\n%s\nObserved peers: %d\nRoutes: %d\nRoute announcements: %d\nSnapshot truncated: %t\nUse --json for peer, route and proof details.\n",
		topology.GetSelfPrincipal(), topology.GetSelfMeshAddress(), captured, topology.GetRuntimeAvailable(),
		localNetStateLabel(topology.GetStatus().GetState()), topology.GetStatus().GetDetail(), len(topology.GetPeers()),
		len(topology.GetRoutes()), len(topology.GetAnnouncements()), topology.GetTruncated())
	return err
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
		return nil, errNoLocalMeshSettings
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
