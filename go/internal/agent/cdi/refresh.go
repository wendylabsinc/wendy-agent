package cdi

import (
	"fmt"
	"sort"
	"syscall"

	"github.com/wendylabsinc/wendy/go/internal/agent/oci"
)

// DeviceRefresh reports what RefreshDeviceNumbers found.
type DeviceRefresh struct {
	// Updated names the device paths whose major/minor pair in the spec no
	// longer matched the host and was rewritten.
	Updated []string
	// Missing names the device paths in the spec that no longer exist on the
	// host at all. Their numbers are left as they are: there is nothing to
	// re-resolve them to, and a device that is genuinely gone is a different
	// problem from one that moved.
	Missing []string
}

// Changed reports whether the spec was modified.
func (r DeviceRefresh) Changed() bool { return len(r.Updated) > 0 }

// RefreshDeviceNumbers re-stats every device node named by an already-built OCI
// spec and rewrites any major/minor pair that no longer matches the host,
// keeping the matching cgroup allow rule in step.
//
// Why this is needed at all: the numbers in a container's spec are a snapshot
// of the host taken when the container was created. Most of the device numbers
// a Jetson hands out for its GPU are allocated dynamically by the kernel at
// module load, so they are stable for a boot rather than forever. A container
// definition outlives a boot — containerd persists it and only the task is
// recreated — so a spec created on one boot can name a device number the
// running kernel has since given to something else, or to nothing. From inside
// the container that is indistinguishable from having no device: on an NVIDIA
// box, CUDA reports "no device" while the host is perfectly healthy.
//
// Re-resolving at start turns that from a state only a reboot could clear into
// one an ordinary restart fixes.
//
// The lookup is keyed on the container-side path, which is the host path for
// every device CDI injects (nvidia-ctk emits identical container and host
// paths). A path that is not a device node on this host is reported in Missing
// and left untouched, so a spec built for another machine is never silently
// rewritten to point at a local device that happens to share its path.
func RefreshDeviceNumbers(spec *oci.Spec) (DeviceRefresh, error) {
	var out DeviceRefresh
	if spec == nil || spec.Linux == nil {
		return out, nil
	}

	// Old pair -> new pair, so the cgroup rules can be rewritten to match. The
	// cgroup list is keyed by numbers rather than by path, so it can only be
	// re-aligned through the numbers the device list used to carry.
	type devKey struct {
		typ          string
		major, minor int64
	}
	remap := make(map[devKey]devKey)

	for i := range spec.Linux.Devices {
		dev := &spec.Linux.Devices[i]

		var st syscall.Stat_t
		if err := syscall.Stat(dev.Path, &st); err != nil {
			out.Missing = append(out.Missing, dev.Path)
			continue
		}
		// A regular file at a device path is not a device that moved; treat it
		// the same as absent rather than deriving numbers from its rdev (which
		// is 0 for a regular file, i.e. the exact placeholder this package
		// exists to stop producing).
		if st.Mode&syscall.S_IFMT != syscall.S_IFCHR && st.Mode&syscall.S_IFMT != syscall.S_IFBLK {
			out.Missing = append(out.Missing, dev.Path)
			continue
		}

		major, minor := deviceNumbersFromRdev(uint64(st.Rdev))
		if int64(major) == dev.Major && int64(minor) == dev.Minor {
			continue
		}

		remap[devKey{typ: dev.Type, major: dev.Major, minor: dev.Minor}] = devKey{
			typ:   dev.Type,
			major: int64(major),
			minor: int64(minor),
		}
		out.Updated = append(out.Updated, fmt.Sprintf("%s (%d:%d -> %d:%d)", dev.Path, dev.Major, dev.Minor, major, minor))
		dev.Major = int64(major)
		dev.Minor = int64(minor)
	}

	if len(remap) > 0 && spec.Linux.Resources != nil {
		for i := range spec.Linux.Resources.Devices {
			rule := &spec.Linux.Resources.Devices[i]
			if rule.Major == nil || rule.Minor == nil {
				// A wildcard rule ("allow all char devices") needs no fixing.
				continue
			}
			to, ok := remap[devKey{typ: rule.Type, major: *rule.Major, minor: *rule.Minor}]
			if !ok {
				continue
			}
			major, minor := to.major, to.minor
			rule.Major = &major
			rule.Minor = &minor
		}
	}

	// Stable output so log lines and tests do not depend on map iteration.
	sort.Strings(out.Updated)
	sort.Strings(out.Missing)
	return out, nil
}
