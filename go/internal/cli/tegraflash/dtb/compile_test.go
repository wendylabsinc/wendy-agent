package dtb_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/dtb"
)

// minimalDTS is a device-tree source that uses a cpp conditional: when FLAG is
// defined the root node carries ok = <1>, otherwise ok = <0>.
const minimalDTS = `/dts-v1/;
#ifdef FLAG
/ { ok = <1>; };
#else
/ { ok = <0>; };
#endif
`

func TestCompileDTS(t *testing.T) {
	if _, err := exec.LookPath("dtc"); err != nil {
		t.Skip("dtc not installed")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "x.dts")

	if err := os.WriteFile(src, []byte(minimalDTS), 0o644); err != nil {
		t.Fatalf("write DTS: %v", err)
	}

	out, err := dtb.Compile(dtb.CompileOptions{
		DTSPath: src,
		OutDir:  dir,
		Defines: []string{"FLAG"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out) != "x_cpp.dtb" {
		t.Errorf("expected output base name x_cpp.dtb, got %s", filepath.Base(out))
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("dtb not produced: %v", err)
	}

	// Verify that the FLAG define actually took effect by compiling again
	// without it and observing a different DTB binary.
	outNoFlag, err := dtb.Compile(dtb.CompileOptions{
		DTSPath: src,
		OutDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("compile without FLAG: %v", err)
	}

	withFlag, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read dtb with FLAG: %v", err)
	}
	withoutFlag, err := os.ReadFile(outNoFlag)
	if err != nil {
		t.Fatalf("read dtb without FLAG: %v", err)
	}
	if string(withFlag) == string(withoutFlag) {
		t.Error("expected DTB compiled with FLAG to differ from DTB compiled without FLAG, but they are identical")
	}
}
