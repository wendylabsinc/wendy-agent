package hostnetwork

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// requireIPTables skips the test unless running as root with a usable
// iptables binary, since InitMeshChain manipulates real host netfilter state.
func requireIPTables(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root to manipulate iptables")
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		t.Skip("iptables binary not found in PATH")
	}
}

// forwardJumpCount returns how many "FORWARD ... -j WENDY-MESH" rules exist,
// by parsing `iptables -S FORWARD` output.
func forwardJumpCount(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("iptables", "-S", "FORWARD").CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -S FORWARD failed: %v\n%s", err, out)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "-j "+MeshChainName) {
			count++
		}
	}
	return count
}

// chainExists reports whether the WENDY-MESH chain exists in the filter table.
func chainExists(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("iptables", "-t", "filter", "-S", MeshChainName)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func TestInitMeshChain(t *testing.T) {
	requireIPTables(t)

	if err := InitMeshChain(); err != nil {
		t.Fatalf("InitMeshChain() first call returned error: %v", err)
	}

	if !chainExists(t) {
		t.Fatalf("expected chain %s to exist in filter table after InitMeshChain", MeshChainName)
	}

	if got := forwardJumpCount(t); got != 1 {
		t.Fatalf("expected exactly 1 FORWARD -> %s jump rule after first call, got %d", MeshChainName, got)
	}

	// Second call must be idempotent: no error, no duplicate jump rule.
	if err := InitMeshChain(); err != nil {
		t.Fatalf("InitMeshChain() second call returned error: %v", err)
	}

	if !chainExists(t) {
		t.Fatalf("expected chain %s to still exist in filter table after second InitMeshChain call", MeshChainName)
	}

	if got := forwardJumpCount(t); got != 1 {
		t.Fatalf("expected exactly 1 FORWARD -> %s jump rule after second call (idempotency), got %d", MeshChainName, got)
	}
}
