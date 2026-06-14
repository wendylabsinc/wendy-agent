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
