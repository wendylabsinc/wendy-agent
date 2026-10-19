package containerd

import (
	"context"
	"errors"
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/wendylabsinc/wendy/go/internal/agent/meshingress"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"go.uber.org/zap"
)

type ingressRecoveryContainer struct{ staleContainerForTest }

func (ingressRecoveryContainer) ID() string { return "sh.wendy.recovery" }

func TestMeshIngressRecoveryScopesBackgroundRetryToNamespace(t *testing.T) {
	called := false
	ctr := ingressRecoveryContainer{staleContainerForTest{taskFn: func(ctx context.Context) (containerd.Task, error) {
		called = true
		ns, ok := namespaces.Namespace(ctx)
		if !ok || ns != "wendy-test" {
			t.Errorf("recovery task lookup lost its containerd namespace: %q, %v", ns, ok)
		}
		return nil, errors.New("end test before task/kernel inspection")
	}}}
	client := &Client{logger: zap.NewNop(), namespace: "wendy-test", meshIngress: meshingress.NewRegistry()}
	labels := wendyLabels(ctr.ID(), "", "test", nil, []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "mesh"}}, "isolated", nil)
	client.rehydrateRunningMeshIngressAttempt(context.Background(), ctr, labels, 0)
	if !called {
		t.Fatal("recovery never reached its containerd lookup")
	}
}

func TestMeshIngressRecoveryHonorsStopDuringRetry(t *testing.T) {
	ctr := ingressRecoveryContainer{staleContainerForTest{taskFn: func(context.Context) (containerd.Task, error) {
		t.Error("a delayed recovery inspected a task after its owner stopped the app")
		return nil, errors.New("stopped tasks must remain denied")
	}}}
	registry := meshingress.NewRegistry()
	client := &Client{logger: zap.NewNop(), namespace: "wendy-test", meshIngress: registry}
	labels := wendyLabels(ctr.ID(), "", "test", nil, []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork, Mode: "mesh"}}, "isolated", nil)
	labels[labelKeyStoppedByUser] = "true"
	client.rehydrateRunningMeshIngressAttempt(context.Background(), ctr, labels, 0)
	if registry.Allowed(8080) {
		t.Fatal("explicitly stopped task regained ingress")
	}
}
