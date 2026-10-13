package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTourAppScaffoldResumesEditsAndAvoidsUnrelatedDirectories(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, "apps", "patrol")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "patrol.py"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := scaffoldTourApp(root, "patrol")
	if err != nil || path != filepath.Join(root, "apps", "patrol-2") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "patrol.py"), []byte("my route"), 0o644); err != nil {
		t.Fatal(err)
	}
	resumed, err := scaffoldTourApp(root, "patrol")
	if err != nil || resumed != path {
		t.Fatalf("resumed=%q err=%v", resumed, err)
	}
	for dir, want := range map[string]string{unrelated: "unrelated", path: "my route"} {
		got, _ := os.ReadFile(filepath.Join(dir, "patrol.py"))
		if string(got) != want {
			t.Fatalf("changed existing source in %s", dir)
		}
	}
}

func TestTourAppScaffoldDoesNotReuseSymlinksOrForeignMarkers(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(strconv.FormatBool(symlink), func(t *testing.T) {
			root := t.TempDir()
			outside, err := scaffoldTourApp(t.TempDir(), "patrol")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "apps"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "apps", "patrol")
			if symlink {
				err = os.Symlink(outside, path)
			} else {
				err = os.Mkdir(path, 0o755)
				if err == nil {
					err = os.WriteFile(filepath.Join(path, tourExampleMarker), []byte(`{"version":1,"kind":"roam"}`), 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := scaffoldTourApp(root, "patrol")
			if err != nil || got != path+"-2" {
				t.Fatalf("path=%q err=%v", got, err)
			}
		})
	}
}

func stubTourAppConnection(t *testing.T) *grpcclient.AgentConnection {
	t.Helper()
	previous := connectTourAppSimulatorFn
	conn := &grpcclient.AgentConnection{SimulatorName: "tour-test"}
	connectTourAppSimulatorFn = func(_ context.Context, choice *simulatorChoice, suppress bool) (*SelectedDevice, error) {
		if choice.Name != "tour-test" || choice.Create || !suppress {
			t.Fatalf("unexpected simulator selection %+v, suppress=%t", choice, suppress)
		}
		return &SelectedDevice{Agent: conn}, nil
	}
	t.Cleanup(func() { connectTourAppSimulatorFn = previous })
	return conn
}

func TestTourAppDeployUsesSelectedSimulatorAndOrdinaryAppProtections(t *testing.T) {
	path, err := scaffoldTourApp(t.TempDir(), "patrol")
	if err != nil {
		t.Fatal(err)
	}
	conn := stubTourAppConnection(t)
	previous := runTourAppWithAgentFn
	t.Cleanup(func() { runTourAppWithAgentFn = previous })
	called := false
	runTourAppWithAgentFn = func(_ context.Context, got *grpcclient.AgentConnection, cwd string, cfg *appconfig.AppConfig, opts runOptions) error {
		called = true
		if got != conn || cwd != path || cfg.AppID != "sh.wendy.simulator.go2.examples.patrol" {
			t.Fatal("deployment changed project or simulator")
		}
		if opts.managedRobot || opts.buildType != "docker" || opts.builder != "docker" || opts.dockerfile != "Dockerfile" || !opts.detach || !opts.noRestart || opts.restartUnlessStopped {
			t.Fatalf("unexpected deploy options %+v", opts)
		}
		return nil
	}
	if err := runTourApp(context.Background(), "tour-test", path); err != nil || !called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	conn.SimulatorName = "other-vm"
	called = false
	if err := runTourApp(context.Background(), "tour-test", path); err == nil || called {
		t.Fatal("accepted a connection to another simulator")
	}
}

func TestTourAppControlRequiresServiceAcceptance(t *testing.T) {
	conn := stubTourAppConnection(t)
	previous := callTourAppServiceFn
	t.Cleanup(func() { callTourAppServiceFn = previous })
	for _, tc := range []struct {
		response string
		rpcOK    bool
		wantOK   bool
	}{
		{"std_srvs.srv.Trigger_Response(success=True, message='ready')", true, true},
		{"std_srvs.srv.Trigger_Response(success=False, message='sensor_stale')", true, false},
		{"success: true\nmessage: ready", true, true},
		{"waiting for service to become available", true, false},
		{"failed", false, false},
	} {
		t.Run(tc.response, func(t *testing.T) {
			callTourAppServiceFn = func(_ context.Context, got *grpcclient.AgentConnection, req *agentpbv2.CallROS2ServiceRequest) (*agentpbv2.CallROS2ServiceResponse, error) {
				if got != conn || req.GetService() != "/patrol/start" || req.GetType() != "std_srvs/srv/Trigger" || req.GetRequest() != "{}" || req.DomainId == nil || *req.DomainId != 0 {
					t.Fatalf("unexpected service target or request %v", req)
				}
				return &agentpbv2.CallROS2ServiceResponse{Success: tc.rpcOK, Response: tc.response}, nil
			}
			response, err := controlTourApp(context.Background(), "tour-test", "patrol", "start")
			if (err == nil) != tc.wantOK || response != tc.response {
				t.Fatalf("response=%q err=%v", response, err)
			}
		})
	}
	if _, err := controlTourApp(context.Background(), "tour-test", "teleop", "start"); err == nil {
		t.Fatal("accepted unsupported control request")
	}
}

func TestTourAppURLVerifiesMappingAndHTTP(t *testing.T) {
	path, err := scaffoldTourApp(t.TempDir(), "sensors")
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	previous := tourAppPortFn
	t.Cleanup(func() { tourAppPortFn = previous })
	tourAppPortFn = func(_ context.Context, name string, guestPort int) (int, error) {
		if name != "tour-test" || guestPort != 8904 {
			t.Fatalf("unexpected mapping lookup %s:%d", name, guestPort)
		}
		return port, nil
	}
	got, err := tourAppURL(context.Background(), "tour-test", path)
	if err != nil || got != server.URL || requests == 0 {
		t.Fatalf("url=%q err=%v requests=%d", got, err, requests)
	}
	tourAppPortFn = func(context.Context, string, int) (int, error) { return 0, nil }
	if _, err := tourAppURL(context.Background(), "tour-test", path); err == nil || !strings.Contains(err.Error(), "verified") {
		t.Fatalf("unverified mapping err=%v", err)
	}
	tourAppPortFn = func(context.Context, string, int) (int, error) { return 0, context.Canceled }
	if _, err := tourAppURL(context.Background(), "tour-test", path); !errors.Is(err, context.Canceled) {
		t.Fatalf("mapping cancellation err=%v", err)
	}
}

func TestTourAppCleanupStopsCapturedAppAndReleasesOnlyItsQuietGrant(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		replaced, foreign, live, wrongIdentity bool
		stopErr                                error
		wantRelease, wantErr                   bool
	}{
		{name: "own grant", wantRelease: true},
		{name: "replacement grant", replaced: true},
		{name: "different node", foreign: true},
		{name: "copied node still publishing", live: true},
		{name: "app never created", stopErr: status.Error(codes.NotFound, "missing")},
		{name: "stop failed", stopErr: errors.New("stop failed"), wantErr: true},
		{name: "wrong simulator identity", wrongIdentity: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := scaffoldTourApp(t.TempDir(), "patrol")
			if err != nil {
				t.Fatal(err)
			}
			cleanup, err := prepareTourAppCleanup("tour-test", "patrol", path)
			if err != nil {
				t.Fatal(err)
			}
			// Later edits must never retarget cleanup to the robot runtime.
			if err := os.WriteFile(filepath.Join(path, "wendy.json"), []byte(`{"appId":"sh.wendy.simulator.go2"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			conn := stubTourAppConnection(t)
			oldEndpoint, oldRequest, oldStop := tourAppRobotEndpointFn, tourAppRobotRequestFn, stopTourAppContainerFn
			t.Cleanup(func() {
				tourAppRobotEndpointFn, tourAppRobotRequestFn, stopTourAppContainerFn = oldEndpoint, oldRequest, oldStop
			})
			profile, err := vm.NewGo2RobotProfile("sha256:"+strings.Repeat("1", 64), go2PolicyBundle)
			if err != nil {
				t.Fatal(err)
			}
			tourAppRobotEndpointFn = func(_ context.Context, name string) (*vm.Store, vm.RobotProfile, int, error) {
				if name != "tour-test" {
					t.Fatal(name)
				}
				return nil, profile, 1234, nil
			}
			stopped, released, reads := false, false, 0
			stopTourAppContainerFn = func(_ context.Context, got *grpcclient.AgentConnection, appID string) error {
				if got != conn || appID != "sh.wendy.simulator.go2.examples.patrol" {
					t.Fatalf("wrong stop target: %s", appID)
				}
				stopped = true
				return tc.stopErr
			}
			tourAppRobotRequestFn = func(_ context.Context, port int, endpoint string, body, result any) error {
				if port != 1234 {
					t.Fatal(port)
				}
				if endpoint == "/api/disarm_ros" {
					if !stopped || reads < 2 {
						t.Fatal("released before stopping and rechecking")
					}
					released = true
					return json.Unmarshal([]byte(`{"released":true}`), result)
				}
				if endpoint != "/api/status" || body != nil {
					t.Fatalf("unexpected request %s", endpoint)
				}
				reads++
				owner, node, age := "ours", "wendy_go2_patrol", 500.0
				if tc.foreign {
					node = "another_node"
				}
				if tc.live {
					age = 0
				}
				if tc.replaced && reads > 1 {
					owner = "replacement"
				}
				if released {
					owner = ""
				}
				state := tourAppControlStatus{tourRobotObservation: tourRobotObservation{
					robotRuntimeStatus: robotRuntimeStatus{VMName: "tour-test", Simulation: !tc.wrongIdentity, ProfileVersion: profile.Version, RobotKind: profile.Kind, SourceDigest: profile.SourceDigest, PolicyBundle: profile.PolicyBundle, World: profile.World, ClockMode: profile.ClockMode, Seed: profile.Seed, VisualDetail: profile.VisualDetail, DDSIsolation: "udp-rtps-loopback", Epoch: 1},
					Armed:              !released, Command: []float64{0, 0, 0}, Velocity: []float64{0, 0, 0},
				}}
				data, _ := json.Marshal(map[string]any{"owner": owner, "sources": []any{map[string]any{"publisher_gid": owner, "node_name": node, "age_ms": age}}})
				if err := json.Unmarshal(data, &state.ROSCommands); err != nil {
					t.Fatal(err)
				}
				*result.(*tourAppControlStatus) = state
				return nil
			}
			if err := cleanup(context.Background()); (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%t", err, tc.wantErr)
			}
			if !stopped || released != tc.wantRelease {
				t.Fatalf("stopped=%t released=%t", stopped, released)
			}
		})
	}
}

func TestTourAppCleanupRejectsManagedRuntimeIDs(t *testing.T) {
	path, err := scaffoldTourApp(t.TempDir(), "patrol")
	if err != nil {
		t.Fatal(err)
	}
	for _, appID := range []string{go2RuntimeAppID, g1RuntimeAppID, r2RuntimeAppID} {
		data, _ := json.Marshal(map[string]string{"appId": appID, "version": "0.1.0", "platform": "linux"})
		if err := os.WriteFile(filepath.Join(path, "wendy.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if cleanup, err := prepareTourAppCleanup("tour-test", "patrol", path); err == nil || cleanup != nil {
			t.Fatalf("accepted cleanup for %s", appID)
		}
	}
}
