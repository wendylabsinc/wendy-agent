# `wendy device top` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wendy device top` — an htop-style live monitor showing host CPU/RAM/GPU and per-container CPU%/RAM, app-grouped.

**Architecture:** A new unary `GetResourceStats` RPC on the v1 `WendyContainerService` returns *cumulative* host CPU jiffies, host memory, host GPU samples, and per-container cumulative CPU-nanos + memory. The CLI polls it (default every 2s), computes CPU percentages from deltas against the previous sample, and renders either a live bubbletea TUI (interactive terminal) or a one-shot table/JSON snapshot (piped / `--json`). The agent stays stateless; all rate math is client-side.

**Tech Stack:** Go 1.26 (cobra CLI + agent), `charmbracelet/bubbletea` + `bubbles/table` TUI, protobuf/gRPC (protoc via `go/scripts/generate-proto.sh`), containerd cgroup metrics.

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy`. All Go imports use this prefix.
- Agent host/GPU gathering is Linux-only and reads `/proc` directly or shells out to `tegrastats`/`nvidia-smi`. **No new third-party dependencies** (no gopsutil) — matches house style.
- Proto changes go in **v1** (`Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`), the version the CLI dials via `agentpb.NewWendyContainerServiceClient`.
- Regenerate Go protobuf with `bash go/scripts/generate-proto.sh` (or `make -C go proto`). Do **not** regenerate Swift protos — they are checked in and regenerated manually; leaving them untouched keeps the Swift agent compiling.
- Run Go tests with `go test ./... -count=1` from the `go/` directory (or a package-scoped path). Build CLI with `go build ./cmd/wendy` and agent with `go build ./cmd/wendy-agent`, both from `go/`.
- The CLI exposes global persistent flags `--json` (`jsonOutput`) and `--device` (`deviceFlag`); access them as package-level vars in `package commands`.
- Older agents (and the macOS dev agent) will not implement the new RPC and return gRPC `codes.Unimplemented`. Every CLI path MUST degrade gracefully on `Unimplemented` (show "unavailable"/"—", never crash), mirroring `runStatsPoll` in `apps_dashboard.go:344`.

## Deviations from the design spec (deliberate)

1. **`ResourceContainerStats` has no separate `service` field.** Per-container stats are keyed by container ID in `app_name` exactly like the existing `ContainerStats` (containerd `ctr.ID()` is `appID` or `appID_serviceName`). App grouping is done CLI-side against `ListContainers`, identical to `buildDashboardRows`. Adding a redundant `service` field would be unused cruft (YAGNI).
2. **No Swift/macOS agent implementation in this plan.** Because every CLI path must already handle `Unimplemented` gracefully (covering older Linux agents), the macOS dev agent is covered by the same path. A native Swift implementation can be a follow-up. This is strictly more robust and less work than a Swift stub.

## File Structure

- `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto` — **modify**: add RPC + 4 messages.
- `go/proto/gen/agentpb/*` — **regenerated** (do not hand-edit).
- `go/internal/agent/hoststats/hoststats.go` — **create**: `/proc` CPU+memory parsers and readers.
- `go/internal/agent/hoststats/hoststats_test.go` — **create**.
- `go/internal/agent/hoststats/gpu.go` — **create**: tegrastats + nvidia-smi parsers and sampler.
- `go/internal/agent/hoststats/gpu_test.go` — **create**.
- `go/internal/agent/containerd/client.go` — **modify**: add `GetResourceStats`.
- `go/internal/agent/containerd/client_resourcestats_test.go` — **create**: CPU-nanos extraction test.
- `go/internal/agent/services/interfaces.go` — **modify**: add `GetResourceStats` to `ContainerdClient`.
- `go/internal/agent/services/container_service.go` — **modify**: add `GetResourceStats` handler.
- `go/internal/agent/services/container_service_test.go` — **modify**: extend `mockContainerdClient`, add handler test.
- `go/internal/cli/commands/device_top.go` — **create**: command, snapshot/JSON, CPU%-delta helpers, TUI model.
- `go/internal/cli/commands/device_top_test.go` — **create**: CPU%-delta + row-builder + JSON tests.
- `go/internal/cli/commands/device.go` — **modify**: register `newTopCmd()`.

---

### Task 1: Proto — add `GetResourceStats` RPC and messages

**Files:**
- Modify: `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`
- Regenerate: `go/proto/gen/agentpb/*`

**Interfaces:**
- Produces (Go generated types consumed by later tasks): `agentpb.GetResourceStatsRequest{}`, `agentpb.GetResourceStatsResponse` (`GetHost() *HostStats`, `GetContainers() []*ResourceContainerStats`), `agentpb.HostStats` (`GetCpuTotalJiffies() uint64`, `GetCpuIdleJiffies() uint64`, `GetCpuCount() uint32`, `GetMemTotalBytes() int64`, `GetMemAvailableBytes() int64`, `GetGpus() []*GpuStats`), `agentpb.GpuStats` (`GetIndex() uint32`, `GetName() string`, `GetUtilPercent() float64`, `GetMemUsedBytes() int64`, `GetMemTotalBytes() int64`, `GetTempC() float64`, `GetPowerW() float64`), `agentpb.ResourceContainerStats` (`GetAppName() string`, `GetCpuUsageNanos() uint64`, `GetMemoryBytes() int64`). Client method `WendyContainerServiceClient.GetResourceStats(ctx, *GetResourceStatsRequest, ...) (*GetResourceStatsResponse, error)` and server stub on `UnimplementedWendyContainerServiceServer`.

- [ ] **Step 1: Add the RPC to the service block**

In `wendy_agent_v1_container_service.proto`, inside `service WendyContainerService {`, add after the `ListContainerStats` line (currently line 20):

```protobuf
    rpc GetResourceStats(GetResourceStatsRequest) returns (GetResourceStatsResponse);
```

- [ ] **Step 2: Add the messages**

After the `ListContainerStatsResponse` message (currently ending at line 247), add:

```protobuf
// --- Resource stats (host + per-container, for `wendy device top`) ---

message GetResourceStatsRequest {}

message GetResourceStatsResponse {
    HostStats host = 1;
    repeated ResourceContainerStats containers = 2;
}

// HostStats carries cumulative host counters; the client computes percentages
// from deltas between consecutive samples.
message HostStats {
    uint64 cpu_total_jiffies = 1;    // sum of all fields on the /proc/stat "cpu" line
    uint64 cpu_idle_jiffies = 2;     // idle + iowait fields
    uint32 cpu_count = 3;            // online logical CPUs
    int64 mem_total_bytes = 4;
    int64 mem_available_bytes = 5;
    repeated GpuStats gpus = 6;      // empty when no GPU / no sampler tool present
}

message GpuStats {
    uint32 index = 1;
    string name = 2;
    double util_percent = 3;         // instantaneous utilization as reported by the sampler
    int64 mem_used_bytes = 4;
    int64 mem_total_bytes = 5;
    optional double temp_c = 6;
    optional double power_w = 7;
}

// ResourceContainerStats mirrors ContainerStats keying: app_name is the
// containerd container ID (appID, or appID_serviceName for multi-service apps).
message ResourceContainerStats {
    string app_name = 1;
    uint64 cpu_usage_nanos = 2;      // cumulative user+sys CPU nanoseconds
    int64 memory_bytes = 3;          // current cgroup memory usage
}
```

- [ ] **Step 3: Regenerate Go protobuf**

Run: `bash go/scripts/generate-proto.sh`
Expected: completes without error; `git status` shows modified files under `go/proto/gen/agentpb/`.

- [ ] **Step 4: Verify the generated symbols exist and the module builds**

Run: `cd go && go build ./... 2>&1 | head`
Expected: builds clean. Then confirm the new client method exists:
Run: `grep -rn "GetResourceStats" go/proto/gen/agentpb/*.go | head`
Expected: matches for `GetResourceStats` on both client and server interfaces, plus the new message types.

- [ ] **Step 5: Commit**

```bash
git add Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto go/proto/gen/agentpb
git commit -m "proto: add v1 GetResourceStats RPC for device top"
```

---

### Task 2: Agent — host CPU + memory readers (`hoststats` package)

**Files:**
- Create: `go/internal/agent/hoststats/hoststats.go`
- Test: `go/internal/agent/hoststats/hoststats_test.go`

**Interfaces:**
- Produces: `hoststats.CPUSample{TotalJiffies uint64; IdleJiffies uint64; CPUCount uint32}`, `hoststats.MemSample{TotalBytes int64; AvailableBytes int64}`, `func ParseProcStat(data []byte) (CPUSample, error)`, `func ParseMemInfo(data []byte) (MemSample, error)`, `func ReadCPU() (CPUSample, error)`, `func ReadMemory() (MemSample, error)`.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/hoststats/hoststats_test.go`:

```go
package hoststats

import "testing"

func TestParseProcStat(t *testing.T) {
	// cpu  <user> <nice> <system> <idle> <iowait> <irq> <softirq> <steal> ...
	data := []byte("cpu  100 0 200 700 50 0 10 0 0 0\n" +
		"cpu0 50 0 100 350 25 0 5 0 0 0\n" +
		"cpu1 50 0 100 350 25 0 5 0 0 0\n" +
		"intr 12345\n")
	got, err := ParseProcStat(data)
	if err != nil {
		t.Fatalf("ParseProcStat: %v", err)
	}
	// total = 100+0+200+700+50+0+10+0+0+0 = 1060
	if got.TotalJiffies != 1060 {
		t.Errorf("TotalJiffies = %d, want 1060", got.TotalJiffies)
	}
	// idle = idle(700) + iowait(50) = 750
	if got.IdleJiffies != 750 {
		t.Errorf("IdleJiffies = %d, want 750", got.IdleJiffies)
	}
	if got.CPUCount != 2 {
		t.Errorf("CPUCount = %d, want 2", got.CPUCount)
	}
}

func TestParseMemInfo(t *testing.T) {
	data := []byte("MemTotal:       16384000 kB\n" +
		"MemFree:         1000000 kB\n" +
		"MemAvailable:    8192000 kB\n")
	got, err := ParseMemInfo(data)
	if err != nil {
		t.Fatalf("ParseMemInfo: %v", err)
	}
	if got.TotalBytes != 16384000*1024 {
		t.Errorf("TotalBytes = %d, want %d", got.TotalBytes, 16384000*1024)
	}
	if got.AvailableBytes != 8192000*1024 {
		t.Errorf("AvailableBytes = %d, want %d", got.AvailableBytes, 8192000*1024)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/agent/hoststats/ 2>&1 | head`
Expected: build failure — `undefined: ParseProcStat` / package does not compile.

- [ ] **Step 3: Implement the parsers and readers**

Create `go/internal/agent/hoststats/hoststats.go`:

```go
// Package hoststats reads host-level CPU, memory, and GPU utilization on Linux
// for the `wendy device top` command. CPU and memory come from /proc; GPU comes
// from tegrastats or nvidia-smi (see gpu.go). All counters are reported raw and
// cumulative — callers compute rates from deltas.
package hoststats

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CPUSample is a snapshot of cumulative host CPU jiffies from /proc/stat.
type CPUSample struct {
	TotalJiffies uint64
	IdleJiffies  uint64
	CPUCount     uint32
}

// MemSample is a snapshot of host memory from /proc/meminfo.
type MemSample struct {
	TotalBytes     int64
	AvailableBytes int64
}

// ParseProcStat parses /proc/stat contents into a CPUSample. TotalJiffies is the
// sum of all numeric fields on the aggregate "cpu " line; IdleJiffies is idle +
// iowait (fields 4 and 5). CPUCount is the number of per-core "cpuN" lines.
func ParseProcStat(data []byte) (CPUSample, error) {
	var s CPUSample
	sc := bufio.NewScanner(bytes.NewReader(data))
	foundAgg := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			foundAgg = true
			for i := 1; i < len(fields); i++ {
				v, err := strconv.ParseUint(fields[i], 10, 64)
				if err != nil {
					return CPUSample{}, fmt.Errorf("parsing /proc/stat cpu field %d: %w", i, err)
				}
				s.TotalJiffies += v
				if i == 4 || i == 5 { // idle, iowait
					s.IdleJiffies += v
				}
			}
		case strings.HasPrefix(fields[0], "cpu"):
			s.CPUCount++
		}
	}
	if err := sc.Err(); err != nil {
		return CPUSample{}, err
	}
	if !foundAgg {
		return CPUSample{}, fmt.Errorf("no aggregate cpu line in /proc/stat")
	}
	return s, nil
}

// ParseMemInfo parses /proc/meminfo contents (values are in kB) into a MemSample.
func ParseMemInfo(data []byte) (MemSample, error) {
	var s MemSample
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			s.TotalBytes = kb * 1024
		case "MemAvailable:":
			s.AvailableBytes = kb * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return MemSample{}, err
	}
	return s, nil
}

// ReadCPU reads and parses /proc/stat.
func ReadCPU() (CPUSample, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUSample{}, err
	}
	return ParseProcStat(data)
}

// ReadMemory reads and parses /proc/meminfo.
func ReadMemory() (MemSample, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemSample{}, err
	}
	return ParseMemInfo(data)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/hoststats/ 2>&1 | head`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/hoststats/hoststats.go go/internal/agent/hoststats/hoststats_test.go
git commit -m "agent: add host CPU/memory readers for device top"
```

---

### Task 3: Agent — GPU samplers (tegrastats + nvidia-smi)

**Files:**
- Create: `go/internal/agent/hoststats/gpu.go`
- Test: `go/internal/agent/hoststats/gpu_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `hoststats.GPUStat{Index uint32; Name string; UtilPercent float64; MemUsedBytes int64; MemTotalBytes int64; TempC *float64; PowerW *float64}`, `func ParseNvidiaSMI(csv string) []GPUStat`, `func ParseTegrastats(line string) []GPUStat`, `func SampleGPU(ctx context.Context) []GPUStat` (returns nil when no tool is available — never errors).

> **NOTE on tegrastats:** its line format varies across JetPack versions. The fixture below is representative of JetPack 5/6 on Orin. When validating on real hardware, capture an actual line (`tegrastats --interval 1000` for one line) and adjust `ParseTegrastats`/the test fixture if fields differ. Parsing is intentionally regex-based and tolerant of missing fields.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/agent/hoststats/gpu_test.go`:

```go
package hoststats

import (
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	// --query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw
	// --format=csv,noheader,nounits  (memory in MiB)
	csv := "0, NVIDIA RTX A2000, 12, 1024, 6138, 45, 18.42\n"
	got := ParseNvidiaSMI(csv)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.Index != 0 || g.Name != "NVIDIA RTX A2000" {
		t.Errorf("index/name = %d/%q", g.Index, g.Name)
	}
	if g.UtilPercent != 12 {
		t.Errorf("util = %v, want 12", g.UtilPercent)
	}
	if g.MemUsedBytes != 1024*1024*1024 { // 1024 MiB
		t.Errorf("memUsed = %d, want %d", g.MemUsedBytes, 1024*1024*1024)
	}
	if g.MemTotalBytes != 6138*1024*1024 {
		t.Errorf("memTotal = %d", g.MemTotalBytes)
	}
	if g.TempC == nil || *g.TempC != 45 {
		t.Errorf("tempC = %v, want 45", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 18.42 {
		t.Errorf("powerW = %v, want 18.42", g.PowerW)
	}
}

func TestParseTegrastats(t *testing.T) {
	line := "RAM 4096/30536MB (lfb 5x4MB) SWAP 0/15268MB (cached 0MB) " +
		"CPU [10%@1190,5%@1190] GR3D_FREQ 37% cpu@49C GPU@48C " +
		"VDD_GPU_SOC 1234mW/1234mW"
	got := ParseTegrastats(line)
	if len(got) != 1 {
		t.Fatalf("got %d gpus, want 1", len(got))
	}
	g := got[0]
	if g.UtilPercent != 37 {
		t.Errorf("util = %v, want 37", g.UtilPercent)
	}
	if g.TempC == nil || *g.TempC != 48 {
		t.Errorf("tempC = %v, want 48", g.TempC)
	}
	if g.PowerW == nil || *g.PowerW != 1.234 { // 1234 mW
		t.Errorf("powerW = %v, want 1.234", g.PowerW)
	}
}

func TestParseTegrastatsNoGPUFields(t *testing.T) {
	// A line with no GR3D_FREQ should yield no GPU entries rather than a bogus 0%.
	got := ParseTegrastats("RAM 100/200MB CPU [1%@100]")
	if len(got) != 0 {
		t.Errorf("got %d gpus, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/agent/hoststats/ -run GPU 2>&1 | head`
Expected: build failure — `undefined: ParseNvidiaSMI` / `ParseTegrastats`.

- [ ] **Step 3: Implement the GPU parsers and sampler**

Create `go/internal/agent/hoststats/gpu.go`:

```go
package hoststats

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GPUStat is a single GPU's instantaneous utilization snapshot.
type GPUStat struct {
	Index         uint32
	Name          string
	UtilPercent   float64
	MemUsedBytes  int64
	MemTotalBytes int64
	TempC         *float64
	PowerW        *float64
}

// ParseNvidiaSMI parses CSV output of:
//   nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw
//              --format=csv,noheader,nounits
// Memory fields are MiB. Missing/[N/A] numeric fields are treated as zero/nil.
func ParseNvidiaSMI(csv string) []GPUStat {
	var out []GPUStat
	for _, line := range strings.Split(csv, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		if len(f) < 5 {
			continue
		}
		g := GPUStat{Name: f[1]}
		if v, err := strconv.ParseUint(f[0], 10, 32); err == nil {
			g.Index = uint32(v)
		}
		if v, err := strconv.ParseFloat(f[2], 64); err == nil {
			g.UtilPercent = v
		}
		if v, err := strconv.ParseInt(f[3], 10, 64); err == nil {
			g.MemUsedBytes = v * 1024 * 1024
		}
		if v, err := strconv.ParseInt(f[4], 10, 64); err == nil {
			g.MemTotalBytes = v * 1024 * 1024
		}
		if len(f) > 5 {
			if v, err := strconv.ParseFloat(f[5], 64); err == nil {
				g.TempC = &v
			}
		}
		if len(f) > 6 {
			if v, err := strconv.ParseFloat(f[6], 64); err == nil {
				g.PowerW = &v
			}
		}
		out = append(out, g)
	}
	return out
}

var (
	tegraGR3DRe  = regexp.MustCompile(`GR3D_FREQ (\d+)%`)
	tegraGPUTemp = regexp.MustCompile(`GPU@([\d.]+)C`)
	tegraGPUPwr  = regexp.MustCompile(`VDD_GPU_SOC (\d+)mW`)
)

// ParseTegrastats extracts the integrated-GPU utilization (GR3D_FREQ) and, when
// present, GPU temperature and power from a single tegrastats line. Jetson uses
// unified memory, so per-GPU memory is left zero. Returns no entries when the
// line has no GR3D_FREQ field.
func ParseTegrastats(line string) []GPUStat {
	m := tegraGR3DRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	g := GPUStat{Name: "Integrated GPU"}
	if v, err := strconv.ParseFloat(m[1], 64); err == nil {
		g.UtilPercent = v
	}
	if t := tegraGPUTemp.FindStringSubmatch(line); t != nil {
		if v, err := strconv.ParseFloat(t[1], 64); err == nil {
			g.TempC = &v
		}
	}
	if p := tegraGPUPwr.FindStringSubmatch(line); p != nil {
		if mw, err := strconv.ParseFloat(p[1], 64); err == nil {
			w := mw / 1000.0
			g.PowerW = &w
		}
	}
	return []GPUStat{g}
}

// SampleGPU returns a one-shot GPU sample, preferring nvidia-smi (discrete GPUs)
// and falling back to tegrastats (Jetson). Returns nil when neither tool is
// available — callers treat that as "no GPU panel", not an error.
func SampleGPU(ctx context.Context) []GPUStat {
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, path,
			"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
			"--format=csv,noheader,nounits").Output()
		if err == nil {
			if gpus := ParseNvidiaSMI(string(out)); len(gpus) > 0 {
				return gpus
			}
		}
	}
	if path, err := exec.LookPath("tegrastats"); err == nil {
		// tegrastats streams; --interval + a short deadline yields one line.
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(cctx, path, "--interval", "500", "--count", "1").Output()
		if line := strings.TrimSpace(string(out)); line != "" {
			return ParseTegrastats(line)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/hoststats/ 2>&1 | head`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/hoststats/gpu.go go/internal/agent/hoststats/gpu_test.go
git commit -m "agent: add tegrastats/nvidia-smi GPU samplers for device top"
```

---

### Task 4: Agent — containerd `GetResourceStats`

**Files:**
- Modify: `go/internal/agent/containerd/client.go` (add method after `GetContainerStats`, ~line 2495)
- Modify: `go/internal/agent/services/interfaces.go` (add to `ContainerdClient` interface, ~line 61)
- Test: `go/internal/agent/containerd/client_resourcestats_test.go`

**Interfaces:**
- Consumes: `agentpb.ResourceContainerStats` (Task 1), existing `extractContainerMetrics` (`client.go:2515`) and `services.ContainerMetrics` (`interfaces.go:125`, fields `UserCPUNanos`, `SysCPUNanos`, `MemBytes`).
- Produces: `func (c *Client) GetResourceStats(ctx context.Context) ([]*agentpb.ResourceContainerStats, error)`; interface method `GetResourceStats(ctx context.Context) ([]*agentpb.ResourceContainerStats, error)` on `ContainerdClient`. Helper `func cpuUsageNanos(m services.ContainerMetrics) uint64` (sum of user+sys, clamped at 0).

- [ ] **Step 1: Write the failing test for the cpu-nanos helper**

Create `go/internal/agent/containerd/client_resourcestats_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/agent/containerd/ -run TestCPUUsageNanos 2>&1 | head`
Expected: build failure — `undefined: cpuUsageNanos`.

- [ ] **Step 3: Implement `cpuUsageNanos` and `GetResourceStats`**

In `go/internal/agent/containerd/client.go`, immediately after `GetContainerStats` (after line 2495), add:

```go
// cpuUsageNanos returns cumulative user+sys CPU nanoseconds, clamped at 0.
func cpuUsageNanos(m services.ContainerMetrics) uint64 {
	total := m.UserCPUNanos + m.SysCPUNanos
	if total < 0 {
		return 0
	}
	return uint64(total)
}

// GetResourceStats returns cumulative per-container CPU nanoseconds and current
// memory usage, keyed by container ID (matching GetContainerStats). The client
// computes CPU percentages from deltas between consecutive samples.
func (c *Client) GetResourceStats(ctx context.Context) ([]*agentpb.ResourceContainerStats, error) {
	ctx = c.withNamespace(ctx)

	containers, err := c.client.Containers(ctx, fmt.Sprintf("labels.%q", labelKeyAppVersion))
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var result []*agentpb.ResourceContainerStats
	for _, ctr := range containers {
		stat := &agentpb.ResourceContainerStats{AppName: ctr.ID()}
		if task, taskErr := ctr.Task(ctx, nil); taskErr == nil {
			if metric, metErr := task.Metrics(ctx); metErr == nil {
				m := extractContainerMetrics(metric)
				stat.CpuUsageNanos = cpuUsageNanos(m)
				stat.MemoryBytes = m.MemBytes
			}
		}
		result = append(result, stat)
	}
	return result, nil
}
```

- [ ] **Step 4: Add the method to the `ContainerdClient` interface**

In `go/internal/agent/services/interfaces.go`, in the `ContainerdClient` interface, after the `GetContainerStats` line (line 61) add:

```go
	GetResourceStats(ctx context.Context) ([]*agentpb.ResourceContainerStats, error)
```

- [ ] **Step 5: Run the test and build to verify**

Run: `cd go && go test ./internal/agent/containerd/ -run TestCPUUsageNanos 2>&1 | head`
Expected: PASS.
Run: `cd go && go build ./internal/agent/... 2>&1 | head`
Expected: FAILS to build `services` package — `mockContainerdClient` does not implement `GetResourceStats`. That is fixed in Task 5. (The containerd package itself builds.)

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/containerd/client.go go/internal/agent/containerd/client_resourcestats_test.go go/internal/agent/services/interfaces.go
git commit -m "agent: add containerd GetResourceStats (per-container CPU nanos + mem)"
```

---

### Task 5: Agent — `ContainerService.GetResourceStats` handler

**Files:**
- Modify: `go/internal/agent/services/container_service.go` (add handler after `ListContainerStats`, ~line 904)
- Modify: `go/internal/agent/services/container_service_test.go` (extend `mockContainerdClient` at ~line 97; add handler test)

**Interfaces:**
- Consumes: `c.containerd.GetResourceStats` (Task 4), `hoststats.ReadCPU/ReadMemory/SampleGPU` (Tasks 2–3), `agentpb.GetResourceStatsResponse/HostStats/GpuStats/ResourceContainerStats` (Task 1).
- Produces: `func (s *ContainerService) GetResourceStats(ctx context.Context, _ *agentpb.GetResourceStatsRequest) (*agentpb.GetResourceStatsResponse, error)` and unexported `func gpuStatsToProto([]hoststats.GPUStat) []*agentpb.GpuStats`.

- [ ] **Step 1: Extend the mock to satisfy the interface**

In `go/internal/agent/services/container_service_test.go`, just after the existing `GetContainerStats` mock method (line 97-100), add:

```go
func (m *mockContainerdClient) GetResourceStats(_ context.Context) ([]*agentpb.ResourceContainerStats, error) {
	return m.resourceStats, m.resourceStatsErr
}
```

Then add the backing fields to the `mockContainerdClient` struct definition (find `type mockContainerdClient struct {` in the same file and add):

```go
	resourceStats    []*agentpb.ResourceContainerStats
	resourceStatsErr error
```

- [ ] **Step 2: Write the failing handler test**

Append to `go/internal/agent/services/container_service_test.go`:

```go
func TestGetResourceStatsHandler(t *testing.T) {
	svc := &ContainerService{
		logger: zap.NewNop(),
		containerd: &mockContainerdClient{
			resourceStats: []*agentpb.ResourceContainerStats{
				{AppName: "myapp", CpuUsageNanos: 5000, MemoryBytes: 2048},
			},
		},
	}
	resp, err := svc.GetResourceStats(context.Background(), &agentpb.GetResourceStatsRequest{})
	if err != nil {
		t.Fatalf("GetResourceStats: %v", err)
	}
	if len(resp.GetContainers()) != 1 || resp.GetContainers()[0].GetAppName() != "myapp" {
		t.Fatalf("unexpected containers: %+v", resp.GetContainers())
	}
	if resp.GetContainers()[0].GetCpuUsageNanos() != 5000 {
		t.Errorf("cpu = %d, want 5000", resp.GetContainers()[0].GetCpuUsageNanos())
	}
	// Host is populated best-effort; on a non-Linux test host the /proc reads may
	// fail, so we only assert the host message is present (non-nil).
	if resp.GetHost() == nil {
		t.Errorf("host stats missing")
	}
}
```

> If the existing mock construction in this file uses a different field name than `containerd` or a constructor, match the pattern already used by `TestListContainerStats`-style tests in the same file.

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestGetResourceStatsHandler 2>&1 | head`
Expected: build failure — `svc.GetResourceStats undefined`.

- [ ] **Step 4: Implement the handler**

In `go/internal/agent/services/container_service.go`, add the import for hoststats to the import block:

```go
	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
```

Then, after `ListContainerStats` (after line 904), add:

```go
// GetResourceStats returns host CPU/memory/GPU counters plus per-container CPU
// and memory for `wendy device top`. Host metrics are best-effort: a failed
// /proc read or absent GPU tool yields zero/empty fields rather than an error,
// so the command degrades gracefully on constrained hosts.
func (s *ContainerService) GetResourceStats(ctx context.Context, _ *agentpb.GetResourceStatsRequest) (*agentpb.GetResourceStatsResponse, error) {
	containers, err := s.containerd.GetResourceStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting resource stats: %v", err)
	}

	host := &agentpb.HostStats{}
	if cpu, cpuErr := hoststats.ReadCPU(); cpuErr == nil {
		host.CpuTotalJiffies = cpu.TotalJiffies
		host.CpuIdleJiffies = cpu.IdleJiffies
		host.CpuCount = cpu.CPUCount
	}
	if mem, memErr := hoststats.ReadMemory(); memErr == nil {
		host.MemTotalBytes = mem.TotalBytes
		host.MemAvailableBytes = mem.AvailableBytes
	}
	host.Gpus = gpuStatsToProto(hoststats.SampleGPU(ctx))

	return &agentpb.GetResourceStatsResponse{
		Host:       host,
		Containers: containers,
	}, nil
}

func gpuStatsToProto(in []hoststats.GPUStat) []*agentpb.GpuStats {
	out := make([]*agentpb.GpuStats, 0, len(in))
	for _, g := range in {
		pg := &agentpb.GpuStats{
			Index:         g.Index,
			Name:          g.Name,
			UtilPercent:   g.UtilPercent,
			MemUsedBytes:  g.MemUsedBytes,
			MemTotalBytes: g.MemTotalBytes,
			TempC:         g.TempC,
			PowerW:        g.PowerW,
		}
		out = append(out, pg)
	}
	return out
}
```

- [ ] **Step 5: Run the test and build agent**

Run: `cd go && go test ./internal/agent/services/ -run TestGetResourceStatsHandler 2>&1 | head`
Expected: PASS.
Run: `cd go && go build ./cmd/wendy-agent 2>&1 | head`
Expected: builds clean. (The generated server already routes `GetResourceStats` to this method; the `Unimplemented` embed is overridden.)

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/container_service.go go/internal/agent/services/container_service_test.go
git commit -m "agent: wire ContainerService.GetResourceStats (host + GPU + containers)"
```

---

### Task 6: CLI — CPU%-delta computation and row builder (pure helpers)

**Files:**
- Create: `go/internal/cli/commands/device_top.go` (helpers + types only in this task)
- Test: `go/internal/cli/commands/device_top_test.go`

**Interfaces:**
- Consumes: `agentpb.GetResourceStatsResponse`, `agentpb.AppContainer` (existing), `formatBytes` (`bytes_format.go:14`).
- Produces:
  - `type topSample struct { host *agentpb.HostStats; containers map[string]uint64; mem map[string]int64; takenAtNanos int64 }`
  - `func newTopSample(resp *agentpb.GetResourceStatsResponse, atNanos int64) topSample`
  - `func hostCPUPercent(prev, cur topSample) float64` — `(1 - idleΔ/totalΔ)*100`, 0 when no delta.
  - `func containerCPUPercent(prev, cur topSample, id string, cpuCount uint32) float64` — `(cpuNanosΔ / (wallNanosΔ * cpuCount)) * 100`, 0 when missing/no delta. Share of the whole machine.
  - `type topRow struct { name, displayName string; cpuPercent float64; memBytes int64; hasCPU bool; isGroupHeader, isSubrow bool }`
  - `func buildTopRows(containers []*agentpb.AppContainer, cpuByID map[string]float64, memByID map[string]int64, sortByCPU bool) []topRow` — app-grouped like `buildDashboardRows`; top-level apps sorted by the active key (CPU or memory) descending, subrows kept under their header.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/device_top_test.go`:

```go
package commands

import (
	"math"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestHostCPUPercent|TestContainerCPUPercent|TestBuildTopRows' 2>&1 | head`
Expected: build failure — `undefined: topSample` etc.

- [ ] **Step 3: Implement the helpers**

Create `go/internal/cli/commands/device_top.go`:

```go
package commands

import (
	"sort"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// topSample is a normalized snapshot used to compute CPU% from deltas.
type topSample struct {
	host         *agentpb.HostStats
	containers   map[string]uint64 // container ID -> cumulative cpu nanos
	mem          map[string]int64  // container ID -> memory bytes
	takenAtNanos int64
}

func newTopSample(resp *agentpb.GetResourceStatsResponse, atNanos int64) topSample {
	s := topSample{
		host:         resp.GetHost(),
		containers:   make(map[string]uint64),
		mem:          make(map[string]int64),
		takenAtNanos: atNanos,
	}
	for _, c := range resp.GetContainers() {
		s.containers[c.GetAppName()] = c.GetCpuUsageNanos()
		s.mem[c.GetAppName()] = c.GetMemoryBytes()
	}
	return s
}

// hostCPUPercent returns busy CPU percentage (0-100) across the whole machine,
// computed from the idle/total jiffy deltas between two samples.
func hostCPUPercent(prev, cur topSample) float64 {
	if prev.host == nil || cur.host == nil {
		return 0
	}
	totalΔ := int64(cur.host.GetCpuTotalJiffies()) - int64(prev.host.GetCpuTotalJiffies())
	idleΔ := int64(cur.host.GetCpuIdleJiffies()) - int64(prev.host.GetCpuIdleJiffies())
	if totalΔ <= 0 {
		return 0
	}
	busy := (1 - float64(idleΔ)/float64(totalΔ)) * 100
	if busy < 0 {
		return 0
	}
	return busy
}

// containerCPUPercent returns a container's CPU usage as a percentage of the
// whole machine (0-100 across all cores), from the CPU-nanos delta over elapsed
// wall time. cpuCount normalizes to "share of total machine".
func containerCPUPercent(prev, cur topSample, id string, cpuCount uint32) float64 {
	wallΔ := cur.takenAtNanos - prev.takenAtNanos
	if wallΔ <= 0 || cpuCount == 0 {
		return 0
	}
	prevNanos, ok := prev.containers[id]
	if !ok {
		return 0
	}
	curNanos := cur.containers[id]
	if curNanos < prevNanos {
		return 0 // counter reset / container restarted
	}
	pct := float64(curNanos-prevNanos) / (float64(wallΔ) * float64(cpuCount)) * 100
	if pct < 0 {
		return 0
	}
	return pct
}

// topRow is one display row (app, group header, or service subrow).
type topRow struct {
	name        string // app ID; "" for subrows
	displayName string
	cpuPercent  float64
	memBytes    int64
	hasCPU      bool
	isGroupHeader bool
	isSubrow      bool
}

// buildTopRows groups containers by app (mirroring buildDashboardRows) with CPU%
// and memory columns. Top-level apps are sorted by the active key descending;
// service subrows stay under their group header.
func buildTopRows(containers []*agentpb.AppContainer, cpuByID map[string]float64, memByID map[string]int64, sortByCPU bool) []topRow {
	type appAgg struct {
		container *agentpb.AppContainer
		cpu       float64
		mem       int64
	}
	aggs := make([]appAgg, 0, len(containers))
	for _, c := range containers {
		appName := c.GetAppName()
		var cpu float64
		var mem int64
		if len(c.GetServices()) > 1 {
			for _, svc := range c.GetServices() {
				key := appName + "_" + svc.GetName()
				cpu += cpuByID[key]
				mem += memByID[key]
			}
		} else {
			cpu = cpuByID[appName]
			mem = memByID[appName]
		}
		aggs = append(aggs, appAgg{container: c, cpu: cpu, mem: mem})
	}

	sort.SliceStable(aggs, func(i, j int) bool {
		if sortByCPU {
			return aggs[i].cpu > aggs[j].cpu
		}
		return aggs[i].mem > aggs[j].mem
	})

	var rows []topRow
	for _, a := range aggs {
		c := a.container
		appName := c.GetAppName()
		if len(c.GetServices()) > 1 {
			rows = append(rows, topRow{
				name:          appName,
				displayName:   appName + " [group]",
				cpuPercent:    a.cpu,
				memBytes:      a.mem,
				hasCPU:        true,
				isGroupHeader: true,
			})
			for _, svc := range c.GetServices() {
				key := appName + "_" + svc.GetName()
				rows = append(rows, topRow{
					displayName: "  ↳ " + svc.GetName(),
					cpuPercent:  cpuByID[key],
					memBytes:    memByID[key],
					hasCPU:      true,
					isSubrow:    true,
				})
			}
		} else {
			rows = append(rows, topRow{
				name:        appName,
				displayName: appName,
				cpuPercent:  a.cpu,
				memBytes:    a.mem,
				hasCPU:      true,
			})
		}
	}
	return rows
}
```

> Note: the non-ASCII `Δ` identifier is valid in Go (Unicode letters are allowed). If the implementer prefers ASCII, rename to `totalDelta`/`idleDelta`/`wallDelta` consistently.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestHostCPUPercent|TestContainerCPUPercent|TestBuildTopRows' 2>&1 | head`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/device_top.go go/internal/cli/commands/device_top_test.go
git commit -m "cli: add CPU%-delta and row-builder helpers for device top"
```

---

### Task 7: CLI — command, one-shot snapshot, JSON, graceful Unimplemented

**Files:**
- Modify: `go/internal/cli/commands/device_top.go` (add command + snapshot/JSON)
- Modify: `go/internal/cli/commands/device.go` (register in `newDeviceCmd`)
- Test: `go/internal/cli/commands/device_top_test.go` (add JSON-shape test)

**Interfaces:**
- Consumes: `connectToAgent` (`helpers.go:617`), `isInteractiveTerminal` (`helpers.go:514`), `jsonOutput` global, helpers from Task 6, `formatBytes`.
- Produces:
  - `func newTopCmd() *cobra.Command` (use: `top`, group `common`, flag `--interval` default `2s`).
  - `type topJSONOutput struct{...}` and `func buildTopJSON(prev, cur topSample, containers []*agentpb.AppContainer) topJSONOutput`.
  - `func runTopSnapshot(ctx, conn, asJSON bool) error` — two samples ~250ms apart, then print table or JSON.
  - `func sampleResourceStats(ctx, conn) (*agentpb.GetResourceStatsResponse, error)`.

- [ ] **Step 1: Write the failing JSON-shape test**

Append to `go/internal/cli/commands/device_top_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildTopJSON 2>&1 | head`
Expected: build failure — `undefined: buildTopJSON`.

- [ ] **Step 3: Implement command, sampling, snapshot, and JSON**

Add to the imports of `go/internal/cli/commands/device_top.go`:

```go
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
```

Append to `device_top.go`:

```go
// errResourceStatsUnimplemented marks an agent too old to support device top.
var errResourceStatsUnimplemented = fmt.Errorf("the device's agent does not support resource stats; update it with 'wendy device update'")

func sampleResourceStats(ctx context.Context, conn *grpcclient.AgentConnection) (*agentpb.GetResourceStatsResponse, error) {
	resp, err := conn.ContainerService.GetResourceStats(ctx, &agentpb.GetResourceStatsRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, errResourceStatsUnimplemented
		}
		return nil, err
	}
	return resp, nil
}

func listAppContainers(ctx context.Context, conn *grpcclient.AgentConnection) ([]*agentpb.AppContainer, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil, err
	}
	var out []*agentpb.AppContainer
	for {
		resp, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" || status.Code(err) == codes.OK {
				break
			}
			// io.EOF check:
			break
		}
		if c := resp.GetContainer(); c != nil {
			out = append(out, c)
		}
	}
	return out, nil
}

type topJSONHost struct {
	CPUPercent     float64        `json:"cpuPercent"`
	CPUCount       uint32         `json:"cpuCount"`
	MemUsedBytes   int64          `json:"memUsedBytes"`
	MemTotalBytes  int64          `json:"memTotalBytes"`
	GPUs           []topJSONGPU   `json:"gpus,omitempty"`
}

type topJSONGPU struct {
	Index         uint32   `json:"index"`
	Name          string   `json:"name"`
	UtilPercent   float64  `json:"utilPercent"`
	MemUsedBytes  int64    `json:"memUsedBytes"`
	MemTotalBytes int64    `json:"memTotalBytes"`
	TempC         *float64 `json:"tempC,omitempty"`
	PowerW        *float64 `json:"powerW,omitempty"`
}

type topJSONContainer struct {
	Name       string  `json:"name"`
	State      string  `json:"state"`
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   int64   `json:"memBytes"`
}

type topJSONOutput struct {
	Host       topJSONHost        `json:"host"`
	Containers []topJSONContainer `json:"containers"`
}

func buildTopJSON(prev, cur topSample, containers []*agentpb.AppContainer) topJSONOutput {
	out := topJSONOutput{}
	if cur.host != nil {
		out.Host.CPUPercent = hostCPUPercent(prev, cur)
		out.Host.CPUCount = cur.host.GetCpuCount()
		out.Host.MemTotalBytes = cur.host.GetMemTotalBytes()
		out.Host.MemUsedBytes = cur.host.GetMemTotalBytes() - cur.host.GetMemAvailableBytes()
		for _, g := range cur.host.GetGpus() {
			out.Host.GPUs = append(out.Host.GPUs, topJSONGPU{
				Index: g.GetIndex(), Name: g.GetName(), UtilPercent: g.GetUtilPercent(),
				MemUsedBytes: g.GetMemUsedBytes(), MemTotalBytes: g.GetMemTotalBytes(),
				TempC: g.TempC, PowerW: g.PowerW,
			})
		}
	}
	cpuCount := uint32(1)
	if cur.host != nil && cur.host.GetCpuCount() > 0 {
		cpuCount = cur.host.GetCpuCount()
	}
	cpuByID := map[string]float64{}
	for id := range cur.containers {
		cpuByID[id] = containerCPUPercent(prev, cur, id, cpuCount)
	}
	rows := buildTopRows(containers, cpuByID, cur.mem, false)
	for _, r := range rows {
		if r.isGroupHeader || r.isSubrow {
			// Flatten: include leaf apps and group headers; skip subrows for JSON brevity.
			if r.isSubrow {
				continue
			}
		}
		out.Containers = append(out.Containers, topJSONContainer{
			Name:       r.displayName,
			CPUPercent: r.cpuPercent,
			MemBytes:   r.memBytes,
		})
	}
	return out
}

func runTopSnapshot(ctx context.Context, conn *grpcclient.AgentConnection, asJSON bool) error {
	containers, err := listAppContainers(ctx, conn)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	first, err := sampleResourceStats(ctx, conn)
	if err != nil {
		return err
	}
	prev := newTopSample(first, time.Now().UnixNano())
	time.Sleep(250 * time.Millisecond)
	second, err := sampleResourceStats(ctx, conn)
	if err != nil {
		return err
	}
	cur := newTopSample(second, time.Now().UnixNano())

	if asJSON {
		data, err := json.MarshalIndent(buildTopJSON(prev, cur, containers), "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Plain table.
	cpuCount := uint32(1)
	if cur.host != nil && cur.host.GetCpuCount() > 0 {
		cpuCount = cur.host.GetCpuCount()
	}
	if cur.host != nil {
		fmt.Printf("CPU: %.1f%%  MEM: %s / %s\n",
			hostCPUPercent(prev, cur),
			formatBytes(cur.host.GetMemTotalBytes()-cur.host.GetMemAvailableBytes()),
			formatBytes(cur.host.GetMemTotalBytes()))
		for _, g := range cur.host.GetGpus() {
			fmt.Printf("GPU%d %s: %.0f%%  %s / %s\n", g.GetIndex(), g.GetName(),
				g.GetUtilPercent(), formatBytes(g.GetMemUsedBytes()), formatBytes(g.GetMemTotalBytes()))
		}
	}
	cpuByID := map[string]float64{}
	for id := range cur.containers {
		cpuByID[id] = containerCPUPercent(prev, cur, id, cpuCount)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "APP\tCPU%\tMEM")
	for _, r := range buildTopRows(containers, cpuByID, cur.mem, false) {
		fmt.Fprintf(tw, "%s\t%.1f\t%s\n", r.displayName, r.cpuPercent, formatBytes(r.memBytes))
	}
	return tw.Flush()
}

func newTopCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Live CPU, memory, and GPU usage for the device and its containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if jsonOutput || !isInteractiveTerminal() {
				return runTopSnapshot(ctx, conn, jsonOutput)
			}
			return runTopDashboard(ctx, conn, interval)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Refresh interval for the live view")
	return cmd
}
```

> `listAppContainers` must treat `io.EOF` as normal stream end. Replace the placeholder EOF handling with the real import: add `"io"` to imports and use `if err == io.EOF { break }` exactly as `runContainersPoll` does in `apps_dashboard.go:302`. (`runTopDashboard` is implemented in Task 8; this task will not compile standalone until Task 8 adds it — implement Task 8 in the same branch before running the CLI build in Step 5. The unit tests for this task do not reference `runTopDashboard`.)

- [ ] **Step 4: Register the command**

In `go/internal/cli/commands/device.go`, in `newDeviceCmd`, add `newTopCmd()` to the `addToGroup("common", ...)` call (after `newDeviceDashboardCmd(),` at line 65):

```go
		newTopCmd(),
```

- [ ] **Step 5: Run the unit test (Task 8 supplies runTopDashboard for the full build)**

Run: `cd go && go test ./internal/cli/commands/ -run TestBuildTopJSON 2>&1 | head`
Expected: PASS. (Full `go build ./cmd/wendy` is verified at the end of Task 8.)

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/device_top.go go/internal/cli/commands/device.go go/internal/cli/commands/device_top_test.go
git commit -m "cli: add device top command with one-shot snapshot and JSON"
```

---

### Task 8: CLI — live TUI dashboard

**Files:**
- Modify: `go/internal/cli/commands/device_top.go` (add `runTopDashboard` + bubbletea model)

**Interfaces:**
- Consumes: `tui.BubbleTable` (`tui/table.go`), `tui.CropANSIView`, bubbletea, helpers from Tasks 6–7, the dashboard patterns in `apps_dashboard.go` (poll goroutine + channel waiter + `dashDimStyle`/`dashMetricVal` styles defined there in `package commands`).
- Produces: `func runTopDashboard(ctx context.Context, conn *grpcclient.AgentConnection, interval time.Duration) error` and an unexported `topModel` implementing `tea.Model`.

- [ ] **Step 1: Implement the model and runner**

Append to `go/internal/cli/commands/device_top.go` (add `strings`, `bubbleTable "github.com/charmbracelet/bubbles/table"`, `tea "github.com/charmbracelet/bubbletea"`, `"github.com/wendylabsinc/wendy/go/internal/cli/tui"` to imports):

```go
type topStatsMsg struct {
	resp *agentpb.GetResourceStatsResponse
	err  error
}

type topContainersMsg struct {
	containers []*agentpb.AppContainer
	err        error
}

type topModel struct {
	conn     *grpcclient.AgentConnection
	ctx      context.Context
	interval time.Duration

	statsCh      chan topStatsMsg
	containersCh chan topContainersMsg

	prev, cur        topSample
	havePrev         bool
	cachedContainers []*agentpb.AppContainer

	table     tui.BubbleTable
	sortByCPU bool
	width     int
	height    int
	flash     string
}

func newTopModel(ctx context.Context, conn *grpcclient.AgentConnection, interval time.Duration) topModel {
	return topModel{
		conn:         conn,
		ctx:          ctx,
		interval:     interval,
		statsCh:      make(chan topStatsMsg, 2),
		containersCh: make(chan topContainersMsg, 2),
		table:        tui.NewBubbleTable(true, nil),
	}
}

func (m topModel) Init() tea.Cmd {
	go m.runStatsPoll()
	go m.runContainersPoll()
	return tea.Batch(waitForTopStats(m.statsCh), waitForTopContainers(m.containersCh))
}

func waitForTopStats(ch chan topStatsMsg) tea.Cmd {
	return func() tea.Msg { msg, ok := <-ch; if !ok { return nil }; return msg }
}
func waitForTopContainers(ch chan topContainersMsg) tea.Cmd {
	return func() tea.Msg { msg, ok := <-ch; if !ok { return nil }; return msg }
}

func (m topModel) runStatsPoll() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	fetch := func() bool {
		resp, err := sampleResourceStats(m.ctx, m.conn)
		select {
		case m.statsCh <- topStatsMsg{resp: resp, err: err}:
		case <-m.ctx.Done():
		}
		return err == nil
	}
	if !fetch() {
		return
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if !fetch() {
				return
			}
		}
	}
}

func (m topModel) runContainersPoll() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	fetch := func() {
		containers, err := listAppContainers(m.ctx, m.conn)
		select {
		case m.containersCh <- topContainersMsg{containers: containers, err: err}:
		case <-m.ctx.Done():
		}
	}
	fetch()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func (m *topModel) refreshTable() {
	cpuCount := uint32(1)
	if m.cur.host != nil && m.cur.host.GetCpuCount() > 0 {
		cpuCount = m.cur.host.GetCpuCount()
	}
	cpuByID := map[string]float64{}
	if m.havePrev {
		for id := range m.cur.containers {
			cpuByID[id] = containerCPUPercent(m.prev, m.cur, id, cpuCount)
		}
	}
	rows := buildTopRows(m.cachedContainers, cpuByID, m.cur.mem, m.sortByCPU)

	cols := []bubbleTable.Column{
		{Title: "", Width: 2},
		{Title: "App", Width: 30},
		{Title: "CPU%", Width: 8},
		{Title: "MEM", Width: 10},
	}
	trows := make([]bubbleTable.Row, len(rows))
	for i, r := range rows {
		icon := " "
		if !r.isSubrow {
			icon = "●"
		}
		cpu := "—"
		if r.hasCPU && m.havePrev {
			cpu = fmt.Sprintf("%.1f", r.cpuPercent)
		}
		trows[i] = bubbleTable.Row{icon, r.displayName, cpu, formatBytes(r.memBytes)}
	}
	m.table.SetColumns(cols)
	m.table.SetRows(trows)
	if m.height > 0 {
		tableH := m.height - 7
		if tableH < 1 {
			tableH = 1
		}
		m.table.SetHeight(min(len(trows)+1, tableH))
	}
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		m.refreshTable()
		return m, cmd

	case topStatsMsg:
		if msg.err != nil {
			m.flash = userFacingGRPCError(msg.err)
			return m, waitForTopStats(m.statsCh)
		}
		if m.cur.host != nil || len(m.cur.containers) > 0 {
			m.prev = m.cur
			m.havePrev = true
		}
		m.cur = newTopSample(msg.resp, time.Now().UnixNano())
		m.refreshTable()
		return m, waitForTopStats(m.statsCh)

	case topContainersMsg:
		if msg.err == nil {
			m.cachedContainers = msg.containers
			m.refreshTable()
		}
		return m, waitForTopContainers(m.containersCh)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "c":
			m.sortByCPU = true
			m.refreshTable()
			return m, nil
		case "m":
			m.sortByCPU = false
			m.refreshTable()
			return m, nil
		default:
			var cmd tea.Cmd
			m.table, cmd = m.table.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m topModel) View() string {
	var sb strings.Builder
	// Header panel.
	if m.cur.host != nil {
		hostCPU := 0.0
		if m.havePrev {
			hostCPU = hostCPUPercent(m.prev, m.cur)
		}
		sb.WriteString(m.viewLine(fmt.Sprintf("  CPU %.1f%%   MEM %s / %s",
			hostCPU,
			formatBytes(m.cur.host.GetMemTotalBytes()-m.cur.host.GetMemAvailableBytes()),
			formatBytes(m.cur.host.GetMemTotalBytes()))) + "\n")
		for _, g := range m.cur.host.GetGpus() {
			line := fmt.Sprintf("  GPU%d %s  %.0f%%  %s / %s", g.GetIndex(), g.GetName(),
				g.GetUtilPercent(), formatBytes(g.GetMemUsedBytes()), formatBytes(g.GetMemTotalBytes()))
			if g.TempC != nil {
				line += fmt.Sprintf("  %.0f°C", *g.TempC)
			}
			sb.WriteString(m.viewLine(line) + "\n")
		}
		if len(m.cur.host.GetGpus()) == 0 {
			sb.WriteString(m.viewLine(dashDimStyle.Render("  No GPU detected")) + "\n")
		}
	}
	sortLabel := "mem"
	if m.sortByCPU {
		sortLabel = "cpu"
	}
	sb.WriteString(m.viewLine(dashDimStyle.Render(
		fmt.Sprintf("  ↑/↓ navigate  m sort by mem  c sort by cpu  [sort: %s]  q quit", sortLabel))) + "\n\n")
	if len(m.table.View()) == 0 {
		sb.WriteString(m.viewLine(dashDimStyle.Render("  Sampling…")) + "\n")
	} else {
		sb.WriteString(m.table.View() + "\n")
	}
	if m.flash != "" {
		sb.WriteString(m.viewLine(dashMetricVal.Render("  "+m.flash)) + "\n")
	}
	return sb.String()
}

func (m topModel) viewLine(line string) string {
	if m.width <= 0 {
		return line
	}
	return tui.CropANSIView(line, 0, m.width)
}

func runTopDashboard(ctx context.Context, conn *grpcclient.AgentConnection, interval time.Duration) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newTopModel(cctx, conn, interval)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
```

> Verify `dashDimStyle` and `dashMetricVal` exist in `package commands` (they are referenced by `apps_dashboard.go`'s `View`). If their definitions live in a file you haven't opened, `grep -rn "dashDimStyle\b" go/internal/cli/commands` to confirm; they are package-level and reusable as-is.

- [ ] **Step 2: Build the CLI**

Run: `cd go && go build ./cmd/wendy 2>&1 | head`
Expected: builds clean.

- [ ] **Step 3: Run the full commands-package test suite**

Run: `cd go && go test ./internal/cli/commands/ -count=1 2>&1 | tail -20`
Expected: PASS (no regressions; new tests pass).

- [ ] **Step 4: Manual smoke check (help + non-TTY snapshot)**

Run: `cd go && go run ./cmd/wendy device top --help 2>&1 | head`
Expected: shows the `top` command usage with the `--interval` flag.
Run (against a reachable device, piped so it takes the snapshot path): `go run ./cmd/wendy device top --json | head` (or `| cat`).
Expected: a JSON object with `host` and `containers`, or — against an old agent — the error "the device's agent does not support resource stats…". Both are acceptable outcomes for this step.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/device_top.go
git commit -m "cli: add live TUI for device top (host panel + sortable container table)"
```

---

## Self-Review

**1. Spec coverage:**
- Per-container CPU% — Tasks 4 (nanos) + 6 (delta math) + 8 (display). ✓
- Host CPU/RAM — Tasks 2 + 5 + 6 + 8. ✓
- Host GPU panel — Tasks 3 + 5 + 8. ✓
- Client-side CPU% deltas (Option A) — Task 6 helpers, agent stays stateless. ✓
- New v1 `GetResourceStats` RPC — Task 1. ✓
- Shares dashboard plumbing (BubbleTable, poll/channel pattern, formatBytes, app-grouping shape) — Tasks 6 + 8 reuse `tui.BubbleTable`, `dashDimStyle`, the poll/waiter pattern. ✓
- One-shot snapshot / `--json` on non-TTY — Task 7. ✓
- Default sort memory descending — Tasks 6 (`sortByCPU=false` default) + 8. ✓
- Graceful `Unimplemented` — Task 7 (`errResourceStatsUnimplemented`) + Task 8 (flash). ✓
- Testing: parsers, delta math, grouping, JSON shape — Tasks 2,3,6,7. ✓
- Out of scope honored: no per-container GPU, no sparklines, no PID tree. ✓
- Deviations (no `service` proto field; no Swift impl) documented at top. ✓ — **flag these to the user at handoff.**

**2. Placeholder scan:** No TBD/TODO. The one soft spot — `listAppContainers` EOF handling — is called out with the exact fix (`io.EOF`, mirroring `apps_dashboard.go:302`); the implementer must add the `io` import and use `err == io.EOF`.

**3. Type consistency:** `topSample`, `topRow`, `buildTopRows(containers, cpuByID, memByID, sortByCPU)`, `hostCPUPercent`, `containerCPUPercent`, `newTopSample`, `sampleResourceStats`, `listAppContainers`, `runTopSnapshot`, `runTopDashboard`, `newTopCmd` are used consistently across Tasks 6–8. Proto accessors (`GetCpuTotalJiffies`, `GetCpuIdleJiffies`, `GetCpuCount`, `GetMemAvailableBytes`, `GetCpuUsageNanos`, `GetGpus`, `TempC`/`PowerW` as `*float64` optionals) match the proto field types in Task 1.
```
