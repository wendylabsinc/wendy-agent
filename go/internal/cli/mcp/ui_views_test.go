package mcp

import (
	"encoding/json"
	"testing"
)

func TestDashboardDataShape(t *testing.T) {
	d := dashboardData("jetson-orin-01", "cloud", map[string]any{"cpu": 38})
	b, _ := json.Marshal(d)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["device"] != "jetson-orin-01" {
		t.Fatalf("device = %v", got["device"])
	}
	if got["connection_type"] != "cloud" {
		t.Fatalf("connection_type = %v", got["connection_type"])
	}
}
