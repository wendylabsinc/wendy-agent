package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// newCameraControlsCmd shows a local camera's tunable V4L2 controls. It is the
// read half of the pair whose write half is set-control: a viewer that wants to
// fix a blown-out exposure needs to see the current value and the range first.
func newCameraControlsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "controls <id>",
		Short: "Show a local camera's tunable controls (exposure, gain, white balance, ...)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCameraID(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.VideoService.GetCameraControls(ctx, &agentpb.GetCameraControlsRequest{DeviceId: id})
			if err != nil {
				return fmt.Errorf("getting camera controls: %w", err)
			}
			return renderCameraControls(resp.GetControls())
		},
	}
}

func renderCameraControls(controls []*agentpb.CameraControl) error {
	if jsonOutput {
		data, err := json.MarshalIndent(controls, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(controls) == 0 {
		fmt.Println("No tunable controls found (is this a local USB/CSI camera?).")
		return nil
	}
	headers := []string{"Control", "Value", "Min", "Max", "Default", "Settable"}
	var rows [][]string
	for _, c := range controls {
		settable := "yes"
		if !c.GetSettable() {
			settable = "no"
		}
		rows = append(rows, []string{
			c.GetName(),
			fmt.Sprintf("%d", c.GetValue()),
			fmt.Sprintf("%d", c.GetMinimum()),
			fmt.Sprintf("%d", c.GetMaximum()),
			fmt.Sprintf("%d", c.GetDefaultValue()),
			settable,
		})
	}
	fmt.Print(tui.RenderTable(headers, rows))
	return nil
}

// newCameraSetControlCmd sets one or more controls on a local camera. Values
// persist by default -- re-applied whenever the capture pipeline reopens the
// device and across agent restarts -- because the whole point is a setting that
// holds; --no-persist makes it a one-shot.
func newCameraSetControlCmd() *cobra.Command {
	var noPersist bool
	cmd := &cobra.Command{
		Use:   "set-control <id> name=value [name=value ...]",
		Short: "Set tunable controls on a local camera",
		Long: `Set tunable V4L2 controls on a local (USB/CSI) camera.

Controls are given as name=value pairs and applied in a safe order (a mode
control such as auto_exposure lands before the control it enables). Values
persist across stream reconnects and reboots unless --no-persist is given.

Example -- stop a flame or lamp from blowing out to white by forcing a short
manual exposure:

  wendy device camera set-control 0 auto_exposure=1 exposure_time_absolute=20 backlight_compensation=0

Run "wendy device camera controls <id>" first to see the available controls and
their ranges.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCameraID(args[0])
			if err != nil {
				return err
			}
			controls, err := parseControlAssignments(args[1:])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.VideoService.SetCameraControls(ctx, &agentpb.SetCameraControlsRequest{
				DeviceId: id,
				Controls: controls,
				Persist:  !noPersist,
			})
			if err != nil {
				return fmt.Errorf("setting camera controls: %w", err)
			}
			return reportControlResults(cmd.OutOrStdout(), id, resp.GetResults())
		},
	}
	cmd.Flags().BoolVar(&noPersist, "no-persist", false,
		"Apply once now; do not re-apply on stream reconnect or reboot")
	return cmd
}

// parseControlAssignments turns name=value arguments into proto controls. Values
// are integers: V4L2 controls are integer-valued (a menu like auto_exposure is
// its numeric index, e.g. 1=Manual).
func parseControlAssignments(args []string) ([]*agentpb.CameraControl, error) {
	out := make([]*agentpb.CameraControl, 0, len(args))
	for _, a := range args {
		name, val, ok := strings.Cut(a, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("expected name=value, got %q", a)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("control %q: value %q is not an integer", name, val)
		}
		out = append(out, &agentpb.CameraControl{Name: name, Value: int32(n)})
	}
	return out, nil
}

// reportControlResults prints per-control outcomes and returns a non-nil error
// (non-zero exit) if any control was not applied, naming them, so a script can
// tell a partial success from a full one.
func reportControlResults(out io.Writer, id uint32, results []*agentpb.CameraControlResult) error {
	var failed []string
	for _, r := range results {
		if r.GetApplied() {
			fmt.Fprintf(out, "camera %d: %s applied\n", id, r.GetName())
			continue
		}
		detail := r.GetDetail()
		if detail == "" {
			detail = "not applied"
		}
		fmt.Fprintf(out, "camera %d: %s NOT applied (%s)\n", id, r.GetName(), detail)
		failed = append(failed, r.GetName())
	}
	if len(failed) > 0 {
		return fmt.Errorf("camera %d: %d control(s) not applied: %s", id, len(failed), strings.Join(failed, ", "))
	}
	return nil
}
