package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func newHardwareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hardware",
		Short: "Query hardware capabilities on the target device",
	}

	cmd.AddCommand(newHardwareListCmd())
	cmd.AddCommand(newHardwareEventsCmd())
	return cmd
}

// newHardwareEventsCmd streams the device's peripheral connect/disconnect
// timeline. Events are published by the agent under service.name
// "wendy.hardware" (see internal/agent/services/hardware_events.go), so this
// is a service-filtered view over the same telemetry stream as `device logs`:
// buffered history first, then live events.
func newHardwareEventsCmd() *cobra.Command {
	var tail int32

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream peripheral connect/disconnect events (history, then live)",
		Long: `Stream the device's peripheral hotplug timeline: USB connect/disconnect
events recorded by the agent, replayed from the on-device telemetry buffer and
then followed live. Press Ctrl-C to stop.

Requires a wendy-agent with hardware event collection (Linux devices; enabled
by default, disabled with WENDY_HARDWARE_EVENTS=false on the device).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			serviceName := "wendy.hardware"
			req := &agentpb.StreamLogsRequest{ServiceName: &serviceName}
			if tail > 0 {
				req.LastN = &tail
			}
			stream, err := conn.TelemetryService.StreamLogs(ctx, req)
			if err != nil {
				return fmt.Errorf("starting hardware event stream: %w", err)
			}

			if !jsonOutput {
				if tail > 0 {
					cliLogln("Streaming hardware events — replaying up to %d recent, then live. Press Ctrl-C to stop.", tail)
				} else {
					cliLogln("Streaming hardware events. Waiting for new events — press Ctrl-C to stop.")
				}
			}

			liveSeparatorPrinted := tail == 0
			seenHistory := false
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fmt.Errorf("receiving hardware events: %w", err)
				}
				logs := resp.GetLogs()
				if logs == nil {
					continue
				}
				if resp.IsHistory {
					seenHistory = true
				}
				if !liveSeparatorPrinted && seenHistory && !resp.IsHistory {
					liveSeparatorPrinted = true
					if !jsonOutput {
						fmt.Println(logMetaStyle.Render("── live ──────────────────────"))
					}
				}
				for _, rl := range logs.GetResourceLogs() {
					for _, sl := range rl.GetScopeLogs() {
						for _, lr := range sl.GetLogRecords() {
							if jsonOutput {
								printLogRecordJSON("", lr)
							} else {
								printHardwareEvent(lr)
							}
						}
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().Int32Var(&tail, "tail", 50, "Replay up to the last N buffered event batches before streaming live (0 = live only)")
	return cmd
}

// printHardwareEvent renders one hotplug event as a timeline line:
//
//	2026-07-19 22:14:03  disconnected  CANable2 (16d0:117e) at 1-2.4
//
// The action word is pulled from the wendy.hardware.action attribute and the
// detail from the event body; records without the attribute (unexpected
// producers) fall back to the raw body.
func printHardwareEvent(lr *otelpb.LogRecord) {
	ts := time.Unix(0, int64(lr.GetTimeUnixNano())).Local().Format("2006-01-02 15:04:05")

	action := ""
	for _, kv := range lr.GetAttributes() {
		if kv.GetKey() == "wendy.hardware.action" {
			action = kv.GetValue().GetStringValue()
		}
	}
	body := lr.GetBody().GetStringValue()
	detail := body
	if action != "" {
		// Bodies look like "usb device disconnected: CANable2 (…) at 1-2.4";
		// the action gets its own column, so keep only the part after ": ".
		if _, after, found := strings.Cut(body, ": "); found {
			detail = after
		}
	}

	_, style := severityLabel(lr.GetSeverityNumber())
	var b strings.Builder
	b.WriteString(logTimeStyle.Render(ts))
	b.WriteString("  ")
	if action != "" {
		b.WriteString(style.Render(fmt.Sprintf("%-12s", action)))
		b.WriteString("  ")
	}
	b.WriteString(detail)
	fmt.Println(b.String())
}

func newHardwareListCmd() *cobra.Command {
	var category string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List hardware capabilities",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()

			if target.Bluetooth != nil && target.Bluetooth.IsWendyAgent() {
				cliLogln("Connecting to %s via Bluetooth...", target.Bluetooth.DisplayName)
				bleClient, bleErr := connectBLEAgent(target.Bluetooth)
				if bleErr != nil {
					return bleErr
				}
				defer bleClient.Close()
				bleCaps, bleErr := bleClient.HardwareList()
				if bleErr != nil {
					return fmt.Errorf("listing hardware capabilities: %w", bleErr)
				}
				if jsonOutput {
					data, jsonErr := json.MarshalIndent(bleCaps, "", "  ")
					if jsonErr != nil {
						return jsonErr
					}
					fmt.Println(string(data))
					return nil
				}
				if len(bleCaps) == 0 {
					fmt.Println("No hardware capabilities found.")
					return nil
				}
				headers := []string{"Type", "Name", "Available"}
				var rows [][]string
				for _, c := range bleCaps {
					avail := "no"
					if c.GetAvailable() {
						avail = "yes"
					}
					rows = append(rows, []string{c.GetType(), c.GetName(), avail})
				}
				fmt.Print(tui.RenderTable(headers, rows))
				return nil
			}

			if target.Agent == nil {
				return fmt.Errorf("selected device does not support this command")
			}

			req := &agentpb.ListHardwareCapabilitiesRequest{}
			if category != "" {
				req.CategoryFilter = &category
			}
			resp, respErr := target.Agent.AgentService.ListHardwareCapabilities(ctx, req)
			if respErr != nil {
				if macErr := macOSBetaUnsupportedFeatureError(ctx, target.Agent.AgentService, respErr, "Hardware capability discovery"); macErr != nil {
					return fmt.Errorf("listing hardware capabilities: %w", macErr)
				}
				return fmt.Errorf("listing hardware capabilities: %w", respErr)
			}
			caps := resp.GetCapabilities()

			if jsonOutput {
				data, err := json.MarshalIndent(caps, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(caps) == 0 {
				fmt.Println("No hardware capabilities found.")
				return nil
			}

			headers := []string{"Category", "Device", "Description", "Properties"}
			var rows [][]string
			for _, c := range caps {
				props := formatProperties(c.GetProperties())
				rows = append(rows, []string{
					c.GetCategory(),
					c.GetDevicePath(),
					c.GetDescription(),
					props,
				})
			}
			fmt.Print(tui.RenderTable(headers, rows))
			return nil
		},
	}

	cmd.Flags().StringVar(&category, "category", "", "Filter by category (e.g., gpu, audio, camera)")
	return cmd
}

func formatProperties(props map[string]string) string {
	if len(props) == 0 {
		return ""
	}
	var parts []string
	for k, v := range props {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ", ")
}
