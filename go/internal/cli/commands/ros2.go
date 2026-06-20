package commands

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// newROS2Cmd builds the `wendy device ros2` command group: live ROS 2
// inspection of a device with zero SSH (WDY-1333).
func newROS2Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ros2",
		Short: "Inspect and debug live ROS 2 systems on the device",
		Long: `Inspect and debug live ROS 2 systems running on a WendyOS device.

The agent discovers ROS 2 app containers (deployed with a "frameworks.ros2"
config in wendy.json), starts a CLI sidecar in the same DDS domain, and runs
ros2 commands there — no SSH and no setup.bash sourcing required.`,
	}

	cmd.AddCommand(
		newROS2NodesCmd(),
		newROS2TopicsCmd(),
		newROS2TopicCmd(),
		newROS2ServicesCmd(),
		newROS2CallCmd(),
		newROS2ParamsCmd(),
		newROS2ParamCmd(),
		newROS2EchoCmd(),
		newROS2HzCmd(),
		newROS2GraphCmd(),
		newROS2BagCmd(),
		newROS2DoctorCmd(),
		newROS2ExecCmd(),
	)
	return cmd
}

// ── connection plumbing ─────────────────────────────────────────────

// ros2Client wraps the typed gRPC client plus the resolved device target.
type ros2Client struct {
	client agentpbv2.ROS2ServiceClient
	target *SelectedDevice
}

func newROS2Client(ctx context.Context) (*ros2Client, error) {
	target, err := resolveTarget(ctx, ExcludeProviders("local", "docker"))
	if err != nil {
		return nil, err
	}
	if target.Agent == nil {
		target.Close()
		return nil, fmt.Errorf("ROS 2 inspection requires a WendyOS agent connection (Bluetooth and external devices are not supported)")
	}
	return &ros2Client{
		client: agentpbv2.NewROS2ServiceClient(target.Agent.Conn),
		target: target,
	}, nil
}

func (c *ros2Client) Close() { c.target.Close() }

// ros2RPCError translates transport errors into actionable messages.
func ros2RPCError(err error) error {
	switch status.Code(err) {
	case codes.Unimplemented:
		return fmt.Errorf("this device's agent does not support ROS 2 inspection; update it with `wendy device update`")
	case codes.FailedPrecondition:
		return errors.New(status.Convert(err).Message())
	}
	return err
}

// ros2DomainFlag registers the shared --domain override flag.
func ros2DomainFlag(cmd *cobra.Command, domain *int32) {
	cmd.Flags().Int32Var(domain, "domain", -1, "ROS_DOMAIN_ID override (default: from the app's ros2 config)")
}

func ros2DomainPtr(domain int32) *int32 {
	if domain < 0 {
		return nil
	}
	return &domain
}

func printROS2JSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ── inspection commands ─────────────────────────────────────────────

// ros2RMWShort renders an RMW identifier compactly for display, e.g.
// "rmw_cyclonedds_cpp" -> "cyclonedds".
func ros2RMWShort(rmw string) string {
	return strings.TrimSuffix(strings.TrimPrefix(rmw, "rmw_"), "_cpp")
}

// ros2ShowRMWTags reports whether the results span more than one RMW, so the
// per-result "[rmw]" tag is shown only on mixed-RMW devices and never clutters
// the common single-RMW case (WDY-1594).
func ros2ShowRMWTags(rmws ...string) bool {
	seen := map[string]struct{}{}
	for _, r := range rmws {
		if r != "" {
			seen[r] = struct{}{}
		}
	}
	return len(seen) > 1
}

func newROS2NodesCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "List running ROS 2 nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.ListNodes(cmd.Context(), &agentpbv2.ListROS2NodesRequest{DomainId: ros2DomainPtr(domain)})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				type node struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
					RMW       string `json:"rmw,omitempty"`
				}
				nodes := make([]node, 0, len(resp.GetNodes()))
				for _, n := range resp.GetNodes() {
					nodes = append(nodes, node{Name: n.GetName(), Namespace: n.GetNamespace(), RMW: n.GetRmw()})
				}
				return printROS2JSON(nodes)
			}
			if len(resp.GetNodes()) == 0 {
				cliLogln("No ROS 2 nodes found.")
				return nil
			}
			rmws := make([]string, 0, len(resp.GetNodes()))
			for _, n := range resp.GetNodes() {
				rmws = append(rmws, n.GetRmw())
			}
			showTags := ros2ShowRMWTags(rmws...)
			for _, n := range resp.GetNodes() {
				line := ros2GraphNodeFQN(n)
				if showTags && n.GetRmw() != "" {
					line += "  [" + ros2RMWShort(n.GetRmw()) + "]"
				}
				fmt.Println(line)
			}
			return nil
		},
	}
	ros2DomainFlag(cmd, &domain)
	return cmd
}

func newROS2TopicsCmd() *cobra.Command {
	var domain int32
	var all bool
	cmd := &cobra.Command{
		Use:   "topics",
		Short: "List ROS 2 topics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.ListTopics(cmd.Context(), &agentpbv2.ListROS2TopicsRequest{
				DomainId:      ros2DomainPtr(domain),
				IncludeCounts: all,
			})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				type topic struct {
					Name        string   `json:"name"`
					Types       []string `json:"types"`
					Publishers  int32    `json:"publishers,omitempty"`
					Subscribers int32    `json:"subscribers,omitempty"`
					RMW         string   `json:"rmw,omitempty"`
				}
				topics := make([]topic, 0, len(resp.GetTopics()))
				for _, t := range resp.GetTopics() {
					topics = append(topics, topic{
						Name:        t.GetName(),
						Types:       t.GetTypes(),
						Publishers:  t.GetPublisherCount(),
						Subscribers: t.GetSubscriberCount(),
						RMW:         t.GetRmw(),
					})
				}
				return printROS2JSON(topics)
			}
			if len(resp.GetTopics()) == 0 {
				cliLogln("No ROS 2 topics found.")
				return nil
			}
			rmws := make([]string, 0, len(resp.GetTopics()))
			for _, t := range resp.GetTopics() {
				rmws = append(rmws, t.GetRmw())
			}
			rmwCol := ros2ShowRMWTags(rmws...)
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			switch {
			case all && rmwCol:
				fmt.Fprintln(w, "TOPIC\tTYPE\tPUBS\tSUBS\tRMW")
				for _, t := range resp.GetTopics() {
					fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", t.GetName(), strings.Join(t.GetTypes(), ", "), t.GetPublisherCount(), t.GetSubscriberCount(), ros2RMWShort(t.GetRmw()))
				}
			case all:
				fmt.Fprintln(w, "TOPIC\tTYPE\tPUBS\tSUBS")
				for _, t := range resp.GetTopics() {
					fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", t.GetName(), strings.Join(t.GetTypes(), ", "), t.GetPublisherCount(), t.GetSubscriberCount())
				}
			case rmwCol:
				fmt.Fprintln(w, "TOPIC\tTYPE\tRMW")
				for _, t := range resp.GetTopics() {
					fmt.Fprintf(w, "%s\t%s\t%s\n", t.GetName(), strings.Join(t.GetTypes(), ", "), ros2RMWShort(t.GetRmw()))
				}
			default:
				fmt.Fprintln(w, "TOPIC\tTYPE")
				for _, t := range resp.GetTopics() {
					fmt.Fprintf(w, "%s\t%s\n", t.GetName(), strings.Join(t.GetTypes(), ", "))
				}
			}
			return w.Flush()
		},
	}
	ros2DomainFlag(cmd, &domain)
	cmd.Flags().BoolVar(&all, "all", false, "Include publisher/subscriber counts")
	return cmd
}

func newROS2TopicCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topic",
		Short: "Topic subcommands",
	}
	var domain int32
	info := &cobra.Command{
		Use:   "info <topic>",
		Short: "Show type, endpoints, and QoS for a topic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.GetTopicInfo(cmd.Context(), &agentpbv2.GetROS2TopicInfoRequest{
				DomainId: ros2DomainPtr(domain),
				Topic:    args[0],
			})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				return printROS2JSON(map[string]any{
					"name":        resp.GetTopic().GetName(),
					"types":       resp.GetTopic().GetTypes(),
					"publishers":  resp.GetTopic().GetPublisherCount(),
					"subscribers": resp.GetTopic().GetSubscriberCount(),
					"verbose":     resp.GetVerbose(),
				})
			}
			fmt.Print(resp.GetVerbose())
			return nil
		},
	}
	ros2DomainFlag(info, &domain)
	cmd.AddCommand(info)
	return cmd
}

func newROS2ServicesCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "services",
		Short: "List ROS 2 services",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.ListServices(cmd.Context(), &agentpbv2.ListROS2ServicesRequest{DomainId: ros2DomainPtr(domain)})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				type svc struct {
					Name  string   `json:"name"`
					Types []string `json:"types"`
					RMW   string   `json:"rmw,omitempty"`
				}
				svcs := make([]svc, 0, len(resp.GetServices()))
				for _, s := range resp.GetServices() {
					svcs = append(svcs, svc{Name: s.GetName(), Types: s.GetTypes(), RMW: s.GetRmw()})
				}
				return printROS2JSON(svcs)
			}
			if len(resp.GetServices()) == 0 {
				cliLogln("No ROS 2 services found.")
				return nil
			}
			rmws := make([]string, 0, len(resp.GetServices()))
			for _, s := range resp.GetServices() {
				rmws = append(rmws, s.GetRmw())
			}
			rmwCol := ros2ShowRMWTags(rmws...)
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			if rmwCol {
				fmt.Fprintln(w, "SERVICE\tTYPE\tRMW")
				for _, s := range resp.GetServices() {
					fmt.Fprintf(w, "%s\t%s\t%s\n", s.GetName(), strings.Join(s.GetTypes(), ", "), ros2RMWShort(s.GetRmw()))
				}
			} else {
				fmt.Fprintln(w, "SERVICE\tTYPE")
				for _, s := range resp.GetServices() {
					fmt.Fprintf(w, "%s\t%s\n", s.GetName(), strings.Join(s.GetTypes(), ", "))
				}
			}
			return w.Flush()
		},
	}
	ros2DomainFlag(cmd, &domain)
	return cmd
}

func newROS2CallCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "call <service> <type> [request]",
		Short: "Call a ROS 2 service",
		Long:  "Call a ROS 2 service, e.g.\n  wendy device ros2 call /reset std_srvs/srv/Empty\n  wendy device ros2 call /set_speed my_msgs/srv/SetSpeed '{speed: 1.5}'",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			req := &agentpbv2.CallROS2ServiceRequest{
				DomainId: ros2DomainPtr(domain),
				Service:  args[0],
				Type:     args[1],
			}
			if len(args) == 3 {
				req.Request = args[2]
			}
			resp, err := client.client.CallService(cmd.Context(), req)
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				return printROS2JSON(map[string]any{"success": resp.GetSuccess(), "response": resp.GetResponse()})
			}
			fmt.Println(resp.GetResponse())
			if !resp.GetSuccess() {
				return fmt.Errorf("service call failed")
			}
			return nil
		},
	}
	ros2DomainFlag(cmd, &domain)
	return cmd
}

func newROS2ParamsCmd() *cobra.Command {
	var domain int32
	var node string
	cmd := &cobra.Command{
		Use:   "params",
		Short: "List parameters across all nodes (or one node)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.ListParams(cmd.Context(), &agentpbv2.ListROS2ParamsRequest{
				DomainId: ros2DomainPtr(domain),
				Node:     node,
			})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				params := make(map[string][]string, len(resp.GetNodes()))
				for _, n := range resp.GetNodes() {
					params[n.GetNode()] = n.GetParams()
				}
				return printROS2JSON(params)
			}
			for _, n := range resp.GetNodes() {
				fmt.Printf("%s:\n", n.GetNode())
				for _, p := range n.GetParams() {
					fmt.Printf("  %s\n", p)
				}
			}
			return nil
		},
	}
	ros2DomainFlag(cmd, &domain)
	cmd.Flags().StringVar(&node, "node", "", "Only list parameters of this node")
	return cmd
}

func newROS2ParamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "param",
		Short: "Get or set node parameters",
	}

	var getDomain int32
	get := &cobra.Command{
		Use:   "get <node> <param>",
		Short: "Get a parameter value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.GetParam(cmd.Context(), &agentpbv2.GetROS2ParamRequest{
				DomainId: ros2DomainPtr(getDomain),
				Node:     args[0],
				Param:    args[1],
			})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				return printROS2JSON(map[string]string{"node": args[0], "param": args[1], "value": resp.GetValue()})
			}
			fmt.Println(resp.GetValue())
			return nil
		},
	}
	ros2DomainFlag(get, &getDomain)

	var setDomain int32
	set := &cobra.Command{
		Use:   "set <node> <param> <value>",
		Short: "Set a parameter value live — no restart needed",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.SetParam(cmd.Context(), &agentpbv2.SetROS2ParamRequest{
				DomainId: ros2DomainPtr(setDomain),
				Node:     args[0],
				Param:    args[1],
				Value:    args[2],
			})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				return printROS2JSON(map[string]any{"success": resp.GetSuccess(), "message": resp.GetMessage()})
			}
			if !resp.GetSuccess() {
				return fmt.Errorf("setting parameter: %s", resp.GetMessage())
			}
			cliSuccess("%s", resp.GetMessage())
			return nil
		},
	}
	ros2DomainFlag(set, &setDomain)

	cmd.AddCommand(get, set)
	return cmd
}

func newROS2DoctorCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run ros2 doctor health checks on the device",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.Doctor(cmd.Context(), &agentpbv2.ROS2DoctorRequest{DomainId: ros2DomainPtr(domain)})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				return printROS2JSON(map[string]string{"report": resp.GetReport()})
			}
			fmt.Print(resp.GetReport())
			return nil
		},
	}
	ros2DomainFlag(cmd, &domain)
	return cmd
}

// ── streaming commands ──────────────────────────────────────────────

func newROS2EchoCmd() *cobra.Command {
	var domain int32
	var count int32
	cmd := &cobra.Command{
		Use:   "echo <topic>",
		Short: "Stream deserialized messages from a topic (ctrl-c to stop)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			stream, err := client.client.EchoTopic(ctx, &agentpbv2.EchoROS2TopicRequest{
				DomainId: ros2DomainPtr(domain),
				Topic:    args[0],
				Count:    count,
			})
			if err != nil {
				return ros2RPCError(err)
			}
			for {
				msg, rerr := stream.Recv()
				if rerr != nil {
					if rerr == io.EOF || ctx.Err() != nil {
						return nil
					}
					return ros2RPCError(rerr)
				}
				if jsonOutput {
					if jerr := printROS2JSON(map[string]string{"topic": msg.GetTopic(), "yaml": msg.GetYaml()}); jerr != nil {
						return jerr
					}
					continue
				}
				fmt.Print(msg.GetYaml())
				fmt.Println("---")
			}
		},
	}
	ros2DomainFlag(cmd, &domain)
	cmd.Flags().Int32Var(&count, "count", 0, "Stop after N messages (0 = until ctrl-c)")
	return cmd
}

func newROS2HzCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "hz <topic>",
		Short: "Monitor the live publish rate of a topic (ctrl-c to stop)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			stream, err := client.client.MonitorHz(ctx, &agentpbv2.MonitorROS2HzRequest{
				DomainId: ros2DomainPtr(domain),
				Topic:    args[0],
			})
			if err != nil {
				return ros2RPCError(err)
			}
			for {
				sample, rerr := stream.Recv()
				if rerr != nil {
					if rerr == io.EOF || ctx.Err() != nil {
						return nil
					}
					return ros2RPCError(rerr)
				}
				if jsonOutput {
					if jerr := printROS2JSON(map[string]any{
						"hz": sample.GetHz(), "minDelta": sample.GetMinDelta(),
						"maxDelta": sample.GetMaxDelta(), "stdDev": sample.GetStdDev(),
						"window": sample.GetWindow(),
					}); jerr != nil {
						return jerr
					}
					continue
				}
				fmt.Printf("average rate: %.3f  min: %.3fs  max: %.3fs  std dev: %.5fs  window: %d\n",
					sample.GetHz(), sample.GetMinDelta(), sample.GetMaxDelta(), sample.GetStdDev(), sample.GetWindow())
			}
		},
	}
	ros2DomainFlag(cmd, &domain)
	return cmd
}

// ── graph ───────────────────────────────────────────────────────────

func newROS2GraphCmd() *cobra.Command {
	var domain int32
	var format string
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Render the node/topic connectivity graph",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.GetGraph(cmd.Context(), &agentpbv2.GetROS2GraphRequest{DomainId: ros2DomainPtr(domain)})
			if err != nil {
				return ros2RPCError(err)
			}
			switch format {
			case "ascii", "":
				fmt.Print(renderROS2GraphASCII(resp))
			case "dot":
				fmt.Print(renderROS2GraphDOT(resp))
			default:
				return fmt.Errorf("unknown graph format %q (supported: ascii, dot)", format)
			}
			return nil
		},
	}
	ros2DomainFlag(cmd, &domain)
	cmd.Flags().StringVar(&format, "format", "ascii", "Output format: ascii or dot (Graphviz)")
	return cmd
}

// ── bag commands ────────────────────────────────────────────────────

func newROS2BagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bag",
		Short: "Record, list, and download rosbag2 recordings",
	}
	cmd.AddCommand(
		newROS2BagRecordCmd(),
		newROS2BagListCmd(),
		newROS2BagDownloadCmd(),
	)
	return cmd
}

func newROS2BagRecordCmd() *cobra.Command {
	var domain int32
	var output string
	cmd := &cobra.Command{
		Use:   "record [topics...]",
		Short: "Record topics to a bag on the device (ctrl-c to stop and finalize)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			// Use the parent context for the stream: the bidi session must
			// outlive ctrl-c so the explicit stop command and the final
			// STOPPED response can travel after the signal fires.
			stream, err := client.client.RecordBag(cmd.Context())
			if err != nil {
				return ros2RPCError(err)
			}
			err = stream.Send(&agentpbv2.RecordROS2BagRequest{
				Command: &agentpbv2.RecordROS2BagRequest_Start{
					Start: &agentpbv2.RecordROS2BagRequest_RecordStart{
						DomainId:   ros2DomainPtr(domain),
						Topics:     args,
						OutputName: output,
					},
				},
			})
			if err != nil {
				return ros2RPCError(err)
			}

			first, err := stream.Recv()
			if err != nil {
				return ros2RPCError(err)
			}
			if first.GetState() == agentpbv2.RecordROS2BagResponse_STATE_ERROR {
				return fmt.Errorf("recording failed: %s", first.GetMessage())
			}
			topicsDesc := "all topics"
			if len(args) > 0 {
				topicsDesc = strings.Join(args, ", ")
			}
			cliLogln("Recording %s to bag %q on the device. Press ctrl-c to stop.", topicsDesc, first.GetBagName())
			if m := first.GetMessage(); m != "" {
				cliLogln("Note: %s", m)
			}

			// Wait for ctrl-c or an unsolicited terminal message (recorder error).
			recvCh := make(chan *agentpbv2.RecordROS2BagResponse, 1)
			recvErr := make(chan error, 1)
			go func() {
				msg, rerr := stream.Recv()
				if rerr != nil {
					recvErr <- rerr
					return
				}
				recvCh <- msg
			}()

			select {
			case <-ctx.Done():
				if serr := stream.Send(&agentpbv2.RecordROS2BagRequest{
					Command: &agentpbv2.RecordROS2BagRequest_Stop{Stop: &agentpbv2.RecordROS2BagRequest_RecordStop{}},
				}); serr != nil {
					return ros2RPCError(serr)
				}
				_ = stream.CloseSend()
				select {
				case msg := <-recvCh:
					if msg.GetState() == agentpbv2.RecordROS2BagResponse_STATE_ERROR {
						return fmt.Errorf("recording failed: %s", msg.GetMessage())
					}
					cliSuccess("Recording stopped. Download it with: wendy device ros2 bag download %s", msg.GetBagName())
				case rerr := <-recvErr:
					if rerr != io.EOF {
						return ros2RPCError(rerr)
					}
				}
				return nil
			case msg := <-recvCh:
				if msg.GetState() == agentpbv2.RecordROS2BagResponse_STATE_ERROR {
					return fmt.Errorf("recording failed: %s", msg.GetMessage())
				}
				cliSuccess("Recording finished: %s", msg.GetBagName())
				return nil
			case rerr := <-recvErr:
				return ros2RPCError(rerr)
			}
		},
	}
	ros2DomainFlag(cmd, &domain)
	cmd.Flags().StringVar(&output, "output", "", "Bag name on the device (default: auto-named with timestamp)")
	return cmd
}

func newROS2BagListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bags recorded on the device",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			resp, err := client.client.ListBags(cmd.Context(), &agentpbv2.ListROS2BagsRequest{})
			if err != nil {
				return ros2RPCError(err)
			}
			if jsonOutput {
				type bag struct {
					Name            string  `json:"name"`
					SizeBytes       int64   `json:"sizeBytes"`
					CreatedUnix     int64   `json:"createdUnix"`
					DurationSeconds float64 `json:"durationSeconds"`
				}
				bags := make([]bag, 0, len(resp.GetBags()))
				for _, b := range resp.GetBags() {
					bags = append(bags, bag{Name: b.GetName(), SizeBytes: b.GetSizeBytes(), CreatedUnix: b.GetCreatedUnix(), DurationSeconds: b.GetDurationSeconds()})
				}
				return printROS2JSON(bags)
			}
			if len(resp.GetBags()) == 0 {
				cliLogln("No bags recorded on the device.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSIZE\tDURATION")
			for _, b := range resp.GetBags() {
				duration := "-"
				if b.GetDurationSeconds() > 0 {
					duration = fmt.Sprintf("%.1fs", b.GetDurationSeconds())
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", b.GetName(), formatBytes(b.GetSizeBytes()), duration)
			}
			return w.Flush()
		},
	}
}

func newROS2BagDownloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "download [name] [dest]",
		Short: "Download a bag from the device to the local machine",
		Long: `Download a bag from the device to the local machine.

With no arguments, lists the bags on the device and lets you pick one
interactively.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !isInteractiveTerminal() {
				return errors.New("missing bag name (interactive selection needs a terminal); run `wendy device ros2 bag list`, then `wendy device ros2 bag download <name>`")
			}

			dest := "."
			if len(args) == 2 {
				dest = args[1]
			}

			client, err := newROS2Client(cmd.Context())
			if err != nil {
				return err
			}
			defer client.Close()

			name := ""
			if len(args) > 0 {
				name = args[0]
			} else {
				name, err = pickROS2Bag(cmd.Context(), client)
				if err != nil {
					return err
				}
			}

			stream, err := client.client.DownloadBag(cmd.Context(), &agentpbv2.DownloadROS2BagRequest{Name: name})
			if err != nil {
				return ros2RPCError(err)
			}

			pr, pw := io.Pipe()
			go func() {
				for {
					chunk, rerr := stream.Recv()
					if rerr == io.EOF {
						pw.Close()
						return
					}
					if rerr != nil {
						pw.CloseWithError(ros2RPCError(rerr))
						return
					}
					if _, werr := pw.Write(chunk.GetData()); werr != nil {
						pw.CloseWithError(werr)
						return
					}
				}
			}()

			if err := extractROS2BagArchive(pr, dest); err != nil {
				return err
			}
			cliSuccess("Downloaded bag %q to %s", name, filepath.Join(dest, name))
			return nil
		},
	}
}

// pickROS2Bag lists the bags recorded on the device and asks the user to
// select one interactively, returning its name.
func pickROS2Bag(ctx context.Context, client *ros2Client) (string, error) {
	resp, err := client.client.ListBags(ctx, &agentpbv2.ListROS2BagsRequest{})
	if err != nil {
		return "", ros2RPCError(err)
	}
	bags := resp.GetBags()
	if len(bags) == 0 {
		return "", errors.New("no bags recorded on the device; record one with `wendy device ros2 bag record`")
	}

	items := make([]tui.PickerItem, 0, len(bags))
	for _, b := range bags {
		duration := "-"
		if b.GetDurationSeconds() > 0 {
			duration = fmt.Sprintf("%.1fs", b.GetDurationSeconds())
		}
		created := "-"
		if b.GetCreatedUnix() > 0 {
			created = time.Unix(b.GetCreatedUnix(), 0).Format("2006-01-02 15:04:05")
		}
		items = append(items, tui.PickerItem{
			Name:       b.GetName(),
			Size:       formatBytes(b.GetSizeBytes()),
			Parameters: duration,
			Comments:   created,
			// Newest bag first: invert the creation time so the picker's
			// ascending SortKey order shows the most recent recording on top.
			SortKey: fmt.Sprintf("%020d", int64(1)<<62-b.GetCreatedUnix()),
			Value:   b.GetName(),
		})
	}
	return pickFromItemsWithColumns("Select a bag to download", items, []tui.PickerColumn{
		{Title: "Name", MinWidth: 18, Required: true, Value: func(i tui.PickerItem) string { return i.Name }},
		{Title: "Size", MinWidth: 8, Value: func(i tui.PickerItem) string { return i.Size }},
		{Title: "Duration", MinWidth: 10, Value: func(i tui.PickerItem) string { return i.Parameters }},
		{Title: "Created", MinWidth: 12, Value: func(i tui.PickerItem) string { return i.Comments }},
	})
}

// extractROS2BagArchive unpacks the tar stream produced by DownloadBag into
// dest, rejecting entries that would escape it (SOC2-CC6, NIST-SI-10).
func extractROS2BagArchive(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading bag archive: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("bag archive contains unsafe path %q", hdr.Name)
		}
		path := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				if closeErr := f.Close(); closeErr != nil {
					return errors.Join(err, closeErr)
				}
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Skip symlinks and special files; bags only contain regular files.
		}
	}
}

// ── escape hatch ────────────────────────────────────────────────────

func newROS2ExecCmd() *cobra.Command {
	var domain int32
	cmd := &cobra.Command{
		Use:   "exec [args...]",
		Short: "Run a raw ros2 CLI command on the device",
		Long: `Run a raw ros2 CLI command inside the device's ROS 2 sidecar.

Everything after the ros2 command word is forwarded verbatim, so --flags
work without escaping. Any wendy flags (--device, --domain) must come before
the ros2 command; use -- to force the rest of the line through unparsed.`,
		Example: `  wendy device ros2 exec topic echo /chatter --once
  wendy device ros2 exec topic pub /cmd_vel geometry_msgs/msg/Twist --rate 10
  wendy device ros2 exec --domain 5 node list`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			stream, err := client.client.Exec(ctx, &agentpbv2.ROS2ExecRequest{
				DomainId: ros2DomainPtr(domain),
				Args:     args,
			})
			if err != nil {
				return ros2RPCError(err)
			}
			for {
				msg, rerr := stream.Recv()
				if rerr != nil {
					if rerr == io.EOF || ctx.Err() != nil {
						return nil
					}
					return ros2RPCError(rerr)
				}
				if len(msg.GetStdout()) > 0 {
					os.Stdout.Write(msg.GetStdout())
				}
				if len(msg.GetStderr()) > 0 {
					os.Stderr.Write(msg.GetStderr())
				}
				if msg.ExitCode != nil && *msg.ExitCode != 0 {
					return fmt.Errorf("ros2 %s exited with code %d", strings.Join(args, " "), *msg.ExitCode)
				}
			}
		},
	}
	ros2DomainFlag(cmd, &domain)
	// Stop flag parsing at the first positional (the ros2 command word) so that
	// --flags meant for ros2 (e.g. `topic echo /chatter --once`) forward verbatim
	// instead of being rejected as unknown flags of this command (WDY-1553).
	cmd.Flags().SetInterspersed(false)
	return cmd
}
