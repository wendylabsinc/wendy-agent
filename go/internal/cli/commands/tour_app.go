package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	go2bundle "github.com/wendylabsinc/wendy/go/simulator/go2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const tourExampleMarker = ".wendy-tour-example"

type tourExampleIdentity struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
}

// scaffoldTourApp resumes the tour's editable copy without replacing user
// changes. An unrelated directory keeps its name and contents.
func scaffoldTourApp(projectDir, kind string) (string, error) {
	if strings.TrimSpace(projectDir) == "" {
		return "", errors.New("tour project directory is empty")
	}
	switch kind {
	case "patrol", "roam", "teleop", "sensors":
	default:
		return "", fmt.Errorf("unknown tour app %q", kind)
	}
	root, err := filepath.Abs(filepath.Join(projectDir, "apps"))
	if err != nil {
		return "", err
	}
	for n := 1; n <= 1000; n++ {
		name := kind
		if n > 1 {
			name = fmt.Sprintf("%s-%d", kind, n)
		}
		path := filepath.Join(root, name)
		if isTourExample(path, kind) {
			return path, nil
		}
		path, err = go2bundle.MaterializeExample(kind, path)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(tourExampleIdentity{Version: 1, Kind: kind})
		marker, err := os.OpenFile(filepath.Join(path, tourExampleMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return "", err
		}
		_, writeErr := marker.Write(append(data, '\n'))
		closeErr := marker.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("no free directory for the %s app in %s", kind, root)
}

func isTourExample(path, kind string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	marker := filepath.Join(path, tourExampleMarker)
	info, err = os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(marker)
	var identity tourExampleIdentity
	return err == nil && json.Unmarshal(data, &identity) == nil && identity.Version == 1 && identity.Kind == kind
}

var (
	connectTourAppSimulatorFn = connectSimulatorChoice
	runTourAppWithAgentFn     = runWithAgent
	callTourAppServiceFn      = func(ctx context.Context, conn *grpcclient.AgentConnection, req *agentpbv2.CallROS2ServiceRequest) (*agentpbv2.CallROS2ServiceResponse, error) {
		return agentpbv2.NewROS2ServiceClient(conn.Conn).CallService(ctx, req)
	}
	tourAppPortFn = func(ctx context.Context, vmName string, guestPort int) (int, error) {
		store, err := robotVMStore()
		if err != nil {
			return 0, err
		}
		return store.TCPPortMapping(ctx, vmName, guestPort)
	}
	tourAppRobotEndpointFn = robotEndpoint
	tourAppRobotRequestFn  = tourRobotRequest
	stopTourAppContainerFn = func(ctx context.Context, conn *grpcclient.AgentConnection, appID string) error {
		_, err := conn.ContainerService.StopContainer(ctx, &agentpb.StopContainerRequest{AppName: appID})
		return err
	}
)

func connectTourApp(ctx context.Context, vmName string) (*SelectedDevice, error) {
	if err := vm.ValidName(vmName); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := connectTourAppSimulatorFn(ctx, &simulatorChoice{Name: vmName}, true)
	if err != nil {
		return nil, err
	}
	if target == nil || target.Agent == nil || target.Agent.SimulatorName != vmName {
		if target != nil {
			target.Close()
		}
		return nil, fmt.Errorf("the tour app requires a connection to simulator %q", vmName)
	}
	return target, nil
}

func runTourApp(ctx context.Context, vmName, path string) error {
	cfg, err := appconfig.LoadFromFile(filepath.Join(path, "wendy.json"))
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	target, err := connectTourApp(ctx, vmName)
	if err != nil {
		return err
	}
	defer target.Close()
	return runTourAppWithAgentFn(ctx, target.Agent, path, cfg, runOptions{
		buildType: "docker", builder: "docker", dockerfile: "Dockerfile", detach: true, noRestart: true,
	})
}

var tourTriggerResult = regexp.MustCompile(`(?i)\b["']?success["']?\s*[:=]\s*(true|false)\b`)

// controlTourApp only sends motion service requests after the guide's explicit
// Start/Stop selection. A successful RPC alone does not prove Trigger accepted.
func controlTourApp(ctx context.Context, vmName, kind, action string) (string, error) {
	if (kind != "patrol" && kind != "roam") || (action != "start" && action != "stop") {
		return "", errors.New("tour control requires patrol or roam and start or stop")
	}
	target, err := connectTourApp(ctx, vmName)
	if err != nil {
		return "", err
	}
	defer target.Close()
	domain := int32(0)
	rpcCtx, cancel := ros2Ctx(ctx, ros2UnaryTimeout)
	defer cancel()
	response, err := callTourAppServiceFn(rpcCtx, target.Agent, &agentpbv2.CallROS2ServiceRequest{
		DomainId: &domain, Service: "/" + kind + "/" + action, Type: "std_srvs/srv/Trigger", Request: "{}",
	})
	if err != nil {
		return "", ros2RPCError(err)
	}
	result := response.GetResponse()
	accepted := tourTriggerResult.FindStringSubmatch(result)
	if !response.GetSuccess() || len(accepted) != 2 || !strings.EqualFold(accepted[1], "true") {
		return result, fmt.Errorf("%s did not confirm %s: %s", kind, action, result)
	}
	return result, nil
}

// tourAppURL returns a browser address only after checking the current VM's
// actual forwarding and waiting for its HTTP listener to answer.
func tourAppURL(ctx context.Context, vmName, path string) (string, error) {
	if err := vm.ValidName(vmName); err != nil {
		return "", err
	}
	cfg, err := appconfig.LoadFromFile(filepath.Join(path, "wendy.json"))
	if err != nil {
		return "", err
	}
	guestPort, found := httpEntitlementPort(cfg.Entitlements)
	if !found {
		return "", nil
	}
	port, err := tourAppPortFn(ctx, vmName, guestPort)
	if err != nil {
		return "", err
	}
	if port == 0 {
		return "", errors.New("the app has no verified simulator port forward")
	}
	readiness := &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}}
	if err := waitForReadiness(ctx, readiness, "127.0.0.1", port); err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

type tourAppControlStatus struct {
	tourRobotObservation
	ROSCommands struct {
		Owner   string `json:"owner"`
		Sources []struct {
			GID      string  `json:"publisher_gid"`
			NodeName string  `json:"node_name"`
			AgeMS    float64 `json:"age_ms"`
		} `json:"sources"`
	} `json:"ros_commands"`
}

// prepareTourAppCleanup captures the deployment identity before the manifest
// can be edited again. Stopping the container also makes an unchanged redeploy
// create a new ROS publisher, which can receive a fresh simulator grant.
func prepareTourAppCleanup(vmName, kind, path string) (func(context.Context) error, error) {
	if err := vm.ValidName(vmName); err != nil {
		return nil, err
	}
	if kind == "sensors" {
		return func(context.Context) error { return nil }, nil
	}
	if kind != "patrol" && kind != "roam" && kind != "teleop" {
		return nil, fmt.Errorf("unknown movement app %q", kind)
	}
	cfg, err := appconfig.LoadFromFile(filepath.Join(path, "wendy.json"))
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	appID := cfg.AppID
	if appID == go2RuntimeAppID || appID == g1RuntimeAppID || appID == r2RuntimeAppID {
		return nil, fmt.Errorf("%s is reserved for the managed robot", appID)
	}
	return func(ctx context.Context) error {
		return releaseTourApp(ctx, vmName, kind, appID)
	}, nil
}

func releaseTourApp(ctx context.Context, vmName, kind, appID string) error {
	target, err := connectTourApp(ctx, vmName)
	if err != nil {
		return err
	}
	defer target.Close()
	_, profile, port, err := tourAppRobotEndpointFn(ctx, vmName)
	if err != nil {
		stopErr := stopTourAppContainerFn(ctx, target.Agent, appID)
		if status.Code(stopErr) == codes.NotFound {
			stopErr = nil
		}
		return errors.Join(err, stopErr)
	}
	readStatus := func() (tourAppControlStatus, error) {
		var state tourAppControlStatus
		if err := tourAppRobotRequestFn(ctx, port, "/api/status", nil, &state); err != nil {
			return state, err
		}
		return state, state.matches(vmName, profile)
	}
	before, readErr := readStatus()
	// Stop the known container even if the viewer cannot currently answer.
	if err := stopTourAppContainerFn(ctx, target.Agent, appID); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil // A failed deployment may never have created this app.
		}
		return fmt.Errorf("stopping %s: %w", appID, err)
	}
	if readErr != nil {
		return fmt.Errorf("app stopped, but its simulator control could not be inspected: %w", readErr)
	}
	owner := before.ROSCommands.Owner
	if owner == "" {
		return nil
	}
	owned := false
	for _, source := range before.ROSCommands.Sources {
		if source.GID == owner && source.NodeName == "wendy_go2_"+kind {
			owned = true
			break
		}
	}
	if !owned {
		return nil // Another app's grant is outside this cleanup's scope.
	}
	current, err := readStatus()
	if err != nil {
		return err
	}
	if current.Epoch != before.Epoch || current.ROSCommands.Owner != owner {
		return nil
	}
	// Node names can be reused by a copied example. A source still emitting
	// after this container stopped belongs to another live publisher.
	quietDeadline := time.Now().Add(time.Second)
	for {
		quiet := false
		for _, source := range current.ROSCommands.Sources {
			if source.GID == owner && source.AgeMS >= 250 && !math.IsInf(source.AgeMS, 0) {
				quiet = true
			}
		}
		if quiet {
			break
		}
		if time.Now().After(quietDeadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		current, err = readStatus()
		if err != nil {
			return err
		}
		if current.Epoch != before.Epoch || current.ROSCommands.Owner != owner {
			return nil
		}
	}
	// The current endpoint cannot revoke conditionally. Check the captured GID
	// immediately before this call; this is not an atomic cross-process guard.
	var released struct {
		Released bool `json:"released"`
	}
	if err := tourAppRobotRequestFn(ctx, port, "/api/disarm_ros", map[string]any{}, &released); err != nil {
		return err
	}
	if !released.Released {
		return errors.New("the simulator did not confirm release of app control")
	}
	for {
		current, err = readStatus()
		if err != nil {
			return err
		}
		if !current.Armed && len(current.Command) == 3 && len(current.Velocity) == 3 &&
			math.Abs(current.Command[0])+math.Abs(current.Command[1])+math.Abs(current.Command[2]) == 0 &&
			math.Hypot(current.Velocity[0], current.Velocity[1]) < .15 {
			return nil
		}
		if current.ROSCommands.Owner != "" && current.ROSCommands.Owner != owner {
			return nil // A new controller took over after our release.
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("app control was released but a stopped robot was not confirmed: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
