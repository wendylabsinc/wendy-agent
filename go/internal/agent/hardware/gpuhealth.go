package hardware

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// GPU driver states reported in the gpu capability's properties.
const (
	driverStatusResponding    = "responding"     // the driver answered
	driverStatusNotResponding = "not_responding" // nodes exist but the driver refused
	driverStatusAbsent        = "absent"         // no driver nodes to ask
)

// gpuProbeTimeout bounds the external query. A wedged driver can leave
// nvidia-smi hanging, and hardware discovery must not hang with it.
const gpuProbeTimeout = 3 * time.Second

// GPUDriverHealth is what a probe of the accelerator's driver found. It answers
// a different question from "does this board have a GPU": whether the driver is
// answering right now.
//
// The distinction matters because the presence signal is easy to mistake for a
// health signal and cannot serve as one. On a Jetson, "has GPU" is satisfied by
// a file on disk (/etc/nv_tegra_release) — true whether the driver is healthy,
// wedged, or not loaded. It has to stay that way: it is a board fact that feeds
// image builds (WENDY_HAS_GPU), so making it depend on the driver's mood would
// change how images are built based on a transient condition. This is the
// signal to read when asking whether the GPU works.
type GPUDriverHealth struct {
	Vendor string
	Status string
	// Probe names what was actually tried, so a "not_responding" verdict can be
	// argued with rather than taken on faith.
	Probe string
	// Detail carries the probe's own words on failure, truncated.
	Detail string
}

var computeCapRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

// nvidiaControlNodes are the NVIDIA control devices worth opening, discrete
// first then Tegra. Opening one read-only is the cheapest question that only a
// live driver can answer: stat sees a node the kernel left behind, open(2) goes
// to the driver.
var nvidiaControlNodes = []string{
	"/dev/nvidiactl",
	"/dev/nvhost-ctrl-gpu",
	"/dev/nvgpu/igpu0/ctrl",
}

// ProbeGPUDriver reports whether the accelerator driver on this host is
// answering. It never returns an error: an unanswerable probe is itself the
// answer, and hardware discovery must not fail because a GPU is unhappy.
func ProbeGPUDriver(ctx context.Context) (GPUDriverHealth, bool) {
	if h, ok := probeNVIDIA(ctx); ok {
		return h, true
	}
	if h, ok := probeAMD(); ok {
		return h, true
	}
	return GPUDriverHealth{}, false
}

func probeNVIDIA(ctx context.Context) (GPUDriverHealth, bool) {
	_, tegra := os.Stat("/etc/nv_tegra_release")
	nodes := existingNodes(nvidiaControlNodes)
	if tegra != nil && len(nodes) == 0 {
		return GPUDriverHealth{}, false
	}

	h := GPUDriverHealth{Vendor: "nvidia"}

	// nvidia-smi is the strongest signal available without linking CUDA: it
	// talks to the driver and comes back with a fact about the device.
	if smi, err := exec.LookPath("nvidia-smi"); err == nil {
		probeCtx, cancel := context.WithTimeout(ctx, gpuProbeTimeout)
		defer cancel()
		out, err := exec.CommandContext(probeCtx, smi, "--query-gpu=compute_cap", "--format=csv,noheader,nounits").Output()
		h.Probe = "nvidia-smi"
		if err == nil && computeCapRe.Match(out) {
			h.Status = driverStatusResponding
			return h, true
		}
		h.Status = driverStatusNotResponding
		h.Detail = probeDetail(err, out)
		return h, true
	}

	// No nvidia-smi (the common Jetson case): ask the driver by opening its
	// control node. A node the driver has abandoned still stats fine but fails
	// to open, which is exactly the state a stat-based check cannot see.
	if len(nodes) == 0 {
		return GPUDriverHealth{Vendor: "nvidia", Status: driverStatusAbsent, Probe: "device nodes"}, true
	}
	h.Probe = "open " + nodes[0]
	if err := openable(nodes[0]); err != nil {
		h.Status = driverStatusNotResponding
		h.Detail = truncate(err.Error())
		return h, true
	}
	h.Status = driverStatusResponding
	return h, true
}

func probeAMD() (GPUDriverHealth, bool) {
	const kfd = "/dev/kfd"
	if _, err := os.Stat(kfd); err != nil {
		return GPUDriverHealth{}, false
	}
	h := GPUDriverHealth{Vendor: "amd", Probe: "open " + kfd}
	if err := openable(kfd); err != nil {
		h.Status = driverStatusNotResponding
		h.Detail = truncate(err.Error())
		return h, true
	}
	h.Status = driverStatusResponding
	return h, true
}

// openable opens a device read-only and closes it immediately. O_NONBLOCK keeps
// a driver that would otherwise block the caller from hanging discovery.
func openable(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return syscall.Close(fd)
}

func existingNodes(paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func probeDetail(err error, out []byte) string {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return truncate(msg)
	}
	if err != nil {
		return truncate(err.Error())
	}
	return ""
}

// truncate keeps a driver's own words without letting a verbose failure become
// the whole response.
func truncate(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
