package oci

import (
	"fmt"
	"os"
)

// ociNSTypeToProcName maps OCI spec namespace type strings to the Linux kernel
// name used in /proc/{pid}/ns/. They differ for "network" (→ "net") and
// "mount" (→ "mnt"); the others match directly.
var ociNSTypeToProcName = map[string]string{
	"ipc":     "ipc",
	"mount":   "mnt",
	"network": "net",
	"pid":     "pid",
	"user":    "user",
	"uts":     "uts",
}

// JoinGroupNamespaces modifies spec so the container joins the Linux namespaces
// owned by the primary container (identified by primaryPID).
//
// isolation controls which namespaces are shared:
//   - "shared-network": joins network and uts namespaces
//   - "shared-ipc":     also joins ipc namespace (enables /dev/shm sharing for ROS2/dora-rs)
//   - anything else:    no-op
//
// It returns a set of open file descriptors whose paths are embedded in the
// spec as /proc/self/fd/{n}. The caller MUST keep these fds open until the
// OCI runtime (runc) has consumed the spec and started the container, then
// close them. Holding the fds prevents the kernel from recycling the
// underlying namespace when the primary container exits between the Lstat
// check and runc opening the path — eliminating the TOCTOU PID-reuse window
// (SOC2-CC6, ISO27001-A.8, NIST-SC-7, NIST-SI-16).
func JoinGroupNamespaces(spec *Spec, primaryPID uint32, isolation string) ([]*os.File, error) {
	if spec.Linux == nil {
		return nil, fmt.Errorf("JoinGroupNamespaces: spec.Linux is nil")
	}
	if primaryPID == 0 {
		return nil, fmt.Errorf("JoinGroupNamespaces: primaryPID must be non-zero")
	}

	join := map[string]bool{}
	switch isolation {
	case "shared-ipc":
		join["ipc"] = true
		join["network"] = true
		join["uts"] = true
	case "shared-network":
		join["network"] = true
		join["uts"] = true
	default:
		return nil, nil
	}

	var anchors []*os.File
	for i, ns := range spec.Linux.Namespaces {
		if join[ns.Type] {
			kernelName, ok := ociNSTypeToProcName[ns.Type]
			if !ok {
				for _, f := range anchors {
					f.Close()
				}
				return nil, fmt.Errorf("JoinGroupNamespaces: unknown OCI namespace type %q", ns.Type)
			}
			nsPath := fmt.Sprintf("/proc/%d/ns/%s", primaryPID, kernelName)
			// Open the namespace file to anchor it: the open fd prevents the
			// kernel from deallocating the namespace when the primary container
			// exits. We embed /proc/self/fd/{n} in the spec instead of the raw
			// procfs path so runc opens our fd-anchored reference, not the
			// (potentially recycled) PID path.
			f, err := os.Open(nsPath)
			if err != nil {
				for _, a := range anchors {
					a.Close()
				}
				return nil, fmt.Errorf("JoinGroupNamespaces: namespace path %q not available (primary container exited?): %w", nsPath, err)
			}
			anchors = append(anchors, f)
			spec.Linux.Namespaces[i].Path = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
		}
	}
	return anchors, nil
}

// SharedSHMMount returns a bind-mount that maps hostSHMPath into /dev/shm.
// Use this for shared-ipc isolation where all services share one shm segment.
// hostSHMPath should be /run/wendy/shm/{appID}.
//
// Threat model: all services in a shared-ipc group are explicitly mutually
// trusted for read-write /dev/shm access — they share IPC namespace by
// declaration and may communicate freely via POSIX shared memory.
// Isolation between services in the same shared-ipc group is out of scope.
//
// Mount options rationale:
//   - nosuid: prevents setuid executables placed in /dev/shm from escalating privilege
//   - nodev:  prevents device file creation
//   - noexec: prevents direct execve of files in /dev/shm by any service,
//     limiting the blast radius of a compromised sibling that places a
//     binary there (SOC2-CC6, NIST-AC-3, ISO27001-A.9)
//
// Note: noexec does not prevent mmap(PROT_EXEC) on shared memory segments
// used for DDS/ROS2 transport, which does not require file execution.
func SharedSHMMount(hostSHMPath string) Mount {
	return Mount{
		Destination: "/dev/shm",
		Type:        "bind",
		Source:      hostSHMPath,
		Options:     []string{"rbind", "rw", "nosuid", "nodev", "noexec"},
	}
}

// RemoveDefaultSHM removes the default per-container tmpfs /dev/shm mount from spec.
// Call this before adding a SharedSHMMount to avoid duplicate mounts.
func RemoveDefaultSHM(spec *Spec) {
	mounts := spec.Mounts[:0]
	for _, m := range spec.Mounts {
		if m.Destination != "/dev/shm" {
			mounts = append(mounts, m)
		}
	}
	spec.Mounts = mounts
}
