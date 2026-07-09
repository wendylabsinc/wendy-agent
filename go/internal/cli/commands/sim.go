package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/simutil"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

func newSimCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sim",
		Short: "Manage robot simulations on the target device",
	}

	cmd.AddCommand(
		newSimCreateCmd(),
		newSimListCmd(),
		newSimImportModelCmd(),
		newSimDescribeModelCmd(),
		newSimSpawnCmd(),
		newSimStateCmd(),
		newSimCameraCmd(),
		newSimResetCmd(),
		newSimRunCmd(),
		newSimReplayCmd(),
	)

	return cmd
}

func newSimCreateCmd() *cobra.Command {
	var scenePath string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a simulation world",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var sceneYAML string
			if scenePath != "" {
				data, err := os.ReadFile(scenePath)
				if err != nil {
					return fmt.Errorf("reading scene file: %w", err)
				}
				sceneYAML = string(data)
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.CreateSimulation(ctx, &agentpbv2.CreateSimulationRequest{
				Name:      args[0],
				SceneYaml: sceneYAML,
			})
			if err != nil {
				return fmt.Errorf("creating simulation: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{
					"worldId": resp.GetWorldId(),
					"backend": resp.GetBackend(),
				})
			}
			cliSuccess("Simulation %s created.", args[0])
			cliLogln("  World ID: %s (backend %s)", resp.GetWorldId(), resp.GetBackend())
			return nil
		},
	}

	cmd.Flags().StringVar(&scenePath, "scene", "", "Path to a scene description YAML file")
	return cmd
}

func newSimListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List simulation worlds",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.ListSimulations(ctx, &agentpbv2.ListSimulationsRequest{})
			if err != nil {
				return fmt.Errorf("listing simulations: %w", err)
			}

			if jsonOutput {
				type jsonSim struct {
					WorldID  string   `json:"worldId"`
					Name     string   `json:"name,omitempty"`
					Backend  string   `json:"backend,omitempty"`
					RobotIDs []string `json:"robotIds,omitempty"`
				}
				sims := make([]jsonSim, len(resp.GetSimulations()))
				for i, s := range resp.GetSimulations() {
					sims[i] = jsonSim{
						WorldID:  s.GetWorldId(),
						Name:     s.GetName(),
						Backend:  s.GetBackend(),
						RobotIDs: s.GetRobotIds(),
					}
				}
				return printJSON(sims)
			}

			if len(resp.GetSimulations()) == 0 {
				cliLogln("No simulations running.")
				return nil
			}
			headers := []string{"World ID", "Name", "Backend", "Robots"}
			var rows [][]string
			for _, s := range resp.GetSimulations() {
				rows = append(rows, []string{
					s.GetWorldId(),
					s.GetName(),
					s.GetBackend(),
					strings.Join(s.GetRobotIds(), ", "),
				})
			}
			fmt.Print(tui.RenderTable(headers, rows))
			return nil
		},
	}
}

func newSimImportModelCmd() *cobra.Command {
	var menageriePath, name, formatFlag string

	cmd := &cobra.Command{
		Use:   "import-model [local-dir]",
		Short: "Import a robot model into the simulation backend",
		Long: "Import a robot model into the simulation backend.\n\n" +
			"Either reference a bundled MuJoCo Menagerie model (--menagerie unitree_go2/go2.xml)\n" +
			"or upload a local model directory (or .tar/.tar.gz archive of one).\n" +
			"The model format (mjcf, sdf, urdf) is auto-detected from the file\n" +
			"extensions in a local source; override with --format. Which formats\n" +
			"load depends on the backend (MuJoCo: mjcf; Gazebo: sdf, urdf).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var localPath string
			if len(args) == 1 {
				localPath = args[0]
			}
			if err := validateImportModelSource(menageriePath, localPath); err != nil {
				return err
			}
			format, err := simutil.ResolveModelFormat(formatFlag, menageriePath, localPath)
			if err != nil {
				return err
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := conn.SimService.ImportModel(ctx)
			if err != nil {
				return fmt.Errorf("importing model: %w", err)
			}
			if err := stream.Send(&simpb.LoadModelChunk{
				Payload: &simpb.LoadModelChunk_Source{Source: &simpb.ModelSource{
					Name:          name,
					Format:        format,
					MenageriePath: menageriePath,
				}},
			}); err != nil {
				return fmt.Errorf("sending model source: %w", err)
			}

			if localPath != "" {
				sendErr := simutil.StreamLocalModel(localPath, func(data []byte) error {
					return stream.Send(&simpb.LoadModelChunk{
						Payload: &simpb.LoadModelChunk_Data{Data: data},
					})
				})
				// A local read error aborts the import (the broken stream is
				// torn down with the connection). Send returning io.EOF means
				// the stream broke; CloseAndRecv below surfaces the cause.
				if sendErr != nil && sendErr != io.EOF {
					return fmt.Errorf("reading model %s: %w", localPath, sendErr)
				}
			}

			resp, err := stream.CloseAndRecv()
			if err != nil {
				return fmt.Errorf("importing model: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"modelId": resp.GetModelId()})
			}
			cliSuccess("Model %s imported.", name)
			cliLogln("  Model ID: %s", resp.GetModelId())
			return nil
		},
	}

	cmd.Flags().StringVar(&menageriePath, "menagerie", "", "Bundled MuJoCo Menagerie model path (e.g. unitree_go2/go2.xml)")
	cmd.Flags().StringVar(&name, "name", "", "Registry name for the model (e.g. go2)")
	cmd.Flags().StringVar(&formatFlag, "format", "", "Model format: mjcf, sdf, or urdf (default: auto-detect)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newSimDescribeModelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe-model <model-id>",
		Short: "Show the capability map of an imported model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.DescribeModel(ctx, &simpb.DescribeModelRequest{ModelId: args[0]})
			if err != nil {
				return fmt.Errorf("describing model: %w", err)
			}
			caps := resp.GetCapabilities()

			levels := make([]string, len(caps.GetSupportedControlLevels()))
			for i, l := range caps.GetSupportedControlLevels() {
				levels[i] = simutil.ControlLevelName(l)
			}

			if jsonOutput {
				type jsonJoint struct {
					Name     string  `json:"name"`
					Type     string  `json:"type,omitempty"`
					RangeMin float64 `json:"rangeMin"`
					RangeMax float64 `json:"rangeMax"`
				}
				type jsonActuator struct {
					Name    string  `json:"name"`
					Joint   string  `json:"joint,omitempty"`
					CtrlMin float64 `json:"ctrlMin"`
					CtrlMax float64 `json:"ctrlMax"`
				}
				type jsonSensor struct {
					Name string `json:"name"`
					Type string `json:"type,omitempty"`
				}
				type jsonSafety struct {
					MaxLinearSpeedMps    float64 `json:"maxLinearSpeedMps"`
					MaxAngularSpeedRadps float64 `json:"maxAngularSpeedRadps"`
				}
				out := struct {
					Joints        []jsonJoint    `json:"joints,omitempty"`
					Actuators     []jsonActuator `json:"actuators,omitempty"`
					Sensors       []jsonSensor   `json:"sensors,omitempty"`
					Cameras       []string       `json:"cameras,omitempty"`
					ControlLevels []string       `json:"controlLevels,omitempty"`
					SafetyLimits  *jsonSafety    `json:"safetyLimits,omitempty"`
				}{
					Cameras:       caps.GetCameras(),
					ControlLevels: levels,
				}
				for _, j := range caps.GetJoints() {
					out.Joints = append(out.Joints, jsonJoint{
						Name: j.GetName(), Type: j.GetType(),
						RangeMin: j.GetRangeMin(), RangeMax: j.GetRangeMax(),
					})
				}
				for _, a := range caps.GetActuators() {
					out.Actuators = append(out.Actuators, jsonActuator{
						Name: a.GetName(), Joint: a.GetJoint(),
						CtrlMin: a.GetCtrlMin(), CtrlMax: a.GetCtrlMax(),
					})
				}
				for _, s := range caps.GetSensors() {
					out.Sensors = append(out.Sensors, jsonSensor{Name: s.GetName(), Type: s.GetType()})
				}
				if sl := caps.GetSafetyLimits(); sl != nil {
					out.SafetyLimits = &jsonSafety{
						MaxLinearSpeedMps:    sl.GetMaxLinearSpeedMps(),
						MaxAngularSpeedRadps: sl.GetMaxAngularSpeedRadps(),
					}
				}
				return printJSON(out)
			}

			cliLogln("Model %s", args[0])
			if len(caps.GetJoints()) > 0 {
				cliLogln("Joints:")
				headers := []string{"Name", "Type", "Range"}
				var rows [][]string
				for _, j := range caps.GetJoints() {
					rows = append(rows, []string{
						j.GetName(), j.GetType(),
						fmt.Sprintf("[%.3f, %.3f]", j.GetRangeMin(), j.GetRangeMax()),
					})
				}
				fmt.Print(tui.RenderTable(headers, rows))
			}
			if len(caps.GetActuators()) > 0 {
				cliLogln("Actuators:")
				headers := []string{"Name", "Joint", "Ctrl range"}
				var rows [][]string
				for _, a := range caps.GetActuators() {
					rows = append(rows, []string{
						a.GetName(), a.GetJoint(),
						fmt.Sprintf("[%.3f, %.3f]", a.GetCtrlMin(), a.GetCtrlMax()),
					})
				}
				fmt.Print(tui.RenderTable(headers, rows))
			}
			if len(caps.GetSensors()) > 0 {
				var names []string
				for _, s := range caps.GetSensors() {
					names = append(names, fmt.Sprintf("%s (%s)", s.GetName(), s.GetType()))
				}
				cliLogln("Sensors: %s", strings.Join(names, ", "))
			}
			if len(caps.GetCameras()) > 0 {
				cliLogln("Cameras: %s", strings.Join(caps.GetCameras(), ", "))
			}
			if len(levels) > 0 {
				cliLogln("Control levels: %s", strings.Join(levels, ", "))
			}
			if sl := caps.GetSafetyLimits(); sl != nil {
				cliLogln("Safety limits: %.2f m/s linear, %.2f rad/s angular",
					sl.GetMaxLinearSpeedMps(), sl.GetMaxAngularSpeedRadps())
			}
			return nil
		},
	}
}

func newSimSpawnCmd() *cobra.Command {
	var worldID, pos string

	cmd := &cobra.Command{
		Use:   "spawn <model-id>",
		Short: "Spawn an imported model into a world",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			pose, err := simutil.ParsePosition(pos)
			if err != nil {
				return err
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.SpawnRobot(ctx, &simpb.SpawnRequest{
				WorldId: worldID,
				ModelId: args[0],
				Pose:    pose,
			})
			if err != nil {
				return fmt.Errorf("spawning robot: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"robotId": resp.GetRobotId()})
			}
			cliSuccess("Robot spawned.")
			cliLogln("  Robot ID: %s", resp.GetRobotId())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID to spawn into")
	cmd.Flags().StringVar(&pos, "pos", "", "Spawn position as x,y,z in meters (default 0,0,0)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimCameraCmd() *cobra.Command {
	var worldID, robotID, cameraName, outputPath string
	var width, height uint32

	cmd := &cobra.Command{
		Use:   "camera",
		Short: "Capture a camera frame from the simulation",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.GetCameraFrame(ctx, &simpb.GetCameraFrameRequest{
				WorldId:    worldID,
				RobotId:    robotID,
				CameraName: cameraName,
				Width:      width,
				Height:     height,
			})
			if err != nil {
				return fmt.Errorf("capturing camera frame: %w", err)
			}

			path := outputPath
			if path == "" {
				path = fmt.Sprintf("frame-%s.%s", worldID, frameExtension(resp.GetEncoding()))
			}
			if err := os.WriteFile(path, resp.GetImage(), 0o644); err != nil {
				return fmt.Errorf("writing frame: %w", err)
			}

			if jsonOutput {
				return printJSON(struct {
					Path     string `json:"path"`
					Bytes    int    `json:"bytes"`
					Encoding string `json:"encoding"`
				}{Path: path, Bytes: len(resp.GetImage()), Encoding: resp.GetEncoding()})
			}
			cliSuccess("Frame saved to %s (%d bytes, %s)", path, len(resp.GetImage()), resp.GetEncoding())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID the tracking camera follows")
	cmd.Flags().StringVar(&cameraName, "camera", "", "Model camera name (default: tracking view)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file (default frame-<world>.<ext>)")
	cmd.Flags().Uint32Var(&width, "width", 640, "Frame width in pixels")
	cmd.Flags().Uint32Var(&height, "height", 480, "Frame height in pixels")
	_ = cmd.MarkFlagRequired("world")

	return cmd
}

// frameExtension maps a camera frame encoding to a file extension.
func frameExtension(encoding string) string {
	switch strings.ToLower(encoding) {
	case "jpeg", "jpg":
		return "jpg"
	default:
		return "png"
	}
}

func newSimStateCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "state",
		Short: "Show a robot's state (pose, joints, fallen)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.GetState(ctx, &simpb.GetStateRequest{
				WorldId: worldID,
				RobotId: robotID,
			})
			if err != nil {
				return fmt.Errorf("getting state: %w", err)
			}

			if jsonOutput {
				type jsonJointState struct {
					Name     string  `json:"name"`
					Position float64 `json:"position"`
					Velocity float64 `json:"velocity"`
				}
				type jsonPose struct {
					X  float64 `json:"x"`
					Y  float64 `json:"y"`
					Z  float64 `json:"z"`
					Qw float64 `json:"qw"`
					Qx float64 `json:"qx"`
					Qy float64 `json:"qy"`
					Qz float64 `json:"qz"`
				}
				out := struct {
					BasePose *jsonPose        `json:"basePose,omitempty"`
					Joints   []jsonJointState `json:"joints,omitempty"`
					SimTimeS float64          `json:"simTimeS"`
					Fallen   bool             `json:"fallen"`
				}{SimTimeS: resp.GetSimTimeS(), Fallen: resp.GetFallen()}
				if p := resp.GetBasePose(); p != nil {
					out.BasePose = &jsonPose{
						X: p.GetX(), Y: p.GetY(), Z: p.GetZ(),
						Qw: p.GetQw(), Qx: p.GetQx(), Qy: p.GetQy(), Qz: p.GetQz(),
					}
				}
				for _, j := range resp.GetJoints() {
					out.Joints = append(out.Joints, jsonJointState{
						Name: j.GetName(), Position: j.GetPosition(), Velocity: j.GetVelocity(),
					})
				}
				return printJSON(out)
			}

			if p := resp.GetBasePose(); p != nil {
				cliLogln("Position:    (%.3f, %.3f, %.3f) m", p.GetX(), p.GetY(), p.GetZ())
				cliLogln("Orientation: (%.3f, %.3f, %.3f, %.3f) wxyz", p.GetQw(), p.GetQx(), p.GetQy(), p.GetQz())
			}
			cliLogln("Sim time:    %.3f s", resp.GetSimTimeS())
			if resp.GetFallen() {
				cliNotice("Robot has FALLEN")
			} else {
				cliLogln("Fallen:      no")
			}
			if len(resp.GetJoints()) > 0 {
				headers := []string{"Joint", "Position", "Velocity"}
				var rows [][]string
				for _, j := range resp.GetJoints() {
					rows = append(rows, []string{
						j.GetName(),
						fmt.Sprintf("%.4f", j.GetPosition()),
						fmt.Sprintf("%.4f", j.GetVelocity()),
					})
				}
				fmt.Print(tui.RenderTable(headers, rows))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset <world-id>",
		Short: "Reset a simulation world to its initial state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.ResetSimulation(ctx, &agentpbv2.ResetSimulationRequest{
				WorldId: args[0],
			}); err != nil {
				return fmt.Errorf("resetting simulation: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"worldId": args[0], "status": "reset"})
			}
			cliSuccess("Simulation %s reset.", args[0])
			return nil
		},
	}
}

func newSimRunCmd() *cobra.Command {
	var worldID, robotID, controlLevel, outputPath string
	var record bool

	cmd := &cobra.Command{
		Use:   "run <task.yaml>",
		Short: "Run a task spec in a simulation and report the result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			level, err := simutil.ParseControlLevel(controlLevel)
			if err != nil {
				return err
			}

			specData, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading task spec: %w", err)
			}
			if len(bytes.TrimSpace(specData)) == 0 {
				return fmt.Errorf("task spec %s is empty", args[0])
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := conn.SimService.RunTask(ctx, &agentpbv2.RunSimTaskRequest{
				Task: &simpb.RunTaskRequest{
					WorldId:         worldID,
					RobotId:         robotID,
					SpecYaml:        string(specData),
					MaxControlLevel: level,
					Record:          record,
				},
				SessionControlLevel: level,
			})
			if err != nil {
				return fmt.Errorf("running task: %w", err)
			}

			var result *simpb.TaskResult
			for {
				ev, recvErr := stream.Recv()
				if recvErr == io.EOF {
					break
				}
				if recvErr != nil {
					return fmt.Errorf("running task: %w", recvErr)
				}
				if res := ev.GetResult(); res != nil {
					result = res // rendered after the stream ends
					continue
				}
				if jsonOutput {
					if line, ok := taskEventJSONLine(ev); ok {
						fmt.Println(line)
					}
					continue
				}
				switch e := ev.GetEvent().(type) {
				case *simpb.TaskEvent_Progress:
					cliLogln("[%7.2fs] %s", e.Progress.GetSimTimeS(), e.Progress.GetObjective())
				case *simpb.TaskEvent_Log:
					cliNotice("%s", e.Log.GetMessage())
				}
			}
			if result == nil {
				return fmt.Errorf("task stream ended without a result")
			}

			if jsonOutput {
				if err := printJSON(taskResultJSON(result)); err != nil {
					return err
				}
			} else {
				fmt.Print(renderTaskResult(result))
			}

			if record && result.GetReplayId() != "" {
				path := outputPath
				if path == "" {
					path = fmt.Sprintf("replay-%s.rrd", result.GetReplayId())
				}
				n, err := simutil.DownloadReplay(ctx, conn.SimService, result.GetReplayId(), path)
				if err != nil {
					return fmt.Errorf("downloading replay: %w", err)
				}
				if jsonOutput {
					if err := printJSON(map[string]any{
						"replayId": result.GetReplayId(),
						"path":     path,
						"bytes":    n,
					}); err != nil {
						return err
					}
				} else {
					cliSuccess("Replay saved to %s (%d bytes)", path, n)
				}
			}

			if !result.GetSuccess() {
				if summary := result.GetSummary(); summary != "" {
					return fmt.Errorf("task failed: %s", summary)
				}
				return fmt.Errorf("task failed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID to run the task in")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID to drive")
	cmd.Flags().BoolVar(&record, "record", true, "Record a replay of the run")
	cmd.Flags().StringVar(&controlLevel, "control-level", "motion",
		"Highest control level the task may use (task, motion, joint, physics)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "",
		"Replay download path (default ./replay-<replay-id>.rrd)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimReplayCmd() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "replay <replay-id>",
		Short: "Download a recorded replay (.rrd) from the simulation backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			path := outputPath
			if path == "" {
				path = fmt.Sprintf("replay-%s.rrd", args[0])
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			n, err := simutil.DownloadReplay(ctx, conn.SimService, args[0], path)
			if err != nil {
				return fmt.Errorf("downloading replay: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"replayId": args[0],
					"path":     path,
					"bytes":    n,
				})
			}
			cliSuccess("Replay %s saved to %s (%d bytes)", args[0], path, n)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "",
		"Output file path (default ./replay-<replay-id>.rrd)")
	return cmd
}

// --- helpers ---

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// validateImportModelSource enforces exactly one model source: a bundled
// Menagerie path or a local model path.
func validateImportModelSource(menageriePath, localPath string) error {
	switch {
	case menageriePath == "" && localPath == "":
		return fmt.Errorf("specify a model source: --menagerie <path> or a local model directory")
	case menageriePath != "" && localPath != "":
		return fmt.Errorf("--menagerie and a local model directory are mutually exclusive")
	}
	return nil
}

// taskEventJSONLine renders a progress or log TaskEvent as a compact JSON
// line for --json streaming output. Result events return ok=false — the final
// result is printed as a JSON object instead.
func taskEventJSONLine(ev *simpb.TaskEvent) (string, bool) {
	var v any
	switch e := ev.GetEvent().(type) {
	case *simpb.TaskEvent_Progress:
		v = struct {
			Event     string  `json:"event"`
			Objective string  `json:"objective,omitempty"`
			SimTimeS  float64 `json:"simTimeS"`
		}{Event: "progress", Objective: e.Progress.GetObjective(), SimTimeS: e.Progress.GetSimTimeS()}
	case *simpb.TaskEvent_Log:
		v = struct {
			Event   string `json:"event"`
			Message string `json:"message"`
		}{Event: "log", Message: e.Log.GetMessage()}
	default:
		return "", false
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// jsonTaskResult is the --json shape of a simpb.TaskResult.
type jsonTaskResult struct {
	Success           bool            `json:"success"`
	Fell              bool            `json:"fell"`
	CollisionCount    uint32          `json:"collisionCount"`
	DistanceTraveledM float64         `json:"distanceTraveledM"`
	Checks            []jsonTaskCheck `json:"checks,omitempty"`
	ReplayID          string          `json:"replayId,omitempty"`
	Summary           string          `json:"summary,omitempty"`
}

type jsonTaskCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

func taskResultJSON(res *simpb.TaskResult) jsonTaskResult {
	out := jsonTaskResult{
		Success:           res.GetSuccess(),
		Fell:              res.GetFell(),
		CollisionCount:    res.GetCollisionCount(),
		DistanceTraveledM: res.GetDistanceTraveledM(),
		ReplayID:          res.GetReplayId(),
		Summary:           res.GetSummary(),
	}
	for _, c := range res.GetChecks() {
		out.Checks = append(out.Checks, jsonTaskCheck{
			Name: c.GetName(), Passed: c.GetPassed(), Detail: c.GetDetail(),
		})
	}
	return out
}

// renderTaskResult renders the human-readable summary block for a TaskResult.
func renderTaskResult(res *simpb.TaskResult) string {
	var b strings.Builder
	if res.GetSuccess() {
		b.WriteString("Task PASSED\n")
	} else {
		b.WriteString("Task FAILED\n")
	}
	fell := "no"
	if res.GetFell() {
		fell = "yes"
	}
	fmt.Fprintf(&b, "  Fell:       %s\n", fell)
	fmt.Fprintf(&b, "  Distance:   %.2f m\n", res.GetDistanceTraveledM())
	fmt.Fprintf(&b, "  Collisions: %d\n", res.GetCollisionCount())
	if len(res.GetChecks()) > 0 {
		b.WriteString("  Checks:\n")
		for _, c := range res.GetChecks() {
			mark := "✓"
			if !c.GetPassed() {
				mark = "✗"
			}
			fmt.Fprintf(&b, "    %s %s", mark, c.GetName())
			if c.GetDetail() != "" {
				fmt.Fprintf(&b, " — %s", c.GetDetail())
			}
			b.WriteByte('\n')
		}
	}
	if res.GetSummary() != "" {
		fmt.Fprintf(&b, "  Summary: %s\n", res.GetSummary())
	}
	return b.String()
}
