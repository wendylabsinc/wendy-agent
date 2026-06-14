package mcp

// dashboardData assembles the structuredContent.data payload the iframe renders
// for the Dashboard view.
func dashboardData(device, connType string, stats map[string]any) map[string]any {
	return map[string]any{
		"device":          device,
		"connection_type": connType,
		"stats":           stats,
	}
}

// containerState is the per-container row the Controls view renders.
type containerState struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	CPU     string `json:"cpu,omitempty"`
}

// controlsData assembles the Controls view payload.
func controlsData(device string, containers []containerState) map[string]any {
	return map[string]any{
		"device":     device,
		"containers": containers,
	}
}
