package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func newDevicePowerModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "power-mode",
		Short: "Show the NVIDIA Jetson power mode and the modes the device offers",
		Long: "Shows the power mode (nvpmodel) a Jetson device is in and the modes it offers. " +
			"Modes are the device's own names and IDs (for example MAXN or MODE_30W); use " +
			"'wendy device power-mode set' to switch. Devices that are not a Jetson report " +
			"that power modes are not supported.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			return runPowerModeShow(ctx, conn.AgentService, os.Stdout, jsonOutput)
		},
	}
	cmd.AddCommand(newDevicePowerModeSetCmd())
	return cmd
}

func newDevicePowerModeSetCmd() *cobra.Command {
	var reboot bool
	cmd := &cobra.Command{
		Use:   "set <name|id>",
		Short: "Switch the NVIDIA Jetson power mode",
		Long: "Switches a Jetson device to the named or numbered power mode, as listed by " +
			"'wendy device power-mode'. Some modes only take effect after a reboot. The device " +
			"is never rebooted unless you pass --reboot; without it the command reports that a " +
			"reboot is needed and leaves the choice to you.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			return runPowerModeSet(ctx, conn.AgentService, args[0], reboot, os.Stdout, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&reboot, "reboot", false, "Reboot the device afterwards if the new mode needs it")
	return cmd
}

// powerModeRPCError turns an agent error into one a person can act on.
func powerModeRPCError(action string, err error) error {
	switch status.Code(err) {
	case codes.Unimplemented:
		return fmt.Errorf("%s: this device's agent does not support power modes; update the agent first", action)
	case codes.Unavailable, codes.InvalidArgument:
		return fmt.Errorf("%s: %s", action, status.Convert(err).Message())
	}
	return fmt.Errorf("%s: %w", action, err)
}

func powerModeLabel(m *agentpb.PowerMode) string {
	return fmt.Sprintf("%s (%d)", m.GetName(), m.GetId())
}

func powerModeJSON(m *agentpb.PowerMode) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{"id": m.GetId(), "name": m.GetName()}
}

func writeJSON(out io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func runPowerModeShow(ctx context.Context, client agentpb.WendyAgentServiceClient, out io.Writer, asJSON bool) error {
	resp, err := client.GetPowerMode(ctx, &agentpb.GetPowerModeRequest{})
	if err != nil {
		return powerModeRPCError("reading the power mode", err)
	}
	current := resp.GetCurrent()
	boot := resp.GetBootDefault()

	if asJSON {
		available := make([]map[string]any, 0, len(resp.GetAvailable()))
		for _, m := range resp.GetAvailable() {
			available = append(available, powerModeJSON(m))
		}
		doc := map[string]any{"current": powerModeJSON(current), "available": available}
		if boot != nil {
			doc["bootDefault"] = powerModeJSON(boot)
		}
		return writeJSON(out, doc)
	}

	fmt.Fprintf(out, "%s %s\n\n", tui.Dim("Power mode:"), tui.Value(powerModeLabel(current)))
	fmt.Fprintln(out, tui.Dim("Available modes:"))
	for _, m := range resp.GetAvailable() {
		marker := " "
		var notes []string
		if m.GetId() == current.GetId() {
			marker = "*"
			notes = append(notes, "current")
		}
		if boot != nil && m.GetId() == boot.GetId() {
			notes = append(notes, "boot default")
		}
		line := fmt.Sprintf("  %s %d  %s", marker, m.GetId(), m.GetName())
		if len(notes) > 0 {
			line += tui.Dim(fmt.Sprintf("  (%s)", joinNotes(notes)))
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

func joinNotes(notes []string) string {
	s := notes[0]
	for _, n := range notes[1:] {
		s += ", " + n
	}
	return s
}

func runPowerModeSet(ctx context.Context, client agentpb.WendyAgentServiceClient, mode string, reboot bool, out io.Writer, asJSON bool) error {
	resp, err := client.SetPowerMode(ctx, &agentpb.SetPowerModeRequest{Mode: mode, Reboot: reboot})
	if err != nil {
		return powerModeRPCError("setting the power mode", err)
	}
	label := powerModeLabel(resp.GetMode())

	if asJSON {
		return writeJSON(out, map[string]any{
			"mode":            powerModeJSON(resp.GetMode()),
			"rebootRequired":  resp.GetRebootRequired(),
			"rebootScheduled": resp.GetRebootScheduled(),
		})
	}

	switch {
	case resp.GetRebootScheduled():
		fmt.Fprintf(out, "Power mode %s needs a reboot; rebooting the device now.\n", tui.Value(label))
	case resp.GetRebootRequired():
		fmt.Fprintf(out, "Power mode %s needs a reboot to take full effect. The device was not rebooted.\n", tui.Value(label))
		fmt.Fprintln(out, "Reboot it yourself, or run this command again with --reboot.")
	default:
		fmt.Fprintf(out, "Power mode set to %s.\n", tui.Value(label))
	}
	return nil
}
