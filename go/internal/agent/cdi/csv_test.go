package cdi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/oci"
)

// writeCSV creates dir and writes a single csv file with the given content,
// then points l4tCSVDir at dir for the duration of the test.
func useCSVDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "l4t.csv"), []byte(content), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	orig := l4tCSVDir
	l4tCSVDir = dir
	t.Cleanup(func() { l4tCSVDir = orig })
	return dir
}

func mountForDest(spec *oci.Spec, dest string) (oci.Mount, bool) {
	for _, m := range spec.Mounts {
		if m.Destination == dest {
			return m, true
		}
	}
	return oci.Mount{}, false
}

func deviceForPath(spec *oci.Spec, path string) (oci.LinuxDevice, bool) {
	for _, d := range spec.Linux.Devices {
		if d.Path == path {
			return d, true
		}
	}
	return oci.LinuxDevice{}, false
}

func hasCgroupRule(spec *oci.Spec, major, minor int64) bool {
	for _, r := range spec.Linux.Resources.Devices {
		if r.Major != nil && r.Minor != nil && *r.Major == major && *r.Minor == minor && r.Allow {
			return true
		}
	}
	return false
}

func newSpec() *oci.Spec {
	return &oci.Spec{Linux: &oci.Linux{Resources: &oci.LinuxResources{}}}
}

func TestApplyL4TCSV_MountsLibsAndDevices(t *testing.T) {
	// Real lib file + dir on disk so the bind-mount sources exist; a missing lib
	// that must be skipped; and two device entries (one resolvable, one not).
	libDir := t.TempDir()
	libFile := filepath.Join(libDir, "libcuda.so.1")
	if err := os.WriteFile(libFile, []byte("ELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	fwDir := filepath.Join(libDir, "firmware")
	if err := os.MkdirAll(fwDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missingLib := filepath.Join(libDir, "nope.so")

	content := "# comment line\n" +
		"\n" +
		"lib, " + libFile + "\n" +
		"sym, " + libFile + "\n" + // duplicate path, different kind: still one mount dest
		"dir, " + fwDir + "\n" +
		"lib, " + missingLib + "\n" + // missing source -> skipped
		"dev, /dev/nvmap\n" +
		"dev, /dev/missing\n" + // statDevice errors -> skipped
		"garbage line without comma\n" +
		"weird, /tmp/x\n" // unknown kind -> skipped

	useCSVDir(t, content)

	orig := statDevice
	statDevice = func(p string) (int64, int64, error) {
		if p == "/dev/nvmap" {
			return 10, 55, nil
		}
		return 0, 0, os.ErrNotExist
	}
	t.Cleanup(func() { statDevice = orig })

	spec := newSpec()
	applied, err := ApplyL4TCSV(spec)
	if err != nil {
		t.Fatalf("ApplyL4TCSV: %v", err)
	}

	// libFile (once, deduped across lib+sym), fwDir, /dev/nvmap = 3 applied.
	if applied != 3 {
		t.Errorf("applied = %d, want 3", applied)
	}

	if m, ok := mountForDest(spec, libFile); !ok {
		t.Error("libcuda mount missing")
	} else if m.Source != libFile || m.Type != "bind" {
		t.Errorf("libcuda mount = %+v", m)
	}
	if _, ok := mountForDest(spec, fwDir); !ok {
		t.Error("firmware dir mount missing")
	}
	if _, ok := mountForDest(spec, missingLib); ok {
		t.Error("missing lib should not be mounted")
	}

	if d, ok := deviceForPath(spec, "/dev/nvmap"); !ok {
		t.Error("/dev/nvmap device node missing")
	} else if d.Major != 10 || d.Minor != 55 || d.Type != "c" {
		t.Errorf("/dev/nvmap device = %+v", d)
	}
	if !hasCgroupRule(spec, 10, 55) {
		t.Error("/dev/nvmap cgroup rule missing")
	}
	if _, ok := deviceForPath(spec, "/dev/missing"); ok {
		t.Error("unresolvable device should be skipped")
	}

	// Lib mounts must remain executable-capable: no "noexec".
	if m, _ := mountForDest(spec, libFile); contains(m.Options, "noexec") {
		t.Error("lib mount must not set noexec (loader needs PROT_EXEC mmap)")
	}
}

func TestApplyL4TCSV_NoFiles(t *testing.T) {
	orig := l4tCSVDir
	l4tCSVDir = t.TempDir() // empty dir, no *.csv
	t.Cleanup(func() { l4tCSVDir = orig })

	spec := newSpec()
	applied, err := ApplyL4TCSV(spec)
	if err != nil {
		t.Fatalf("ApplyL4TCSV: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 when no CSV files present", applied)
	}
}

func TestApplyL4TCSV_SkipsDuplicateOfExistingSpecEntry(t *testing.T) {
	libDir := t.TempDir()
	libFile := filepath.Join(libDir, "lib.so")
	if err := os.WriteFile(libFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	useCSVDir(t, "lib, "+libFile+"\n")

	spec := newSpec()
	// Pre-seed the same destination (e.g. another entitlement already mounted it).
	spec.Mounts = append(spec.Mounts, oci.Mount{Destination: libFile, Source: "/other"})

	applied, err := ApplyL4TCSV(spec)
	if err != nil {
		t.Fatalf("ApplyL4TCSV: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (dest already present)", applied)
	}
	var count int
	for _, m := range spec.Mounts {
		if m.Destination == libFile {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate mount destination created: %d entries", count)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
