package hostnetwork

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func meshRedirectFixture(t *testing.T) {
	t.Helper()
	requireIPTables(t)
	if err := InitMeshNATChain(); err != nil {
		t.Fatalf("InitMeshNATChain: %v", err)
	}
	t.Cleanup(func() {
		exec.Command("iptables", "-t", "nat", "-F", MeshChainName).Run()
		exec.Command("iptables", "-t", "nat", "-D", "PREROUTING", "-j", MeshChainName).Run()
		exec.Command("iptables", "-t", "nat", "-X", MeshChainName).Run()
	})
}

func meshRedirectCount(t *testing.T, containerIP, serviceCIDR string, proxyPort int) int {
	t.Helper()
	out, err := exec.Command("iptables", "-t", "nat", "-S", MeshChainName).CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -t nat -S %s failed: %v\n%s", MeshChainName, err, out)
	}
	want := fmt.Sprintf("-s %s/32 -d %s -p tcp -j REDIRECT --to-ports %d", containerIP, serviceCIDR, proxyPort)
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, want) {
			count++
		}
	}
	return count
}

func TestAddMeshRedirectCreatesRule(t *testing.T) {
	meshRedirectFixture(t)
	if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("AddMeshRedirect: %v", err)
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 1 {
		t.Fatalf("rule count = %d, want 1", got)
	}
}

func TestAddMeshRedirectIsIdempotent(t *testing.T) {
	meshRedirectFixture(t)
	for i := 0; i < 2; i++ {
		if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
			t.Fatalf("AddMeshRedirect #%d: %v", i+1, err)
		}
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 1 {
		t.Fatalf("rule count after double add = %d, want 1", got)
	}
}

func TestRemoveMeshRedirect(t *testing.T) {
	meshRedirectFixture(t)
	if err := AddMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("AddMeshRedirect: %v", err)
	}
	if err := RemoveMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("RemoveMeshRedirect: %v", err)
	}
	if got := meshRedirectCount(t, "10.88.0.7", "10.99.0.0/16", 50058); got != 0 {
		t.Fatalf("rule count after remove = %d, want 0", got)
	}
	// Removing again is success (idempotent).
	if err := RemoveMeshRedirect("10.88.0.7", "10.99.0.0/16", 50058); err != nil {
		t.Fatalf("second RemoveMeshRedirect: %v", err)
	}
}

func TestInitMeshNATChainIsIdempotent(t *testing.T) {
	meshRedirectFixture(t)
	if err := InitMeshNATChain(); err != nil {
		t.Fatalf("second InitMeshNATChain: %v", err)
	}
	out, err := exec.Command("iptables", "-t", "nat", "-S", "PREROUTING").CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -t nat -S PREROUTING: %v", err)
	}
	jumps := strings.Count(string(out), "-j "+MeshChainName)
	if jumps != 1 {
		t.Fatalf("PREROUTING jump count = %d, want 1", jumps)
	}
}
