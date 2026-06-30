package commands

import (
	"math"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestFormatThermalZones(t *testing.T) {
	zones := []*agentpb.ThermalZone{
		{Name: "gpu-thermal", TempC: 52.4},
		{Name: "cpu-thermal", TempC: 49},
		{Name: "soc0-therm", TempC: 47},
		{Name: "thermal_zone9", TempC: 40},
	}
	got := formatThermalZones(zones)
	want := "gpu 52°C  cpu 49°C  soc0 47°C  thermal_zone9 40°C"
	if got != want {
		t.Errorf("formatThermalZones = %q; want %q", got, want)
	}
	if formatThermalZones(nil) != "" {
		t.Errorf("formatThermalZones(nil) should be empty")
	}
}

func TestHostCPUPercent(t *testing.T) {
	prev := topSample{host: &agentpb.HostStats{CpuTotalJiffies: 1000, CpuIdleJiffies: 800}}
	cur := topSample{host: &agentpb.HostStats{CpuTotalJiffies: 1100, CpuIdleJiffies: 850}}
	// totalΔ=100, idleΔ=50 → busy = 1 - 50/100 = 50%
	if got := hostCPUPercent(prev, cur); !approx(got, 50) {
		t.Errorf("hostCPUPercent = %v, want 50", got)
	}
}

func TestHostCPUPercentNoDelta(t *testing.T) {
	s := topSample{host: &agentpb.HostStats{CpuTotalJiffies: 1000, CpuIdleJiffies: 800}}
	if got := hostCPUPercent(s, s); got != 0 {
		t.Errorf("hostCPUPercent = %v, want 0", got)
	}
}

func TestContainerCPUPercent(t *testing.T) {
	// 1e9 ns of CPU over 1e9 ns of wall time on a 2-core machine = 50% of machine.
	prev := topSample{containers: map[string]uint64{"a": 0}, takenAtNanos: 0}
	cur := topSample{containers: map[string]uint64{"a": 1_000_000_000}, takenAtNanos: 1_000_000_000}
	if got := containerCPUPercent(prev, cur, "a", 2); !approx(got, 50) {
		t.Errorf("containerCPUPercent = %v, want 50", got)
	}
}

func TestBuildTopRowsSortedByMemoryDesc(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{AppName: "low", RunningState: agentpb.AppRunningState_RUNNING},
		{AppName: "high", RunningState: agentpb.AppRunningState_RUNNING},
	}
	mem := map[string]int64{"low": 100, "high": 900}
	cpu := map[string]float64{"low": 1, "high": 2}
	rows := buildTopRows(containers, cpu, mem, false /*sortByCPU*/)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].name != "high" {
		t.Errorf("first row = %q, want high (mem desc)", rows[0].name)
	}
}

func TestBuildTopRowsMultiServiceGrouping(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{
			AppName:      "web",
			RunningState: agentpb.AppRunningState_RUNNING,
			Services: []*agentpb.ServiceEntry{
				{Name: "api", RunningState: agentpb.AppRunningState_RUNNING},
				{Name: "worker", RunningState: agentpb.AppRunningState_RUNNING},
			},
		},
	}
	// Per-service stats are keyed appID_serviceName.
	mem := map[string]int64{"web_api": 100, "web_worker": 200}
	cpu := map[string]float64{"web_api": 5, "web_worker": 7}
	rows := buildTopRows(containers, cpu, mem, false)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (header + 2 services)", len(rows))
	}
	if !rows[0].isGroupHeader {
		t.Errorf("row 0 should be group header")
	}
	if rows[0].memBytes != 300 {
		t.Errorf("group mem = %d, want 300", rows[0].memBytes)
	}
	if !rows[1].isSubrow || !rows[2].isSubrow {
		t.Errorf("rows 1,2 should be subrows")
	}
}

func TestBuildTopJSON(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_RUNNING},
	}
	prev := topSample{
		host:         &agentpb.HostStats{CpuTotalJiffies: 1000, CpuIdleJiffies: 900, CpuCount: 2, MemTotalBytes: 200, MemAvailableBytes: 150},
		containers:   map[string]uint64{"myapp": 0},
		mem:          map[string]int64{"myapp": 50},
		takenAtNanos: 0,
	}
	cur := topSample{
		host:         &agentpb.HostStats{CpuTotalJiffies: 1100, CpuIdleJiffies: 950, CpuCount: 2, MemTotalBytes: 200, MemAvailableBytes: 140},
		containers:   map[string]uint64{"myapp": 500_000_000},
		mem:          map[string]int64{"myapp": 60},
		takenAtNanos: 1_000_000_000,
	}
	out := buildTopJSON(prev, cur, containers)
	if out.Host.CPUPercent <= 0 {
		t.Errorf("host cpu%% = %v, want > 0", out.Host.CPUPercent)
	}
	if out.Host.MemUsedBytes != 60 { // total - available = 200-140
		t.Errorf("host memUsed = %d, want 60", out.Host.MemUsedBytes)
	}
	if len(out.Containers) != 1 || out.Containers[0].Name != "myapp" {
		t.Fatalf("containers = %+v", out.Containers)
	}
	if out.Containers[0].CPUPercent <= 0 {
		t.Errorf("container cpu%% = %v, want > 0", out.Containers[0].CPUPercent)
	}
}
