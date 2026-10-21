package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/usermessage"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Review 4339052693: locally evidenced expiry and context switching override clock advice.
func TestReviewCertificateRecoveryStepsSurvive(t *testing.T) {
	for _, switchStep := range []string{"", "Switch to context 'other-org', then retry."} {
		err := tlsHandshakeRejectedError{cause: status.Error(codes.Unavailable, "remote error: tls: expired certificate"), refreshReason: "The client certificate has expired.", switchStep: switchStep}
		got := usermessage.Explain(err, ExecutionErrorClass(err))
		want := refreshCertsStep(switchStep)
		if len(got.Steps) != 1 || got.Steps[0] != want || strings.Contains(got.Text(), "sync-time") || strings.Count(got.Text(), want) != 1 {
			t.Errorf("wrong TLS recovery: %#v", got)
		}
	}
}

// Review 4339052700: the diagnostic must retain the exact local pin and command.
func TestReviewIdentityDiagnosticNamesHostAndOrganizations(t *testing.T) {
	err := refuseDevicePin(devicePinDiagnostic{hostname: "my-pi.local", heading: `Connection blocked: device "my-pi.local" identity changed.`, details: "Saved: organization 7\nNow:   organization 9"})
	got := userFacingGRPCError(err)
	for _, want := range []string{"my-pi.local", "wendy device unpin my-pi.local", "Saved: organization 7", "Now:   organization 9"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q: %q", want, got)
		}
	}
	if styled, ok := RenderIdentityRefusal(err, got); !ok || !strings.Contains(styled, err.(*deviceIdentityRefusalError).CLIMessage()) {
		t.Errorf("styled identity diagnostic not used: %q", styled)
	}
	if strings.Contains(got, "<host>") {
		t.Errorf("placeholder diagnostic: %q", got)
	}
}

// Review 4339052739: camera login and build summaries are CLI-authored guidance.
func TestReviewBuildFailureSummarySurvives(t *testing.T) {
	cause := &imageBuildFailedError{errors.New("exit status 1")}
	err := buildFailureError(buildFailureSummary{stage: "build 4/4", command: "RUN go build .", cause: "undefined: foo"}, cause)
	got := usermessage.Text(err)
	if !strings.Contains(got, "build 4/4") || !strings.Contains(got, "undefined: foo") {
		t.Errorf("lost compiler summary: %q", got)
	}
}

// Review 4339052753: terminal update verdicts retain their typed code and advice.
func TestReviewBusyUpdateRetainsStatusAndRecovery(t *testing.T) {
	err := agentUpdateTerminalError(status.Error(codes.FailedPrecondition, "update is already in progress: private lock detail"))
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("status flattened: %v", err)
	}
	got := userFacingGRPCError(err)
	for _, want := range []string{"Wait for it to finish", "reboot the device"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q: %q", want, got)
		}
	}
	if strings.Contains(got, "private lock") {
		t.Errorf("foreign text leaked: %q", got)
	}
}

// Review 4339052798: discovery View must be silent under diagnostic settings.
func TestReviewDiscoveryViewNeverLogs(t *testing.T) {
	t.Setenv("WENDY_TLS_DEBUG", "1")
	model := newDiscoverModel(context.Background(), discovery.DiscoveryOptions{}, false)
	model.err = status.Error(codes.Unavailable, "private transport detail")
	output := captureStderr(t, func() {
		for range 3 {
			_ = model.View()
		}
	})
	if output != "" {
		t.Fatalf("View wrote stderr: %q", output)
	}
}

func TestReviewCameraFailureKeepsLoginCommand(t *testing.T) {
	client := &fakeCameraTester{resp: &agentpb.TestCameraCredentialsResponse{Result: agentpb.TestCameraCredentialsResponse_RESULT_AUTH_FAILED, Detail: "private RTSP detail"}}
	err := runCameraTest(context.Background(), client, 3, &bytes.Buffer{})
	got := userFacingGRPCError(err)
	if !strings.Contains(got, "wendy device camera login 3") || strings.Contains(got, "private RTSP") {
		t.Errorf("lost camera remedy: %q", got)
	}
}

type missingReviewAppClient struct {
	agentpb.WendyContainerServiceClient
}

func (*missingReviewAppClient) StopContainer(context.Context, *agentpb.StopContainerRequest, ...grpc.CallOption) (*agentpb.StopContainerResponse, error) {
	return nil, status.Error(codes.NotFound, "private container lookup detail")
}

func TestReviewStoppingMissingAppNamesTheApp(t *testing.T) {
	previous := resolveAppsTargetFn
	resolveAppsTargetFn = func(context.Context, ...resolveOption) (*SelectedDevice, error) {
		return &SelectedDevice{Agent: &grpcclient.AgentConnection{ContainerService: &missingReviewAppClient{}}}, nil
	}
	t.Cleanup(func() { resolveAppsTargetFn = previous })
	cmd := newAppsStopCmd()
	err := cmd.RunE(cmd, []string{"typo"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("lost agent status: %v", err)
	}
	if got := userFacingGRPCError(err); got != "Stopping app typo: The requested item was not found." {
		t.Errorf("wrong missing-app message: %q", got)
	}
}
