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
// The spec embeds raw /proc/{pid}/ns/{name} paths — runc consumes the spec
// in its own process, so agent-local /proc/self/fd paths would never resolve
// there. It returns a set of open file descriptors anchoring each namespace;
// the caller MUST keep them open until the OCI runtime (runc) has consumed
// the spec and started the container, then close them. The anchors prevent
// the kernel from deallocating the namespaces if the primary exits in that
// window; a vanished primary then surfaces as a clean ENOENT from runc.
// Callers MUST re-verify the primary's task (same PID, still running) after
// the secondary starts, closing the residual PID-recycling TOCTOU window
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
	// anchorNamespace opens the primary's namespace file (anchoring it against
	// deallocation) and returns the raw procfs path runc must embed. On any
	// error it closes every anchor opened so far and returns the error.
	//
	// The spec must embed the raw procfs path, NOT /proc/self/fd/{n}: the spec
	// is consumed by runc in a *different process*, where an agent-local fd
	// number can never resolve (runc would fail with "lstat /proc/self/fd/N:
	// no such file or directory"). With the raw path, a primary that exits
	// before runc opens it produces a clean ENOENT failure. The residual TOCTOU
	// (primary PID recycled between this check and runc's open) must be handled
	// by callers re-verifying the primary's task after the join (SOC2-CC6,
	// ISO27001-A.8, NIST-SC-7, NIST-SI-16).
	anchorNamespace := func(nsType string) (string, error) {
		kernelName, ok := ociNSTypeToProcName[nsType]
		if !ok {
			return "", fmt.Errorf("JoinGroupNamespaces: unknown OCI namespace type %q", nsType)
		}
		nsPath := fmt.Sprintf("/proc/%d/ns/%s", primaryPID, kernelName)
		f, err := os.Open(nsPath)
		if err != nil {
			return "", fmt.Errorf("JoinGroupNamespaces: namespace path %q not available (primary container exited?): %w", nsPath, err)
		}
		anchors = append(anchors, f)
		return nsPath, nil
	}
	closeAnchors := func() {
		for _, f := range anchors {
			f.Close()
		}
	}

	// Patch the namespaces already present in the spec, tracking which of the
	// requested types we've handled.
	joined := make(map[string]bool, len(join))
	for i, ns := range spec.Linux.Namespaces {
		if !join[ns.Type] {
			continue
		}
		nsPath, err := anchorNamespace(ns.Type)
		if err != nil {
			closeAnchors()
			return nil, err
		}
		spec.Linux.Namespaces[i].Path = nsPath
		joined[ns.Type] = true
	}

	// Add an entry for any requested namespace the base spec did not declare.
	// Without this a join would silently no-op for a missing type (e.g. a spec
	// built without a network namespace), leaving the container in a private
	// namespace instead of the primary's — the failure mode is invisible
	// because no error surfaces. Iterate ociNSTypeToProcName (stable set) and
	// filter by join for deterministic, gomap-order-independent behavior.
	for _, nsType := range []string{"ipc", "network", "uts", "pid", "user", "mount"} {
		if !join[nsType] || joined[nsType] {
			continue
		}
		nsPath, err := anchorNamespace(nsType)
		if err != nil {
			closeAnchors()
			return nil, err
		}
		spec.Linux.Namespaces = append(spec.Linux.Namespaces, LinuxNamespace{Type: nsType, Path: nsPath})
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
