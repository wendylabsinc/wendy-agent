package containerd

import (
	"errors"
	"testing"
	"time"

	runtimespec "github.com/opencontainers/runtime-spec/specs-go"

	localoci "github.com/wendylabsinc/wendy/go/internal/agent/oci"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestNetworkIdentityFailClosed(t *testing.T) {
	base := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "bridge"}}
	a := networkIdentity("isolated", base)
	b := networkIdentity("isolated", append(base, appconfig.Entitlement{Type: appconfig.EntitlementGPIO, Pins: []int{17}}))
	c := networkIdentity("shared-network", base)
	if a == "" || len(a) != 64 {
		t.Fatalf("identity = %q", a)
	}
	if a == b {
		t.Fatal("entitlement change did not invalidate identity")
	}
	if a == c {
		t.Fatal("isolation change did not invalidate identity")
	}
	if got := networkIdentity("isolated", base); got != a {
		t.Fatalf("identity is not deterministic: %q != %q", got, a)
	}
}

func TestNetworkSandboxEligibilityIsConservative(t *testing.T) {
	bridge := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "bridge"}}
	mesh := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "mesh"}}
	if !networkSandboxEligible("", bridge) {
		t.Fatal("single-service bridge should be eligible")
	}
	if networkSandboxEligible("api", bridge) {
		t.Fatal("multi-service bridge must fail closed")
	}
	if networkSandboxEligible("", mesh) || networkSandboxEligible("", append(bridge, mesh...)) {
		t.Fatal("mesh networking must fail closed")
	}
	if networkSandboxEligible("", nil) {
		t.Fatal("host/default networking must fail closed")
	}
}

func TestNetworkIdentityFromLabelsRecomputesFingerprint(t *testing.T) {
	bridge := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "bridge"}}
	labels := wendyLabels("com.example.app", "", "1", nil, bridge, "isolated", nil)
	identity := networkIdentity("isolated", bridge)
	labels[labelKeyNetworkIdentity] = identity
	if got, ok := networkIdentityFromLabels(labels); !ok || got != identity {
		t.Fatalf("verified identity = %q, %v; want %q, true", got, ok, identity)
	}
	labels[labelKeyIsolation] = "shared-network"
	if _, ok := networkIdentityFromLabels(labels); ok {
		t.Fatal("stale stored fingerprint was trusted after isolation changed")
	}
	labels = wendyLabels("com.example.app", "api", "1", nil, bridge, "isolated", nil)
	labels[labelKeyNetworkIdentity] = networkIdentity("isolated", bridge)
	if _, ok := networkIdentityFromLabels(labels); ok {
		t.Fatal("multi-service labels were accepted for sandbox retention")
	}
}

func TestDesiredNetworkIdentityIgnoresPersistedFingerprint(t *testing.T) {
	bridge := []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "bridge"}}
	labels := wendyLabels("com.example.app", "", "1", nil, bridge, "isolated", nil)
	labels[labelKeyNetworkIdentity] = "attacker-controlled"
	want := networkIdentity("isolated", bridge)
	if got, ok := desiredNetworkIdentityFromLabels(labels); !ok || got != want {
		t.Fatalf("derived identity = %q, %v; want %q, true", got, ok, want)
	}
}

func TestNetworkSandboxChecksRequireCNICheck(t *testing.T) {
	if networkSandboxChecksPassed(true, errors.New("CHECK rejected prevResult")) {
		t.Fatal("healthy namespace was accepted after CNI CHECK failed")
	}
	if networkSandboxChecksPassed(false, nil) {
		t.Fatal("unhealthy namespace was accepted after CNI CHECK passed")
	}
	if !networkSandboxChecksPassed(true, nil) {
		t.Fatal("healthy namespace with successful CNI CHECK was rejected")
	}
}

func TestNetworkOperationLockSerializesSameContainer(t *testing.T) {
	c := &Client{networkOps: make(map[string]*networkOperation)}
	unlock := c.lockNetworkOperation("myapp")
	acquired := make(chan struct{})
	go func() {
		defer c.lockNetworkOperation("myapp")()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("second network operation entered concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second network operation did not proceed after unlock")
	}
	c.networkOpsMu.Lock()
	defer c.networkOpsMu.Unlock()
	if len(c.networkOps) != 0 {
		t.Fatalf("idle network lock entries leaked: %d", len(c.networkOps))
	}
}

func TestAdditionalNetworkOperationLocksSerializeGroupMembers(t *testing.T) {
	c := &Client{networkOps: make(map[string]*networkOperation)}
	unlockGroup := c.lockNetworkOperation("myapp")
	unlockMembers := c.lockAdditionalNetworkOperations("myapp", []string{"myapp_api", "myapp", "myapp_worker", "myapp_api"})

	acquired := make(chan string, 2)
	for _, containerID := range []string{"myapp_api", "myapp_worker"} {
		go func() {
			defer c.lockNetworkOperation(containerID)()
			acquired <- containerID
		}()
	}
	select {
	case containerID := <-acquired:
		t.Fatalf("member operation %q entered during group lock", containerID)
	case <-time.After(20 * time.Millisecond):
	}

	unlockMembers()
	for range 2 {
		select {
		case <-acquired:
		case <-time.After(time.Second):
			t.Fatal("member operation did not proceed after group lock release")
		}
	}
	unlockGroup()

	c.networkOpsMu.Lock()
	defer c.networkOpsMu.Unlock()
	if len(c.networkOps) != 0 {
		t.Fatalf("idle network lock entries leaked: %d", len(c.networkOps))
	}
}

func TestSpecJoinsNetworkSandbox(t *testing.T) {
	spec := &localoci.Spec{Linux: &localoci.Linux{Namespaces: []localoci.LinuxNamespace{
		{Type: "network", Path: "/run/wendy/netns/myapp"},
	}}}
	if !specJoinsNetworkSandbox(spec, "/run/wendy/netns/myapp") {
		t.Fatal("matching network namespace was not recognised")
	}
	if specJoinsNetworkSandbox(spec, "/run/wendy/netns/other") {
		t.Fatal("mismatched network namespace was accepted")
	}
}

func TestRuntimeSpecDetectsInvalidPersistedNetworkPath(t *testing.T) {
	spec := &runtimespec.Spec{Linux: &runtimespec.Linux{Namespaces: []runtimespec.LinuxNamespace{
		{Type: runtimespec.NetworkNamespace, Path: "/proc/self/ns/net"},
	}}}
	if !runtimeSpecHasNetworkNamespacePath(spec) {
		t.Fatal("non-empty invalid network path was not marked stale")
	}
	if runtimeSpecJoinsNetworkSandbox(spec, "/run/wendy/netns/myapp") {
		t.Fatal("invalid network path matched canonical sandbox")
	}
}

func TestRegisterNetworkSandboxReleasesReplacedOwner(t *testing.T) {
	c := &Client{networkSandboxes: make(map[string]*networkSandbox)}
	closed := 0
	first := &networkSandbox{containerID: "myapp", cleanup: func() { closed++ }}
	second := &networkSandbox{containerID: "myapp"}
	c.registerNetworkSandbox(first)
	c.registerNetworkSandbox(second)
	if closed != 1 {
		t.Fatalf("old sandbox cleanup calls = %d, want 1", closed)
	}
	first.close()
	if closed != 1 {
		t.Fatalf("cleanup was not idempotent: calls = %d", closed)
	}
	if got := c.takeNetworkSandbox("myapp"); got != second {
		t.Fatalf("registered sandbox = %#v, want second", got)
	}
}

func TestNetworkSandboxPathRejectsTraversal(t *testing.T) {
	if got := networkSandboxPath("../escape"); got != "" {
		t.Fatalf("unsafe path = %q", got)
	}
	if got := networkSandboxPath("myapp"); got != "/run/wendy/netns/myapp" {
		t.Fatalf("path = %q", got)
	}
	if got := networkSandboxResultPath("../escape"); got != "" {
		t.Fatalf("unsafe result path = %q", got)
	}
}
