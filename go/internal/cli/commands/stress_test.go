package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// TestResolveDeployableServicesFuzz generates random acyclic dependency graphs
// with random failures and checks resolveDeployableServices against an
// independent reference: a service is deployable iff it did not fail and all its
// dependencies are deployable. Also checks the dropped set is exactly the
// non-failed, non-deployable services.
func TestResolveDeployableServicesFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for iter := 0; iter < 5000; iter++ {
		n := 1 + r.Intn(12)
		names := make([]string, n)
		services := map[string]*appconfig.ServiceConfig{}
		for i := 0; i < n; i++ {
			names[i] = fmt.Sprintf("s%02d", i)
		}
		// Edges only to lower indices => acyclic.
		deps := map[string][]string{}
		for i := 0; i < n; i++ {
			for j := 0; j < i; j++ {
				if r.Intn(3) == 0 {
					deps[names[i]] = append(deps[names[i]], names[j])
				}
			}
			services[names[i]] = &appconfig.ServiceConfig{DependsOn: deps[names[i]]}
		}
		failed := map[string]error{}
		for i := 0; i < n; i++ {
			if r.Intn(4) == 0 {
				failed[names[i]] = errors.New("boom")
			}
		}

		// Reference: compute in index order (deps are all lower-indexed).
		ref := map[string]bool{}
		for i := 0; i < n; i++ {
			name := names[i]
			ok := failed[name] == nil
			if ok {
				for _, d := range deps[name] {
					if !ref[d] {
						ok = false
						break
					}
				}
			}
			ref[name] = ok
		}

		gotDeployable, gotDropped := resolveDeployableServices(services, failed)

		for _, name := range names {
			_, inDeploy := gotDeployable[name]
			if inDeploy != ref[name] {
				t.Fatalf("iter %d: service %s deployable=%v, want %v (failed=%v deps=%v)",
					iter, name, inDeploy, ref[name], failed[name] != nil, deps[name])
			}
			_, inDropped := gotDropped[name]
			wantDropped := !ref[name] && failed[name] == nil
			if inDropped != wantDropped {
				t.Fatalf("iter %d: service %s dropped=%v, want %v", iter, name, inDropped, wantDropped)
			}
		}
	}
}

// TestBuildServicesParallelStress hammers buildServicesParallel with a fake
// builder under random skip/fail/concurrency settings, asserting that: skipped
// services are never built, every non-skipped service is built exactly once, and
// the returned failure map matches exactly the injected failures of non-skipped
// services. Run with -race -count=N to stress the scheduling.
func TestBuildServicesParallelStress(t *testing.T) {
	root := t.TempDir()
	const n = 24
	services := map[string]*appconfig.ServiceConfig{}
	names := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("svc%02d", i)
		names[i] = name
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		services[name] = &appconfig.ServiceConfig{Context: name}
	}

	r := rand.New(rand.NewSource(42))
	skip := map[string]bool{}
	failSet := map[string]bool{}
	for _, name := range names {
		if r.Intn(3) == 0 {
			skip[name] = true
		} else if r.Intn(3) == 0 {
			failSet[name] = true
		}
	}

	const appID = "app"
	repoToName := func(repo string) string { return strings.TrimPrefix(repo, appID+"-") }

	var mu sync.Mutex
	built := map[string]int{}
	orig := buildServiceImage
	defer func() { buildServiceImage = orig }()
	buildServiceImage = func(_ context.Context, _ *grpcclient.AgentConnection, _ int, _, _, repo, _, _ string, _ map[string]string, _ string, _, _ io.Writer) error {
		name := repoToName(repo)
		mu.Lock()
		built[name]++
		mu.Unlock()
		if failSet[name] {
			return fmt.Errorf("injected failure for %s", name)
		}
		return nil
	}

	maxConc := 1 + r.Intn(8)
	failed, infraErr := buildServicesParallel(
		context.Background(), nil, 5000, root, appID, services, "linux/arm64", nil, "docker", skip, maxConc)
	if infraErr != nil {
		t.Fatalf("unexpected infra error: %v", infraErr)
	}

	for _, name := range names {
		mu.Lock()
		c := built[name]
		mu.Unlock()
		switch {
		case skip[name]:
			if c != 0 {
				t.Fatalf("skipped service %s was built %d times", name, c)
			}
		default:
			if c != 1 {
				t.Fatalf("service %s built %d times, want 1", name, c)
			}
		}
	}

	wantFailed := map[string]bool{}
	for name := range failSet {
		if !skip[name] {
			wantFailed[name] = true
		}
	}
	if len(failed) != len(wantFailed) {
		t.Fatalf("failed set size %d, want %d (failed=%v)", len(failed), len(wantFailed), sortedServiceErrorKeys(failed))
	}
	for name := range wantFailed {
		if failed[name] == nil {
			t.Fatalf("expected %s in failed set; got %v", name, sortedServiceErrorKeys(failed))
		}
	}
}
