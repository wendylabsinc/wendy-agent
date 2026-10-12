//go:build darwin || linux || windows

package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
)

// This starter controls only a positively identified managed Go2 VM. It has no
// address/device option: editing the file cannot redirect movement to hardware.
type tourRobotProject struct {
	Version         int     `json:"version"`
	VMName          string  `json:"vm_name"`
	DurationSeconds float64 `json:"duration_seconds"`
	ForwardMPS      float64 `json:"forward_mps"`
	TurnRPS         float64 `json:"turn_rps"`
}

func (p tourRobotProject) validate() error {
	if p.Version != 1 {
		return errors.New("unsupported robot tour project version")
	}
	if err := vm.ValidName(p.VMName); err != nil {
		return err
	}
	for _, v := range []float64{p.DurationSeconds, p.ForwardMPS, p.TurnRPS} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("movement settings must be finite numbers")
		}
	}
	if p.DurationSeconds < 1 || p.DurationSeconds > 8 || p.ForwardMPS < .55 || p.ForwardMPS > .6 || math.Abs(p.TurnRPS) > 1 {
		return errors.New("use duration_seconds between 1 and 8, forward_mps between 0.55 and 0.6, and turn_rps between -1 and 1")
	}
	return nil
}

func loadTourRobotProject(dir string) (tourRobotProject, string, error) {
	var p tourRobotProject
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, "", err
		}
		dir = filepath.Join(home, "Documents", "wendy-robot-tour")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return p, "", err
	}
	path := filepath.Join(dir, "tour.json")
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 8192))
		d.DisallowUnknownFields()
		if err = d.Decode(&p); err == nil {
			if d.Decode(new(any)) != io.EOF {
				err = errors.New("expected one JSON object")
			}
		}
		if err != nil {
			return p, path, fmt.Errorf("read %s: %w", path, err)
		}
		return p, path, p.validate()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return p, path, err
	}
	var id [4]byte
	if _, err = rand.Read(id[:]); err != nil {
		return p, path, err
	}
	p = tourRobotProject{Version: 1, VMName: "tour-" + hex.EncodeToString(id[:]), DurationSeconds: 5, ForwardMPS: .55, TurnRPS: .5}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return p, path, err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return p, path, err
	}
	_, err = f.Write(append(data, '\n'))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return p, path, err
}

func runRobotTour(cmd *cobra.Command, projectDir string) (runErr error) {
	ctx := analytics.WithEntrypoint(analytics.WithAttempt(cmd.Context()), "tour")
	step := "dependency_check"
	tourStage(ctx, "simulator", "journey_started", "started", nil, "prepare")
	defer func() { tourStage(ctx, "simulator", "journey_completed", tourOutcome(runErr), runErr, step) }()
	if !vm.DetachSupported() {
		return errors.New("the robot tour needs macOS or Linux for its background VM; use 'wendy tour --hardware' on this host")
	}
	return runWithInterruptContext(ctx, func(ctx context.Context) error {
		fmt.Fprintln(cmd.OutOrStdout(), "Set up a simulated Go2, watch it walk an arc, then edit and run again.\nThe first run downloads WendyOS and builds the robot runtime. Allow several minutes and disk space for the VM and container images.\nRequires QEMU with ARM64 firmware and a running Docker builder. No robot, cloud account, or host Python is needed.")
		if err := ensureQEMUFn(ctx); err != nil {
			return err
		}
		if _, err := vm.FindFirmware(runtime.GOOS, vm.BrewPrefix()); err != nil {
			return err
		}
		if err := ensureDockerDaemon(ctx); err != nil {
			return err
		}
		step = "project"
		p, path, err := loadTourRobotProject(projectDir)
		if err != nil {
			return err
		}
		tourStage(ctx, "simulator", "project_ready", "completed", nil, "project")
		tourStage(ctx, "simulator", "target_selected", "completed", nil, "select_target")
		resumeCommand := tourRobotResumeCommand(path)
		fmt.Fprintf(cmd.OutOrStdout(), "Your editable starter: %s\nResume or walk again: %s\nStop the simulator: wendy vm stop %s\n", path, resumeCommand, p.VMName)
		step = "provision"
		store, err := robotVMStore()
		if err != nil {
			return err
		}
		if _, exists := store.ReadMeta(p.VMName); !exists {
			if err := createVM(cmd.OutOrStdout(), p.VMName, "", "", defaultSimulatorDiskGiB, false, 0); err != nil {
				return err
			}
		}
		profile, exists, err := store.ReadRobotProfile(p.VMName)
		if err != nil {
			return err
		}
		if !exists {
			if err := attachSimulatorProfile(p.VMName, vm.RobotKindGo2); err != nil {
				return err
			}
		} else if profile.Kind != vm.RobotKindGo2 {
			return errors.New("this starter's VM has a different robot profile; choose a new --project directory")
		}
		// This operation connects the VM and provisions its robot runtime.
		// Child operations provide narrower steps; the tour records the deploy phase.
		step = "deploy"
		picked, err := connectSimulatorChoice(ctx, &simulatorChoice{Name: p.VMName}, true)
		if err != nil {
			return err
		}
		defer picked.Agent.Close()
		tourStage(ctx, "simulator", "device_boot_verified", "completed", nil, "first_boot")
		_, profile, port, err := robotEndpoint(ctx, p.VMName)
		if err != nil {
			return err
		}
		step = "readiness"
		if err := waitForRobot(ctx, p.VMName, port, profile); err != nil {
			return err
		}
		tourStage(ctx, "simulator", "app_ready", "completed", nil, "readiness")
		if err := browserOpen(robotURL(port)); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Open the viewer: %s\n", robotURL(port))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Viewer: %s\nWatch the robot walk for %.0f seconds. Ctrl-C stops the demonstration.\n", robotURL(port), p.DurationSeconds)
		// Give the browser time to load before this finite demonstration starts.
		select {
		case <-ctx.Done():
			return ErrUserCancelled
		case <-time.After(2 * time.Second):
		}
		distance, err := walkTourRobot(ctx, port, profile, p)
		if err != nil {
			return fmt.Errorf("robot demonstration: %w; resume with:\n  %s", err, resumeCommand)
		}
		tourStage(ctx, "simulator", "motion_verified", "completed", nil, "readiness")
		fmt.Fprintf(cmd.OutOrStdout(), "Your simulated Go2 moved %.2f metres from its starting point and stopped.\nThe viewer stays open. Choose Enable controls, then hold W to walk. Space stops; Release controls ends manual control.\nEdit turn_rps or duration_seconds in %s, then run:\n  %s\nStop the VM when finished:\n  wendy vm stop %s\nSet up a physical device next:\n  wendy tour --hardware\n", distance, path, resumeCommand, p.VMName)
		return nil
	})
}

func tourRobotResumeCommand(projectPath string) string {
	return "wendy tour --simulator --project " + shellQuoteArg(filepath.Dir(projectPath))
}

func tourOutcome(err error) string {
	if err == nil {
		return "completed"
	}
	if errors.Is(err, ErrUserCancelled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

func tourStage(ctx context.Context, route, event, outcome string, err error, step string) {
	errorClass := ""
	if err != nil {
		errorClass = ExecutionErrorClass(err)
	}
	properties := map[string]string{"entrypoint": "tour", "route": route, "stage": "tour"}
	if step != "" {
		properties["step"] = step
	}
	analytics.TrackStage(ctx, event, "wendy tour", outcome, errorClass, properties)
}

type tourRobotObservation struct {
	robotRuntimeStatus
	Position []float64 `json:"position"`
	Velocity []float64 `json:"linear_velocity_world"`
	Command  []float64 `json:"command"`
	Time     float64   `json:"time"`
	Armed    bool      `json:"armed"`
}

func tourRobotRequest(ctx context.Context, port int, path string, body any, result any) error {
	var reader io.Reader
	method := http.MethodGet
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, robotURL(port)+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := robotHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("simulator %s returned HTTP %d; inspect the viewer and 'wendy vm robot status'", path, resp.StatusCode)
	}
	if result == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result)
}

func observeTourRobot(ctx context.Context, port int, profile vm.RobotProfile, name string) (tourRobotObservation, error) {
	var o tourRobotObservation
	if err := tourRobotRequest(ctx, port, "/api/status", nil, &o); err != nil {
		return o, err
	}
	if err := o.matches(name, profile); err != nil {
		return o, err
	}
	if !o.Healthy || !o.Ready || len(o.Position) != 3 || len(o.Velocity) != 3 || len(o.Command) != 3 {
		return o, errors.New("simulated robot is not ready; inspect the viewer before retrying")
	}
	for _, v := range append(append(append([]float64{o.Time}, o.Position...), o.Velocity...), o.Command...) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return o, errors.New("invalid simulated robot observation")
		}
	}
	return o, nil
}

func walkTourRobot(ctx context.Context, port int, profile vm.RobotProfile, p tourRobotProject) (distance float64, err error) {
	if err = p.validate(); err != nil {
		return
	}
	before, err := observeTourRobot(ctx, port, profile, p.VMName)
	if err != nil {
		return 0, err
	}
	if before.Armed {
		return 0, errors.New("another controller owns the simulator; release its controls in the viewer before running the tour")
	}
	// A repeat always starts in the same empty part of this tour's world.
	if err = tourRobotRequest(ctx, port, "/api/reset", map[string]any{}, nil); err != nil {
		return
	}
	if err = waitForRobot(ctx, p.VMName, port, profile); err != nil {
		return
	}
	before, err = observeTourRobot(ctx, port, profile, p.VMName)
	if err != nil {
		return 0, err
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err = tourRobotRequest(ctx, port, "/api/arm", map[string]any{}, &grant); err != nil {
		return
	}
	if grant.Token == "" {
		return 0, errors.New("simulator did not grant control")
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		stopErr := tourRobotRequest(stopCtx, port, "/api/stop", map[string]any{"token": grant.Token}, nil)
		releaseErr := tourRobotRequest(stopCtx, port, "/api/release", map[string]any{"token": grant.Token}, nil)
		if stopErr != nil || releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("could not confirm simulator stop/release: %w", errors.Join(stopErr, releaseErr)))
			return
		}
		for {
			o, e := observeTourRobot(stopCtx, port, profile, p.VMName)
			if e == nil && !o.Armed && math.Abs(o.Command[0])+math.Abs(o.Command[1])+math.Abs(o.Command[2]) == 0 && math.Hypot(o.Velocity[0], o.Velocity[1]) < .15 {
				return
			}
			select {
			case <-stopCtx.Done():
				err = errors.Join(err, errors.New("stop was requested but stationary robot state was not verified"))
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	deadline := time.NewTimer(time.Duration(p.DurationSeconds * float64(time.Second)))
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return distance, ErrUserCancelled
		case <-deadline.C:
			if distance < .1 {
				return distance, errors.New("commands were accepted but at least 10 cm of robot motion was not observed")
			}
			return distance, nil
		case <-tick.C:
			o, e := observeTourRobot(ctx, port, profile, p.VMName)
			if e != nil {
				return distance, e
			}
			if o.Epoch != before.Epoch || o.Time < before.Time {
				return distance, errors.New("simulator was reset during the demonstration")
			}
			distance = math.Max(distance, math.Hypot(o.Position[0]-before.Position[0], o.Position[1]-before.Position[1]))
			if e = tourRobotRequest(ctx, port, "/api/command", map[string]any{"token": grant.Token, "velocity": []float64{p.ForwardMPS, 0, p.TurnRPS}}, nil); e != nil {
				return distance, e
			}
		}
	}
}
