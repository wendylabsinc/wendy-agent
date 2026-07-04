package hoststats

import (
	"context"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxGPUToolOutput caps how much stdout we read from nvidia-smi/tegrastats. The
// real tools emit a few hundred bytes; the cap bounds agent memory if a rogue or
// misbehaving binary ahead of the real one on $PATH streams unbounded output.
const maxGPUToolOutput = 64 << 10 // 64 KiB

// maxGPUNameLen bounds the GPU name stored from tool output so a pathological
// name cannot corrupt downstream proto/TUI rendering.
const maxGPUNameLen = 64

// GPUStat is a single GPU's instantaneous utilization snapshot.
// Mem fields are zero when the sampler cannot report per-GPU memory — e.g.
// Jetson unified memory, where nvidia-smi answers "[N/A]" because the GPU
// shares host RAM. A real GPU never has 0 bytes of total memory, so zero
// safely doubles as "not applicable".
// REFACTOR: if presence ever needs to be explicit, make GpuStats'
// mem_used_bytes/mem_total_bytes `optional` next time the container-service
// proto is touched, and thread *int64 through here.
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
//
//	nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw
//	           --format=csv,noheader,nounits
//
// Memory fields are MiB. Missing/[N/A] numeric fields are left zero, which
// renderers treat as "not applicable" (unified memory), never as a real size.
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
		name := f[1]
		if len(name) > maxGPUNameLen {
			name = name[:maxGPUNameLen]
		}
		g := GPUStat{Name: name}
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
	tegraGR3DRe = regexp.MustCompile(`GR3D_FREQ (\d+)%`)
	// JetPack ≤6 prints "GPU@48C"; JetPack 7 (Thor) prints "gpu@62.906C".
	tegraGPUTemp = regexp.MustCompile(`(?i)\bGPU@([\d.]+)C`)
	// JetPack ≤6 names the GPU power rail VDD_GPU_SOC; JetPack 7 (Thor)
	// renamed it to VDD_GPU. The CPU rail (VDD_CPU_SOC_MSS) matches neither.
	tegraGPUPwr = regexp.MustCompile(`\bVDD_GPU(?:_SOC)? (\d+)mW`)
)

// ParseTegrastats extracts the integrated-GPU utilization (GR3D_FREQ) and, when
// present, GPU temperature and power from a single tegrastats line. Jetson uses
// unified memory, so per-GPU memory is left zero (not applicable).
//
// JetPack 7 (Thor) tegrastats has no GR3D_FREQ field at all, so utilization is
// NOT a gate: a line with only GPU temperature/power still yields an entry
// (utilization reads 0 — the price of a degraded fallback). Returns no entries
// only when the line has none of the GPU fields.
func ParseTegrastats(line string) []GPUStat {
	g := GPUStat{Name: "Integrated GPU"}
	found := false
	if m := tegraGR3DRe.FindStringSubmatch(line); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			g.UtilPercent = v
			found = true
		}
	}
	if t := tegraGPUTemp.FindStringSubmatch(line); t != nil {
		if v, err := strconv.ParseFloat(t[1], 64); err == nil {
			g.TempC = &v
			found = true
		}
	}
	if p := tegraGPUPwr.FindStringSubmatch(line); p != nil {
		if mw, err := strconv.ParseFloat(p[1], 64); err == nil {
			w := mw / 1000.0
			g.PowerW = &w
			found = true
		}
	}
	if !found {
		return nil
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
		out, err := runBounded(exec.CommandContext(cctx, path,
			"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
			"--format=csv,noheader,nounits"))
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
		out, _ := runBounded(exec.CommandContext(cctx, path, "--interval", "500", "--count", "1"))
		if line := strings.TrimSpace(string(out)); line != "" {
			return ParseTegrastats(line)
		}
	}
	return nil
}

// runBounded starts cmd and returns up to maxGPUToolOutput bytes of its stdout.
// It mirrors (*Cmd).Output but reads through an io.LimitReader so a subprocess
// cannot stream unbounded data into agent memory; the command's context deadline
// still bounds wall-clock time.
func runBounded(cmd *exec.Cmd) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxGPUToolOutput))
	waitErr := cmd.Wait()
	if readErr != nil {
		return data, readErr
	}
	return data, waitErr
}
