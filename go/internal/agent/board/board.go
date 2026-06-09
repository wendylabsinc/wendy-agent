// Package board centralizes detection of the physical SBC (Jetson, Raspberry
// Pi, or Generic) the agent is running on. Other packages that need to gate
// platform-specific behavior should call Detect() instead of replicating the
// underlying file-system probes.
//
// Existing inline call sites in agent_service.go and cdi/generate.go are not
// migrated by this package's introduction; that consolidation is a separate
// follow-up.
package board

import (
	"os"
	"strings"
	"sync"
)

// Kind identifies the host SBC.
type Kind int

const (
	Generic Kind = iota
	Jetson
	RaspberryPi
)

// Info bundles the detected board kind with any descriptive strings that
// happen to be cheap to read at detection time.
type Info struct {
	Kind      Kind
	Model     string // /proc/device-tree/model when available
	SoCFamily string // /sys/devices/soc0/family when available
}

// IsJetson reports whether the host is an NVIDIA Jetson.
func (i Info) IsJetson() bool { return i.Kind == Jetson }

// IsRaspberryPi reports whether the host is a Raspberry Pi.
func (i Info) IsRaspberryPi() bool { return i.Kind == RaspberryPi }

// Detection paths are package vars so tests can substitute them.
var (
	tegraReleasePath    = "/etc/nv_tegra_release"
	socFamilyPath       = "/sys/devices/soc0/family"
	deviceTreeModelPath = "/proc/device-tree/model"

	cached Info
	once   sync.Once
)

// Detect returns the host board info. The first call probes the filesystem;
// subsequent calls return the cached result. The result does not change at
// runtime.
func Detect() Info {
	once.Do(func() {
		cached = detect()
	})
	return cached
}

// resetForTest clears the cache. Test-only; not exported. Tests that call this
// must not run Detect() concurrently with the reset: replacing once is not
// synchronized, by design — detection is a cheap, deterministic filesystem
// probe, so tests reset-then-detect sequentially.
func resetForTest() {
	once = sync.Once{}
	cached = Info{}
}

func detect() Info {
	var info Info
	if b, err := os.ReadFile(deviceTreeModelPath); err == nil {
		info.Model = strings.TrimRight(strings.TrimSpace(string(b)), "\x00")
	}
	if b, err := os.ReadFile(socFamilyPath); err == nil {
		info.SoCFamily = strings.TrimSpace(string(b))
	}
	if _, err := os.Stat(tegraReleasePath); err == nil {
		info.Kind = Jetson
		return info
	}
	if strings.Contains(strings.ToLower(info.SoCFamily), "tegra") {
		info.Kind = Jetson
		return info
	}
	if strings.Contains(info.Model, "Raspberry Pi") {
		info.Kind = RaspberryPi
		return info
	}
	info.Kind = Generic
	return info
}
