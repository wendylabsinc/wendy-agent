package hostlocal

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
)

func testIPAMConf(t *testing.T, dataDir string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{
		"cniVersion": "1.0.0",
		"name": "wendy-mesh",
		"type": "bridge",
		"ipam": {
			"type": "host-local",
			"dataDir": "%s",
			"ranges": [[{ "subnet": "10.77.0.0/28" }]]
		}
	}`, dataDir))
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever was written to it. Used to prove Allocate has no print side
// effect — the exact behavior CmdAdd's exec-based use (host-local invoked as
// a subprocess) relies on, and that in-process callers like the bridge
// plugin must NOT trigger, since it would corrupt their own CNI result on
// their own stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestAllocate_NoStdoutSideEffect(t *testing.T) {
	dataDir := t.TempDir()
	args := &skel.CmdArgs{
		ContainerID: "test-container",
		IfName:      "eth0",
		StdinData:   testIPAMConf(t, dataDir),
	}

	var gotErr error
	stdout := captureStdout(t, func() {
		res, _, err := Allocate(args)
		gotErr = err
		if err == nil && (res == nil || len(res.IPs) == 0) {
			t.Error("Allocate() returned no error but no allocated IPs")
		}
	})
	if gotErr != nil {
		t.Fatalf("Allocate() error = %v", gotErr)
	}
	if stdout != "" {
		t.Fatalf("Allocate() wrote to stdout: %q — it must return the result, not print it, "+
			"so in-process callers (bridge) don't have their own CNI result corrupted", stdout)
	}

	// Clean up the lease so subsequent tests / a real CmdDel don't collide.
	if err := CmdDel(args); err != nil {
		t.Fatalf("cleanup CmdDel() error = %v", err)
	}
}

func TestAllocate_AllocatesFromConfiguredRange(t *testing.T) {
	dataDir := t.TempDir()
	args := &skel.CmdArgs{
		ContainerID: "test-container-2",
		IfName:      "eth0",
		StdinData:   testIPAMConf(t, dataDir),
	}

	res, confVersion, err := Allocate(args)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if confVersion != "1.0.0" {
		t.Errorf("confVersion = %q, want 1.0.0", confVersion)
	}
	if len(res.IPs) != 1 {
		t.Fatalf("len(res.IPs) = %d, want 1", len(res.IPs))
	}
	if res.IPs[0].Address.IP == nil {
		t.Fatalf("allocated IP is nil: %+v", res.IPs[0].Address)
	}

	t.Cleanup(func() { _ = CmdDel(args) })
}

// CmdAdd is the standalone-subprocess entry point (still dispatched to by
// cni_exec_linux.go); unlike Allocate, it MUST print — this is a regression
// guard on the split introduced when Allocate was extracted for in-process
// callers.
func TestCmdAdd_StillPrintsResult(t *testing.T) {
	dataDir := t.TempDir()
	args := &skel.CmdArgs{
		ContainerID: "test-container-3",
		IfName:      "eth0",
		StdinData:   testIPAMConf(t, dataDir),
	}

	var addErr error
	stdout := captureStdout(t, func() {
		addErr = CmdAdd(args)
	})
	if addErr != nil {
		t.Fatalf("CmdAdd() error = %v", addErr)
	}
	if stdout == "" {
		t.Fatal("CmdAdd() wrote nothing to stdout; the CNI plugin contract requires it to print its result")
	}

	t.Cleanup(func() { _ = CmdDel(args) })
}
