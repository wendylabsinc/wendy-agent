package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/hardwarediag"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func (s *mcpServer) registerHardwareTools(srv *server.MCPServer) {
	capsOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("List hardware capabilities (GPUs, cameras, I2C buses, USB devices, etc.) on the connected device. USB entries include vendor_id/product_id/serial/port_path properties; the 'power' category lists voltage/current sensor chips."),
		mcpgo.WithString("category",
			mcpgo.Description("Filter by category, e.g. gpu, usb, i2c, gpio, camera, power (optional)"),
		),
	}
	capsOpts = append(capsOpts, readOnly()...)
	capsOpts = append(capsOpts, localOnly()...)
	srv.AddTool(mcpgo.NewTool("hardware_capabilities", capsOpts...), s.handleHardwareCapabilities)

	eventsOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("Peripheral hotplug timeline for the connected device: USB connect/disconnect events, re-enumeration storms, and watched-device missing/restored alerts, newest last. Use this to answer 'did a device drop off the bus, and when?'"),
		mcpgo.WithNumber("tail",
			mcpgo.Description("Replay up to the last N buffered event batches (default 50)"),
		),
	}
	eventsOpts = append(eventsOpts, readOnly()...)
	eventsOpts = append(eventsOpts, localOnly()...)
	srv.AddTool(mcpgo.NewTool("hardware_events", eventsOpts...), s.handleHardwareEvents)

	watchOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("Read or edit the device's USB watch list. Watched devices produce a watched_missing ERROR event (see hardware_events) when absent >30s and watched_restored when back. With no arguments, returns the current list. Specs are vid:pid[:serial]; include the serial (from hardware_capabilities) to pin one of several identical devices."),
		mcpgo.WithString("add",
			mcpgo.Description("Comma-separated watch specs to add, e.g. \"1d50:606f:SERIAL,16d0:117e\" (optional)"),
		),
		mcpgo.WithString("remove",
			mcpgo.Description("Comma-separated watch specs to remove (optional)"),
		),
		mcpgo.WithBoolean("clear",
			mcpgo.Description("Remove all watches (optional)"),
		),
	}
	watchOpts = append(watchOpts, mutating()...)
	watchOpts = append(watchOpts, idempotent()...)
	watchOpts = append(watchOpts, localOnly()...)
	srv.AddTool(mcpgo.NewTool("hardware_watch", watchOpts...), s.handleHardwareWatch)

	diagOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("Diagnose USB instability on the connected device and name the likely culprit in plain language: one device/cable (repeated drops of one port), a hub (clustered drops, power budget over the port's 500/900mA, USB2 bandwidth contention), or the board itself (drops across buses, sagging supply rail). Start here when peripherals are flaky."),
		mcpgo.WithNumber("tail",
			mcpgo.Description("Buffered event batches to replay into the analysis (default 200)"),
		),
	}
	diagOpts = append(diagOpts, readOnly()...)
	diagOpts = append(diagOpts, localOnly()...)
	srv.AddTool(mcpgo.NewTool("hardware_diagnose", diagOpts...), s.handleHardwareDiagnose)
}

func (s *mcpServer) handleHardwareDiagnose(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	devices, events, volts, err := hardwarediag.Collect(ctx, conn, int32(intParam(req, "tail", 200)))
	if err != nil {
		return errResult(codeFromGRPC(err), grpcErrString(err)), nil
	}
	findings := hardwarediag.Diagnose(devices, events, volts)
	return okResult(map[string]any{
		"devices_on_bus":  len(devices),
		"events_replayed": len(events),
		"findings":        findings,
	}), nil
}

func (s *mcpServer) handleHardwareCapabilities(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	hwReq := &agentpb.ListHardwareCapabilitiesRequest{}
	if v := stringParam(req, "category"); v != "" {
		hwReq.CategoryFilter = &v
	}
	resp, err := conn.AgentService.ListHardwareCapabilities(ctx, hwReq)
	if err != nil {
		return errResult(codeFromGRPC(err), grpcErrString(err)), nil
	}
	var caps []map[string]any
	for _, c := range resp.GetCapabilities() {
		entry := map[string]any{
			"category":    c.GetCategory(),
			"device_path": c.GetDevicePath(),
			"description": c.GetDescription(),
		}
		if props := c.GetProperties(); len(props) > 0 {
			entry["properties"] = props
		}
		caps = append(caps, entry)
	}
	if caps == nil {
		caps = []map[string]any{}
	}
	return okResult(caps), nil
}

// handleHardwareEvents replays the buffered wendy.hardware timeline and
// returns it as flat entries. The stream is history-then-live; the short
// timeout ends collection once the replay is drained.
func (s *mcpServer) handleHardwareEvents(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}

	serviceName := "wendy.hardware"
	tail := int32(intParam(req, "tail", 50))
	logsReq := &agentpb.StreamLogsRequest{ServiceName: &serviceName}
	if tail > 0 {
		logsReq.LastN = &tail
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := conn.TelemetryService.StreamLogs(ctx, logsReq)
	if err != nil {
		return errResult(codeFromGRPC(err), grpcErrString(err)), nil
	}

	events := []map[string]any{}
	for int32(len(events)) < tail {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				break // replay drained; don't wait for live events
			}
			return errResult(codeFromGRPC(err), grpcErrString(err)), nil
		}
		for _, rl := range resp.GetLogs().GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				for _, lr := range sl.GetLogRecords() {
					events = append(events, flattenHardwareEvent(lr))
				}
			}
		}
	}
	return okResult(map[string]any{
		"events": events,
		"hint":   "actions: connected/disconnected (hotplug), storm (re-enumeration flood), watched_missing/watched_restored (watch list alerts)",
	}), nil
}

// flattenHardwareEvent converts an OTLP record into a compact entry: the
// wendy.hardware.* attributes are lifted to top-level keys without the prefix.
func flattenHardwareEvent(lr *otelpb.LogRecord) map[string]any {
	entry := map[string]any{
		"timestamp": time.Unix(0, int64(lr.GetTimeUnixNano())).UTC().Format(time.RFC3339),
		"severity":  lr.GetSeverityText(),
		"message":   lr.GetBody().GetStringValue(),
	}
	for _, kv := range lr.GetAttributes() {
		key := strings.TrimPrefix(kv.GetKey(), "wendy.hardware.")
		entry[key] = kv.GetValue().GetStringValue()
	}
	return entry
}

func (s *mcpServer) handleHardwareWatch(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}

	current, err := conn.DeviceInfoService.GetHardwareWatchList(ctx, &agentpbv2.GetHardwareWatchListRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return errResult("unsupported", "this agent does not support the hardware watch list — update the agent"), nil
		}
		return errResult(codeFromGRPC(err), grpcErrString(err)), nil
	}
	watches := current.GetDevices()

	adds := splitWatchSpecs(stringParam(req, "add"))
	removes := splitWatchSpecs(stringParam(req, "remove"))
	clear := boolParam(req, "clear")

	if clear || len(adds) > 0 || len(removes) > 0 {
		if clear {
			watches = nil
		}
		watches, err = applyWatchSpecEdits(watches, adds, removes)
		if err != nil {
			return errResult("invalid_argument", err.Error()), nil
		}
		if _, err := conn.DeviceInfoService.SetHardwareWatchList(ctx, &agentpbv2.SetHardwareWatchListRequest{Devices: watches}); err != nil {
			return errResult(codeFromGRPC(err), grpcErrString(err)), nil
		}
	}

	out := []map[string]any{}
	for _, w := range watches {
		entry := map[string]any{
			"vendor_id":  w.GetVendorId(),
			"product_id": w.GetProductId(),
		}
		if w.GetSerial() != "" {
			entry["serial"] = w.GetSerial()
		}
		if w.GetLabel() != "" {
			entry["label"] = w.GetLabel()
		}
		out = append(out, entry)
	}
	return okResult(map[string]any{"watched_devices": out}), nil
}

// splitWatchSpecs splits a comma-separated spec list, dropping empties.
func splitWatchSpecs(s string) []string {
	if s == "" {
		return nil
	}
	var specs []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			specs = append(specs, p)
		}
	}
	return specs
}

// applyWatchSpecEdits applies "vid:pid[:serial]" add/remove specs to the
// list, deduplicating on vendor+product+serial. (Mirrors the CLI's
// applyWatchEdits in internal/cli/commands; kept separate because the mcp
// package cannot import commands.)
func applyWatchSpecEdits(current []*agentpbv2.WatchedUSBDevice, adds, removes []string) ([]*agentpbv2.WatchedUSBDevice, error) {
	parse := func(spec string) (*agentpbv2.WatchedUSBDevice, error) {
		parts := strings.SplitN(spec, ":", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
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

	out := []*agentpbv2.WatchedUSBDevice{}
	for _, k := range order {
		if w, ok := byKey[k]; ok {
			out = append(out, w)
		}
	}
	return out, nil
}
