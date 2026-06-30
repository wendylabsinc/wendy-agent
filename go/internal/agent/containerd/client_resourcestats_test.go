package containerd

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
)

func TestCPUUsageNanos(t *testing.T) {
	got := cpuUsageNanos(services.ContainerMetrics{UserCPUNanos: 1000, SysCPUNanos: 250})
	if got != 1250 {
		t.Errorf("cpuUsageNanos = %d, want 1250", got)
	}
	// Negative values (shouldn't happen, but guard) clamp to 0.
	got = cpuUsageNanos(services.ContainerMetrics{UserCPUNanos: -5, SysCPUNanos: -5})
	if got != 0 {
		t.Errorf("cpuUsageNanos = %d, want 0", got)
	}
}
