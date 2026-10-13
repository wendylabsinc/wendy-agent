package mcp

import (
	"slices"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// readOnlyTools is every built-in tool marked read-only. wendy chat runs these
// without asking the user, including in turns a watch event starts by itself,
// so a tool belongs here only if calling it changes nothing on the device, in
// Cloud or on this computer. Adding a name is a review decision: read the
// tool's handler before you do.
var readOnlyTools = []string{
	"app_inspect", "bluetooth_scan", "camera_controls", "camera_list", "cloud_discover", "cloud_ping",
	"cloud_tunnel_list", "container_list", "container_stats", "device_info", "device_list", "device_os_logs",
	"hardware_capabilities", "os_install_plan", "os_install_status", "os_install_verify", "os_list_drives",
	"os_update_status", "project_validate", "provisioning_status", "ros2_lidar_summary", "ros2_topic_hz",
	"ros2_topic_info", "ros2_topic_sample", "ros2_topics", "simulator_list", "telemetry_logs", "telemetry_metrics",
	"telemetry_traces", "watch_events", "watch_list", "watch_sources", "wendy_status", "wendy_tools",
	"wifi_known_networks", "wifi_list", "wifi_status",
}

func TestReadOnlyToolsAreAnExplicitList(t *testing.T) {
	s := New(&config.Config{}, nil)
	srv, err := s.newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}
	for name, tool := range srv.ListTools() {
		hint := tool.Tool.Annotations.ReadOnlyHint
		marked := hint != nil && *hint
		listed := slices.Contains(readOnlyTools, name)
		switch {
		case marked && !listed:
			t.Errorf("%s is marked read-only but not in readOnlyTools; chat would run it without approval. Add it only after checking it changes nothing.", name)
		case listed && !marked:
			t.Errorf("%s is in readOnlyTools but not marked read-only; remove it from the list", name)
		}
	}
	for _, name := range readOnlyTools {
		if _, ok := srv.ListTools()[name]; !ok {
			t.Errorf("readOnlyTools names %s, which is not a tool", name)
		}
	}
}
