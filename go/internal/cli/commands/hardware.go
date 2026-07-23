package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/hardwarediag"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func newHardwareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hardware",
		Short: "Query hardware capabilities on the target device",
	}

	cmd.AddCommand(newHardwareListCmd())
	cmd.AddCommand(newHardwareEventsCmd())
	cmd.AddCommand(newHardwareWatchCmd())
	cmd.AddCommand(newHardwareDiagnoseCmd())
	return cmd
}

// newHardwareDiagnoseCmd runs the peripheral-health heuristics and prints
// human-language findings: whether USB instability points at one device/cable,
// a hub (power budget, bandwidth, clustered drops), or the board itself
// (multi-bus drops, sagging supply rail).
func newHardwareDiagnoseCmd() *cobra.Command {
	var tail int32

	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Analyze USB stability and name the likely culprit (device, cable, hub, or board)",
		Long: `Collects the USB topology (with declared power draw and link speeds), the
buffered hotplug event history, and recent supply-rail voltages, then runs
diagnosis heuristics and reports findings in plain language — e.g. "multiple
devices dropping behind hub 1-2: suspect the hub, its cable, or its power",
or "devices behind hub 1-2 declare 1200mA, above the 500mA the port supplies".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			devices, events, volts, err := hardwarediag.Collect(ctx, conn, tail)
			if err != nil {
				return fmt.Errorf("collecting hardware state: %w", err)
			}
			findings := hardwarediag.Diagnose(devices, events, volts)

			if jsonOutput {
				data, err := json.MarshalIndent(map[string]any{
					"devices_on_bus":  len(devices),
					"events_replayed": len(events),
					"findings":        findings,
				}, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("Inspected %d USB devices, %d buffered events, %d voltage sensors.\n\n", len(devices), len(events), len(volts))
			for i, f := range findings {
				fmt.Printf("%d. [%s] %s\n", i+1, strings.ToUpper(f.Severity), f.Title)
				fmt.Printf("   %s\n", f.Evidence)
				if f.Suggestion != "" {
					fmt.Printf("   → %s\n", f.Suggestion)
				}
				fmt.Println()
			}
			return nil
		},
	}

	cmd.Flags().Int32Var(&tail, "tail", 200, "How many buffered event batches to replay into the analysis")
	return cmd
}

// newHardwareWatchCmd manages the device-local USB watch list. Interactive
// runs open a checklist seeded from the devices currently on the bus (plus
// watched-but-absent entries); the selection replaces the device's list. The
// agent alerts (watched_missing ERROR / watched_restored INFO in the
// wendy.hardware stream) when a watched device is absent past a grace period.
// Nothing is added to wendy.json — the list lives on the device.
func newHardwareWatchCmd() *cobra.Command {
	var addFlags, removeFlags []string
	var clear bool

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Choose USB devices the device should alert on when missing",
		Long: `Manage the device's hardware watch list. Without flags, opens an interactive
picker listing the USB devices currently on the bus — check the ones that must
stay present. The agent then publishes a watched_missing (ERROR) event when a
watched device is gone for more than ~30s, and watched_restored (INFO) when it
returns; both appear in 'wendy device hardware events' and in cloud telemetry.

Devices with a serial number are pinned to that exact unit, so two identical
adapters are tracked individually.

Non-interactive usage:
  --add vid:pid[:serial]     add a watch (repeatable)
  --remove vid:pid[:serial]  remove a watch (repeatable)
  --clear                    remove all watches
With no flags and no TTY, prints the current watch list.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			snapshot, err := fetchHardwareSnapshot(ctx, conn)
			if err != nil {
				if status.Code(err) == codes.Unimplemented {
					return fmt.Errorf("this agent does not support the hardware watch list — update the agent (wendy device update)")
				}
				return fmt.Errorf("getting hardware state: %w", err)
			}
			watches := snapshot.GetWatchList()

			switch {
			case clear:
				if _, err := conn.DeviceInfoService.SetHardwareWatchList(ctx, &agentpbv2.SetHardwareWatchListRequest{}); err != nil {
					return fmt.Errorf("clearing hardware watch list: %w", err)
				}
				cliLogln("Hardware watch list cleared.")
				return nil

			case len(addFlags) > 0 || len(removeFlags) > 0:
				updated, err := applyWatchEdits(watches, addFlags, removeFlags)
				if err != nil {
					return err
				}
				if _, err := conn.DeviceInfoService.SetHardwareWatchList(ctx, &agentpbv2.SetHardwareWatchListRequest{Devices: updated}); err != nil {
					return fmt.Errorf("updating hardware watch list: %w", err)
				}
				return printWatchList(updated)

			case jsonOutput || !term.IsTerminal(int(os.Stdin.Fd())):
				return printWatchList(watches)
			}

			// Interactive picker seeded from the snapshot's live bus view.
			items := buildWatchChecklist(snapshot.GetUsbDevices(), watches)
			if len(items) == 0 {
				fmt.Println("No USB devices found to watch.")
				return nil
			}
			selected, err := tui.RunChecklist("Select USB devices the device must alert on when missing:", items)
			if err != nil {
				if errors.Is(err, tui.ErrCancelled) {
					cliLogln("Cancelled; watch list unchanged.")
					return nil
				}
				return err
			}

			var chosen []*agentpbv2.WatchedUSBDevice
			for _, item := range selected {
				var w agentpbv2.WatchedUSBDevice
				if err := json.Unmarshal([]byte(item.Value), &w); err != nil {
					return fmt.Errorf("internal: decoding selection: %w", err)
				}
				chosen = append(chosen, &w)
			}
			if _, err := conn.DeviceInfoService.SetHardwareWatchList(ctx, &agentpbv2.SetHardwareWatchListRequest{Devices: chosen}); err != nil {
				return fmt.Errorf("updating hardware watch list: %w", err)
			}
			return printWatchList(chosen)
		},
	}

	cmd.Flags().StringArrayVar(&addFlags, "add", nil, "Add a watch as vid:pid[:serial] (repeatable)")
	cmd.Flags().StringArrayVar(&removeFlags, "remove", nil, "Remove a watch as vid:pid[:serial] (repeatable)")
	cmd.Flags().BoolVar(&clear, "clear", false, "Remove all watches")
	return cmd
}

// fetchHardwareSnapshot opens a WatchHardware stream, reads the leading
// snapshot (current USB devices + watch list), and closes the stream — the
// snapshot-only read pattern the RPC contract supports.
func fetchHardwareSnapshot(ctx context.Context, conn *grpcclient.AgentConnection) (*agentpbv2.HardwareSnapshot, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := conn.DeviceInfoService.WatchHardware(sctx, &agentpbv2.WatchHardwareRequest{})
	if err != nil {
		return nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	snapshot := resp.GetSnapshot()
	if snapshot == nil {
		return nil, fmt.Errorf("agent sent no hardware snapshot")
	}
	return snapshot, nil
}

// buildWatchChecklist merges the live USB devices with watched-but-absent
// entries into checklist items. Kernel root hubs (vendor 1d6b) are hidden:
// they are the host controller itself and always present. Item values carry
// the WatchedUSBDevice JSON so the selection round-trips losslessly.
func buildWatchChecklist(live []*agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability, watches []*agentpbv2.WatchedUSBDevice) []tui.ChecklistItem {
	matched := make([]bool, len(watches))
	var items []tui.ChecklistItem

	for _, c := range live {
		props := c.GetProperties()
		vendor, product := props["vendor_id"], props["product_id"]
		if vendor == "" || product == "" || vendor == "1d6b" {
			continue
		}
		serial := props["serial"]

		label := c.GetDescription()
		if idx := strings.LastIndex(label, " ("); idx > 0 {
			label = label[:idx]
		}

		w := &agentpbv2.WatchedUSBDevice{VendorId: vendor, ProductId: product}
		if serial != "" {
			w.Serial = &serial
		}
		if label != "" {
			w.Label = &label
		}
		value, _ := json.Marshal(w)

		desc := vendor + ":" + product
		if port := props["port_path"]; port != "" {
			desc += "  port " + port
		}
		if serial != "" {
			desc += "  serial " + serial
		}

		selected := false
		for i, existing := range watches {
			if watchCoversDevice(existing, vendor, product, serial) {
				matched[i] = true
				selected = true
			}
		}

		items = append(items, tui.ChecklistItem{
			Label:       label,
			Description: desc,
			Value:       string(value),
			Selected:    selected,
		})
	}

	// Watched entries with no matching live device: keep them visible (and
	// checked) so an already-missing device isn't silently dropped from the
	// list just by re-running the picker.
	for i, w := range watches {
		if matched[i] {
			continue
		}
		label := w.GetLabel()
		if label == "" {
			label = w.GetVendorId() + ":" + w.GetProductId()
		}
		desc := w.GetVendorId() + ":" + w.GetProductId()
		if w.GetSerial() != "" {
			desc += "  serial " + w.GetSerial()
		}
		desc += "  (not currently connected)"
		value, _ := json.Marshal(w)
		items = append(items, tui.ChecklistItem{
			Label:       label,
			Description: desc,
			Value:       string(value),
			Selected:    true,
		})
	}

	return items
}

// watchCoversDevice reports whether an existing watch entry corresponds to the
// live device: serial-pinned watches require the exact serial, loose watches
// match any device with the same vendor:product id.
func watchCoversDevice(w *agentpbv2.WatchedUSBDevice, vendor, product, serial string) bool {
	if !strings.EqualFold(w.GetVendorId(), vendor) || !strings.EqualFold(w.GetProductId(), product) {
		return false
	}
	return w.GetSerial() == "" || w.GetSerial() == serial
}

// applyWatchEdits applies --add/--remove specs ("vid:pid[:serial]") to the
// current list, deduplicating on vendor+product+serial.
func applyWatchEdits(current []*agentpbv2.WatchedUSBDevice, adds, removes []string) ([]*agentpbv2.WatchedUSBDevice, error) {
	parse := func(spec string) (*agentpbv2.WatchedUSBDevice, error) {
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid watch spec %q: expected vid:pid[:serial]", spec)
		}
		w := &agentpbv2.WatchedUSBDevice{
			VendorId:  strings.ToLower(parts[0]),
			ProductId: strings.ToLower(parts[1]),
		}
		if len(parts) == 3 && parts[2] != "" {
			w.Serial = &parts[2]
		}
		return w, nil
	}
	key := func(w *agentpbv2.WatchedUSBDevice) string {
		return strings.ToLower(w.GetVendorId()) + ":" + strings.ToLower(w.GetProductId()) + ":" + w.GetSerial()
	}

	byKey := make(map[string]*agentpbv2.WatchedUSBDevice)
	var order []string
	for _, w := range current {
		k := key(w)
		if _, ok := byKey[k]; !ok {
			byKey[k] = w
			order = append(order, k)
		}
	}
	for _, spec := range adds {
		w, err := parse(spec)
		if err != nil {
			return nil, err
		}
		k := key(w)
		if _, ok := byKey[k]; !ok {
			byKey[k] = w
			order = append(order, k)
		}
	}
	for _, spec := range removes {
		w, err := parse(spec)
		if err != nil {
			return nil, err
		}
		delete(byKey, key(w))
	}

	var out []*agentpbv2.WatchedUSBDevice
	for _, k := range order {
		if w, ok := byKey[k]; ok {
			out = append(out, w)
		}
	}
	return out, nil
}

func printWatchList(watches []*agentpbv2.WatchedUSBDevice) error {
	if jsonOutput {
		data, err := json.MarshalIndent(watches, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(watches) == 0 {
		fmt.Println("No devices watched. Run 'wendy device hardware watch' to pick some.")
		return nil
	}
	headers := []string{"Device", "Vendor:Product", "Serial"}
	var rows [][]string
	for _, w := range watches {
		serial := w.GetSerial()
		if serial == "" {
			serial = "(any)"
		}
		rows = append(rows, []string{w.GetLabel(), w.GetVendorId() + ":" + w.GetProductId(), serial})
	}
	fmt.Print(tui.RenderTable(headers, rows))
	return nil
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

			if !jsonOutput {
				if tail > 0 {
					cliLogln("Streaming hardware events — replaying up to %d recent, then live. Press Ctrl-C to stop.", tail)
				} else {
					cliLogln("Streaming hardware events. Waiting for new events — press Ctrl-C to stop.")
				}
			}

			// Phase 1: replay buffered history from the telemetry stream. The
			// stream turns live after the replay; the first live batch (or a
			// short timeout when nothing is live) ends the phase — real-time
			// delivery is WatchHardware's job.
			if tail > 0 {
				replayHardwareEventHistory(ctx, conn, tail)
			}

			// Phase 2: real-time events pushed by the agent.
			stream, err := conn.DeviceInfoService.WatchHardware(ctx, &agentpbv2.WatchHardwareRequest{})
			if err != nil {
				return fmt.Errorf("starting hardware watch stream: %w", err)
			}
			first, err := stream.Recv()
			if err != nil {
				return fmt.Errorf("receiving hardware snapshot: %w", err)
			}
			if !jsonOutput {
				fmt.Println(logMetaStyle.Render(fmt.Sprintf("── live (%d usb devices on the bus) ──", len(first.GetSnapshot().GetUsbDevices()))))
			}
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return fmt.Errorf("receiving hardware events: %w", err)
				}
				if ev := resp.GetEvent(); ev != nil {
					if jsonOutput {
						printHardwareEventProtoJSON(ev)
					} else {
						printHardwareEventProto(ev)
					}
				}
			}
		},
	}

	cmd.Flags().Int32Var(&tail, "tail", 50, "Replay up to the last N buffered event batches before streaming live (0 = live only)")
	return cmd
}

// replayHardwareEventHistory prints buffered wendy.hardware records from the
// telemetry stream. Best-effort: the phase ends at the first live batch or
// after a short drain timeout; errors just mean no history is shown.
func replayHardwareEventHistory(ctx context.Context, conn *grpcclient.AgentConnection, tail int32) {
	hctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	serviceName := "wendy.hardware"
	req := &agentpb.StreamLogsRequest{ServiceName: &serviceName, LastN: &tail}
	stream, err := conn.TelemetryService.StreamLogs(hctx, req)
	if err != nil {
		return
	}
	for {
		resp, err := stream.Recv()
		if err != nil || !resp.GetIsHistory() {
			return
		}
		for _, rl := range resp.GetLogs().GetResourceLogs() {
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
}

// hardwareActionSeverity maps a WatchHardware event action to the OTel
// severity its telemetry twin carries, so both printers style consistently.
func hardwareActionSeverity(action string) (otelpb.SeverityNumber, string) {
	switch action {
	case "disconnected", "storm":
		return otelpb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"
	case "watched_missing":
		return otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
	default:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
	}
}

// printHardwareEventProto renders one live WatchHardware event in the same
// timeline format as the history lines.
func printHardwareEventProto(ev *agentpbv2.HardwareEvent) {
	ts := time.Unix(0, ev.GetTimeUnixNano()).Local().Format("2006-01-02 15:04:05")
	detail := ev.GetMessage()
	if _, after, found := strings.Cut(detail, ": "); found {
		detail = after
	}
	severity, _ := hardwareActionSeverity(ev.GetAction())
	_, style := severityLabel(severity)

	var b strings.Builder
	b.WriteString(logTimeStyle.Render(ts))
	b.WriteString("  ")
	b.WriteString(style.Render(fmt.Sprintf("%-16s", ev.GetAction())))
	b.WriteString("  ")
	b.WriteString(detail)
	fmt.Println(b.String())
}

func printHardwareEventProtoJSON(ev *agentpbv2.HardwareEvent) {
	_, severityText := hardwareActionSeverity(ev.GetAction())
	entry := map[string]any{
		"timestamp": time.Unix(0, ev.GetTimeUnixNano()).UTC().Format(time.RFC3339Nano),
		"severity":  severityText,
		"action":    ev.GetAction(),
		"body":      ev.GetMessage(),
	}
	attrs := map[string]string{}
	setAttr := func(k, v string) {
		if v != "" {
			attrs[k] = v
		}
	}
	setAttr("vendor_id", ev.GetVendorId())
	setAttr("product_id", ev.GetProductId())
	setAttr("serial", ev.GetSerial())
	setAttr("product", ev.GetProduct())
	setAttr("port_path", ev.GetPortPath())
	if ev.GetSuppressed() > 0 {
		attrs["suppressed"] = fmt.Sprintf("%d", ev.GetSuppressed())
	}
	if len(attrs) > 0 {
		entry["attributes"] = attrs
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
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
