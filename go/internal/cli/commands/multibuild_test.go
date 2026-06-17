package commands

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestPoolBuilderName(t *testing.T) {
	cases := []struct {
		base string
		mtls bool
		i    int
		want string
	}{
		{"wendy", false, 0, "wendy-pool-0"},
		{"wendy", false, 3, "wendy-pool-3"},
		{"wendy", true, 0, "wendy-mtls-pool-0"},
		{"acme", true, 2, "acme-mtls-pool-2"},
	}
	for _, c := range cases {
		if got := poolBuilderName(c.base, c.mtls, c.i); got != c.want {
			t.Errorf("poolBuilderName(%q, %v, %d) = %q, want %q", c.base, c.mtls, c.i, got, c.want)
		}
	}
}

func TestMultiServiceUsesBuildxPool(t *testing.T) {
	origGOOS, origGOARCH := imageBuilderHostGOOS, imageBuilderHostGOARCH
	defer func() { imageBuilderHostGOOS, imageBuilderHostGOARCH = origGOOS, origGOARCH }()
	t.Setenv("WENDY_BUILDX_POOL", "")

	// Non-darwin host: the Docker buildx path is always taken.
	imageBuilderHostGOOS = func() string { return "linux" }
	imageBuilderHostGOARCH = func() string { return "amd64" }
	if !multiServiceUsesBuildxPool("docker") {
		t.Error("explicit docker builder should use the pool")
	}
	if !multiServiceUsesBuildxPool("") {
		t.Error("default builder on a non-mac host should use the pool")
	}
	if multiServiceUsesBuildxPool("apple-container") {
		t.Error("apple-container builder must not use the buildx pool")
	}

	// macOS arm64: the no-flag auto path tries Apple Container first, so skip the
	// pool; an explicit --builder docker still uses it.
	imageBuilderHostGOOS = func() string { return "darwin" }
	imageBuilderHostGOARCH = func() string { return "arm64" }
	if multiServiceUsesBuildxPool("") {
		t.Error("auto Apple Container path (mac arm64, no flag) must skip the pool")
	}
	if !multiServiceUsesBuildxPool("docker") {
		t.Error("explicit docker builder on mac should use the pool")
	}

	// Opt-out env disables the pool everywhere.
	t.Setenv("WENDY_BUILDX_POOL", "0")
	if multiServiceUsesBuildxPool("docker") {
		t.Error("WENDY_BUILDX_POOL=0 should disable the pool")
	}
}

// TestBuildServicesParallelAssignsDistinctBuilders verifies the core WDY-1554
// invariant: with a builder pool, no two concurrent builds ever share a builder,
// and concurrency reaches the pool size.
func TestBuildServicesParallelAssignsDistinctBuilders(t *testing.T) {
	pool := []buildxAssignment{
		{name: "wendy-pool-0", cacheDir: "/c0"},
		{name: "wendy-pool-1", cacheDir: "/c1"},
		{name: "wendy-pool-2", cacheDir: "/c2"},
	}
	const numServices = 6
	services := map[string]*appconfig.ServiceConfig{}
	for i := 0; i < numServices; i++ {
		services[fmt.Sprintf("svc%d", i)] = &appconfig.ServiceConfig{}
	}

	var (
		mu        sync.Mutex
		busy      = map[string]bool{}
		usedCount = map[string]int{}
		inFlight  int
		gateOnce  sync.Once
		gate      = make(chan struct{})
	)

	orig := buildServiceImage
	defer func() { buildServiceImage = orig }()
	buildServiceImage = func(ctx context.Context, conn *grpcclient.AgentConnection, regPort int, builder, dir, repo, platform, dockerfile string, buildArgs map[string]string, asg *buildxAssignment, streamOutput, logOutput io.Writer) error {
		if asg == nil {
			t.Error("pooled build received a nil assignment")
			return nil
		}
		mu.Lock()
		if busy[asg.name] {
			mu.Unlock()
			t.Errorf("builder %s used by two concurrent builds", asg.name)
			return nil
		}
		busy[asg.name] = true
		usedCount[asg.name]++
		inFlight++
		reachedPoolSize := inFlight == len(pool)
		mu.Unlock()

		// The build that brings concurrency up to the pool size opens the gate; the
		// others block on it. This deterministically forces pool-size concurrency
		// (the first batch all hold the gate, proving distinct simultaneous builders).
		if reachedPoolSize {
			gateOnce.Do(func() { close(gate) })
		}
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out before reaching pool-size concurrency (inFlight stuck below %d)", len(pool))
		}

		mu.Lock()
		busy[asg.name] = false
		inFlight--
		mu.Unlock()
		return nil
	}

	if err := buildServicesParallel(context.Background(), nil, 5000, t.TempDir(), "app", services, "linux/arm64", nil, imageBuilderDocker, pool); err != nil {
		t.Fatalf("buildServicesParallel: %v", err)
	}

	if len(usedCount) != len(pool) {
		t.Errorf("distinct builders used = %d, want %d", len(usedCount), len(pool))
	}
	total := 0
	for _, c := range usedCount {
		total += c
	}
	if total != numServices {
		t.Errorf("total builds = %d, want %d", total, numServices)
	}
}

// TestBuildServicesParallelNilPoolUsesNilAssignments verifies the unchanged
// single-shared-builder path: every build gets a nil assignment.
func TestBuildServicesParallelNilPoolUsesNilAssignments(t *testing.T) {
	services := map[string]*appconfig.ServiceConfig{"a": {}, "b": {}, "c": {}}

	var mu sync.Mutex
	count := 0

	orig := buildServiceImage
	defer func() { buildServiceImage = orig }()
	buildServiceImage = func(ctx context.Context, conn *grpcclient.AgentConnection, regPort int, builder, dir, repo, platform, dockerfile string, buildArgs map[string]string, asg *buildxAssignment, streamOutput, logOutput io.Writer) error {
		if asg != nil {
			t.Errorf("expected nil assignment without a pool, got %q", asg.name)
		}
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}

	if err := buildServicesParallel(context.Background(), nil, 5000, t.TempDir(), "app", services, "linux/arm64", nil, imageBuilderDocker, nil); err != nil {
		t.Fatalf("buildServicesParallel: %v", err)
	}
	if count != len(services) {
		t.Errorf("built %d services, want %d", count, len(services))
	}
}
