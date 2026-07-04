package cdi

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/wendylabsinc/wendy/go/internal/agent/oci"
)

// l4tCSVDir is the directory the NVIDIA Container Runtime "CSV mode" uses to
// list the host driver files and device nodes that must be exposed to a
// container on Tegra/L4T (Jetson). Behind a var so tests can point it at a
// tempdir.
var l4tCSVDir = "/etc/nvidia-container-runtime/host-files-for-container.d"

// statDevice returns the major/minor of the device node at p. Behind a var so
// tests can inject device numbers without creating real device nodes (which
// requires root + mknod). Mirrors the dev_t decoding used in the oci package.
var statDevice = func(p string) (major, minor int64, err error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, 0, err
	}
	rdev := uint64(st.Rdev)
	return int64(unix.Major(rdev)), int64(unix.Minor(rdev)), nil
}

type csvEntryKind string

const (
	csvLib csvEntryKind = "lib"
	csvSym csvEntryKind = "sym"
	csvDir csvEntryKind = "dir"
	csvDev csvEntryKind = "dev"
)

type l4tCSVEntry struct {
	kind csvEntryKind
	path string
}

// ApplyL4TCSV provisions Tegra/L4T GPU access on a Jetson by translating the
// NVIDIA Container Runtime "CSV mode" file lists
// (/etc/nvidia-container-runtime/host-files-for-container.d/*.csv) into OCI
// mounts and device nodes.
//
// It is the fallback used when no nvidia-ctk-generated CDI spec is available —
// e.g. on JetPack 5 (L4T r35) devices whose nvidia-container-toolkit (≤1.11)
// predates `nvidia-ctk cdi generate`. Without it the container's 0-byte
// libcuda.so.1 stub is never overlaid with the host driver and the Tegra iGPU
// device nodes (/dev/nvmap, /dev/nvhost-*, /dev/nvgpu/igpu0/*) are missing, so
// CUDA is unavailable inside the container (WDY-1716).
//
// CSV entries:
//   - lib / sym / dir → read-only bind-mount the host path at the same path in
//     the container, overlaying any stub the image ships.
//   - dev → add the device node (mknod'd by runc) plus a cgroup allow rule.
//
// Missing sources are skipped (never fatal) so a stale CSV entry cannot stop a
// container from starting. Entries whose destination is already present in the
// spec are skipped to avoid duplicates. Returns the number of mounts + device
// nodes applied.
func ApplyL4TCSV(spec *oci.Spec) (int, error) {
	entries, err := loadL4TCSVEntries(l4tCSVDir)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	if spec.Linux == nil {
		spec.Linux = &oci.Linux{}
	}
	if spec.Linux.Resources == nil {
		spec.Linux.Resources = &oci.LinuxResources{}
	}

	existingMounts := make(map[string]bool, len(spec.Mounts))
	for _, m := range spec.Mounts {
		existingMounts[m.Destination] = true
	}
	existingDevs := make(map[string]bool, len(spec.Linux.Devices))
	for _, d := range spec.Linux.Devices {
		existingDevs[d.Path] = true
	}

	applied := 0
	for _, e := range entries {
		switch e.kind {
		case csvLib, csvSym, csvDir:
			// Bind-mount the host file/dir read-only at the same path. ro +
			// nosuid + nodev keep it least-privilege; noexec is deliberately NOT
			// set — shared libraries must be mmap'd executable by the loader.
			// Skip missing sources so a stale entry can't block container start.
			if _, err := os.Lstat(e.path); err != nil {
				continue
			}
			if existingMounts[e.path] {
				continue
			}
			existingMounts[e.path] = true
			spec.Mounts = append(spec.Mounts, oci.Mount{
				Destination: e.path,
				Source:      e.path,
				Type:        "bind",
				Options:     []string{"rbind", "ro", "nosuid", "nodev"},
			})
			applied++
		case csvDev:
			if existingDevs[e.path] {
				continue
			}
			major, minor, err := statDevice(e.path)
			if err != nil {
				continue
			}
			existingDevs[e.path] = true
			spec.Linux.Devices = append(spec.Linux.Devices, oci.LinuxDevice{
				Path:  e.path,
				Type:  "c",
				Major: major,
				Minor: minor,
			})
			// "rw", not "rwm": runc creates the node above, so the container only
			// opens it and the mknod bit is withheld as least privilege (matches
			// the gpu/camera entitlement convention).
			mj, mn := major, minor
			spec.Linux.Resources.Devices = append(spec.Linux.Resources.Devices, oci.LinuxDeviceCgroup{
				Allow:  true,
				Type:   "c",
				Major:  &mj,
				Minor:  &mn,
				Access: "rw",
			})
			applied++
		}
	}

	return applied, nil
}

// loadL4TCSVEntries reads every *.csv under dir and returns the parsed,
// de-duplicated entries. Each line is "<kind>, <path>" where kind is one of
// lib/sym/dir/dev; blank lines, comments, malformed lines, and unknown kinds
// are skipped. Unreadable files are skipped rather than failing the whole load.
func loadL4TCSVEntries(dir string) ([]l4tCSVEntry, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.csv"))
	if err != nil {
		return nil, err
	}

	var entries []l4tCSVEntry
	seen := make(map[string]bool)
	for _, f := range matches {
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, ",", 2)
			if len(parts) != 2 {
				continue
			}
			kind := csvEntryKind(strings.ToLower(strings.TrimSpace(parts[0])))
			switch kind {
			case csvLib, csvSym, csvDir, csvDev:
			default:
				continue
			}
			path := strings.TrimSpace(parts[1])
			if path == "" {
				continue
			}
			key := string(kind) + "\x00" + path
			if seen[key] {
				continue
			}
			seen[key] = true
			entries = append(entries, l4tCSVEntry{kind: kind, path: path})
		}
		file.Close()
	}
	return entries, nil
}
