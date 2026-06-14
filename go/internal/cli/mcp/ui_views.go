package mcp

import (
	"context"
	"fmt"
	"io"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// containerInfo is the per-container row the Dashboard and Controls views render.
// The UI reads name/version/running/hasUi (and tolerates absent cpu/mem).
type containerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Running bool   `json:"running"`
	HasUI   bool   `json:"hasUi"`
}

// dashboardData assembles the structuredContent.data payload for the Dashboard
// view. containers/stats may be nil; the iframe degrades gracefully.
func dashboardData(device, connType string, stats map[string]any, containers any) map[string]any {
	return map[string]any{
		"device":          device,
		"connection_type": connType,
		"stats":           stats,
		"containers":      containers,
	}
}

// controlsData assembles the Controls view payload. containers may be nil.
func controlsData(device string, containers any) map[string]any {
	return map[string]any{
		"device":     device,
		"containers": containers,
	}
}

// gatherContainers lists the device's containers for the Dashboard/Controls
// views. UI capability comes from the cache (populated lazily by apps_list /
// app_open), so this stays cheap and does not itself proxy into containers.
func (s *mcpServer) gatherContainers(ctx context.Context, conn *grpcclient.AgentConnection) []containerInfo {
	if conn == nil || conn.ContainerService == nil {
		return nil
	}
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil
	}
	var out []containerInfo
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		out = append(out, containerInfo{
			Name:    c.GetAppName(),
			Version: c.GetAppVersion(),
			Running: c.GetRunningState() == agentpb.AppRunningState_RUNNING,
			HasUI:   s.getAppHasUI(c.GetAppName()),
		})
	}
	return out
}

// deviceStats gathers the cheap device metrics the Dashboard tiles render today
// (disk usage). Live CPU/GPU/memory/temp come from telemetry and are left unset.
func (s *mcpServer) deviceStats(ctx context.Context, conn *grpcclient.AgentConnection) map[string]any {
	if conn == nil || conn.AgentService == nil {
		return nil
	}
	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return nil
	}
	stats := map[string]any{}
	if resp.DiskUsedBytes != nil && resp.DiskTotalBytes != nil {
		const gib = 1024 * 1024 * 1024
		used := float64(resp.GetDiskUsedBytes())
		total := float64(resp.GetDiskTotalBytes())
		stats["disk"] = fmt.Sprintf("%.1f / %.1f GB", used/gib, total/gib)
		if total > 0 {
			stats["disk_pct"] = used / total * 100
		}
	}
	return stats
}
