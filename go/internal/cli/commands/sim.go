package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

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
		newSimWatchCmd(),
		newSimDriveCmd(),
		newSimCameraCmd(),
		newSimViewerCmd(),
		newSimResetCmd(),
		newSimRunCmd(),
		newSimReplayCmd(),
		newSimPauseCmd(),
		newSimResumeCmd(),
		newSimSpeedCmd(),
		newSimPushCmd(),
		newSimTeleportCmd(),
		newSimSnapshotCmd(),
		newSimSensorsCmd(),
		newSimSceneCmd(),
		newSimPolicyCmd(),
		newSimRecordCmd(),
		newSimJointsCmd(),
		newSimStepCmd(),
		newSimEvalCmd(),
		newSimTeleopCmd(),
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
	var menageriePath, name, formatFlag, replaceModelID string

	cmd := &cobra.Command{
		Use:   "import-model [local-dir]",
		Short: "Import a robot model into the simulation backend",
		Long: "Import a robot model into the simulation backend.\n\n" +
			"Either reference a bundled MuJoCo Menagerie model (--menagerie unitree_go2/go2.xml)\n" +
			"or upload a local model directory (or .tar/.tar.gz archive of one).\n" +
			"The model format (mjcf, sdf, urdf) is auto-detected from the file\n" +
			"extensions in a local source; override with --format. Which formats\n" +
			"load depends on the backend (MuJoCo: mjcf; Gazebo: sdf, urdf).\n\n" +
			"Pass --replace <model-id> to reload an existing model in place: robots\n" +
			"spawned from it are respawned against the new model (the edit-reload loop).",
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
					Name:           name,
					Format:         format,
					MenageriePath:  menageriePath,
					ReplaceModelId: replaceModelID,
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
	cmd.Flags().StringVar(&replaceModelID, "replace", "",
		"Replace this existing model in place (robots spawned from it are respawned)")
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

func newSimDriveCmd() *cobra.Command {
	var worldID, robotID string
	var vx, vy, yaw float64
	var stop bool

	cmd := &cobra.Command{
		Use:   "drive",
		Short: "Command a base velocity (the robot keeps it until told otherwise)",
		Long: "Command a base velocity for a simulated robot.\n\n" +
			"The controller holds the command until the next drive (or a task takes\n" +
			"over); the backend clamps it to the model's safety limits. Use --stop\n" +
			"to halt.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if stop {
				vx, vy, yaw = 0, 0, 0
			}
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.SetVelocity(ctx, &simpb.SetVelocityRequest{
				WorldId:      worldID,
				RobotId:      robotID,
				VxMps:        vx,
				VyMps:        vy,
				YawRateRadps: yaw,
			}); err != nil {
				return fmt.Errorf("driving robot: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{"vx": vx, "vy": vy, "yawRate": yaw})
			}
			if stop {
				cliSuccess("Robot %s stopping.", robotID)
			} else {
				cliSuccess("Driving %s: vx=%.2f m/s, vy=%.2f m/s, yaw=%.2f rad/s", robotID, vx, vy, yaw)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().Float64Var(&vx, "vx", 0, "Forward velocity in m/s (negative = backward)")
	cmd.Flags().Float64Var(&vy, "vy", 0, "Lateral velocity in m/s")
	cmd.Flags().Float64Var(&yaw, "yaw", 0, "Turn rate in rad/s (positive = counter-clockwise)")
	cmd.Flags().BoolVar(&stop, "stop", false, "Halt the robot (zero all velocities)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimWatchCmd() *cobra.Command {
	var worldID, robotID string
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Live-updating robot state (Ctrl-C to exit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			for {
				resp, err := conn.SimService.GetState(ctx, &simpb.GetStateRequest{
					WorldId: worldID,
					RobotId: robotID,
				})
				if err != nil {
					return fmt.Errorf("getting state: %w", err)
				}
				if jsonOutput {
					fmt.Println(watchJSONLine(resp))
				} else {
					// Home + clear-to-end keeps the display in place without
					// flicker; the frame always ends in a newline.
					fmt.Print("\033[H\033[2J" + renderWatchFrame(worldID, robotID, resp))
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().DurationVar(&interval, "interval", 500*time.Millisecond, "Refresh interval")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

// renderWatchFrame renders one sim watch frame: pose, status, and joints in
// columns.
func renderWatchFrame(worldID, robotID string, resp *simpb.GetStateResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "wendy sim watch — world %s, robot %s (Ctrl-C to exit)\n\n", worldID, robotID)
	if p := resp.GetBasePose(); p != nil {
		fmt.Fprintf(&b, "  Position   x %+7.3f   y %+7.3f   z %+7.3f m\n", p.GetX(), p.GetY(), p.GetZ())
	}
	fmt.Fprintf(&b, "  Sim time   %.2f s\n", resp.GetSimTimeS())
	if resp.GetFallen() {
		b.WriteString("  Status     FALLEN\n")
	} else {
		b.WriteString("  Status     upright\n")
	}
	if joints := resp.GetJoints(); len(joints) > 0 {
		b.WriteString("\n  Joint                 Position   Velocity\n")
		for _, j := range joints {
			fmt.Fprintf(&b, "  %-20s  %+8.3f   %+8.3f\n", j.GetName(), j.GetPosition(), j.GetVelocity())
		}
	}
	return b.String()
}

// watchJSONLine renders one sim watch sample as a compact JSON line.
func watchJSONLine(resp *simpb.GetStateResponse) string {
	out := map[string]any{
		"simTimeS": resp.GetSimTimeS(),
		"fallen":   resp.GetFallen(),
	}
	if p := resp.GetBasePose(); p != nil {
		out["x"], out["y"], out["z"] = p.GetX(), p.GetY(), p.GetZ()
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func newSimViewerCmd() *cobra.Command {
	var urlFlag string
	var port int

	cmd := &cobra.Command{
		Use:   "viewer",
		Short: "Open the live sim viewer in the browser",
		Long: "Open the sim backend's live Rerun web viewer in the browser.\n\n" +
			"The viewer is served by the sim container when it runs with\n" +
			"WENDYSIM_LIVE_VIEWER=1; the host defaults to the connected device.",
		RunE: func(cmd *cobra.Command, args []string) error {
			viewerURL := urlFlag
			if viewerURL == "" {
				host := "localhost"
				if deviceFlag != "" {
					host = deviceFlag
					if h, _, err := net.SplitHostPort(deviceFlag); err == nil {
						host = h
					}
				}
				viewerURL = fmt.Sprintf("http://%s:%d/?url=rerun%%2Bhttp://%s:%d/proxy",
					host, port, host, port-1)
			}
			if jsonOutput {
				return printJSON(map[string]string{"url": viewerURL})
			}
			cliLogln("Opening %s", viewerURL)
			if err := openBrowser(viewerURL); err != nil {
				cliNotice("Could not open a browser: %v", err)
				cliLogln("Open it manually: %s", viewerURL)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&urlFlag, "url", "", "Viewer URL (default: derived from the device host)")
	cmd.Flags().IntVar(&port, "port", 9877, "Viewer web port on the sim host (gRPC proxy is port-1)")
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

// --- Interactive tooling ---

// setClock sends a SetClock request and renders the resulting clock state.
func setClock(cmd *cobra.Command, worldID string, paused bool, speedFactor float64) error {
	ctx := cmd.Context()
	conn, err := connectToAgent(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := conn.SimService.SetClock(ctx, &simpb.SetClockRequest{
		WorldId:     worldID,
		Paused:      paused,
		SpeedFactor: speedFactor,
	})
	if err != nil {
		return fmt.Errorf("setting sim clock: %w", err)
	}

	if jsonOutput {
		return printJSON(map[string]any{
			"paused":      resp.GetPaused(),
			"speedFactor": resp.GetSpeedFactor(),
		})
	}
	state := "running"
	if resp.GetPaused() {
		state = "paused"
	}
	cliSuccess("Simulation %s %s (speed %.2fx).", worldID, state, resp.GetSpeedFactor())
	return nil
}

func newSimPauseCmd() *cobra.Command {
	var worldID string

	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause a simulation world's physics",
		Long: "Pause a simulation world's physics stepping.\n\n" +
			"While paused, advance time deterministically with `wendy sim step`;\n" +
			"resume real-time stepping with `wendy sim resume`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setClock(cmd, worldID, true, 0)
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimResumeCmd() *cobra.Command {
	var worldID string

	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume a paused simulation world",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setClock(cmd, worldID, false, 0)
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimSpeedCmd() *cobra.Command {
	var worldID string

	cmd := &cobra.Command{
		Use:   "speed <factor>",
		Short: "Set a simulation world's real-time pacing (1 = real time)",
		Long: "Set a simulation world's real-time pacing multiplier: 1 = real time,\n" +
			"10 = 10x fast-forward, 0.5 = half speed. Setting a speed also resumes\n" +
			"a paused world.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			factor, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				return fmt.Errorf("invalid speed factor %q: expected a number", args[0])
			}
			if factor <= 0 {
				return fmt.Errorf("speed factor must be positive (got %v)", factor)
			}
			return setClock(cmd, worldID, false, factor)
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimPushCmd() *cobra.Command {
	var worldID, robotID, force string
	var duration float64

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Apply a force impulse to a robot's base (balance testing)",
		Long: "Apply a world-frame force to a robot's base — the programmatic version\n" +
			"of shoving it in a viewer, used to test balance and recovery.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			f, err := simutil.ParseVector3(force, "force")
			if err != nil {
				return err
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.ApplyForce(ctx, &simpb.ApplyForceRequest{
				WorldId:   worldID,
				RobotId:   robotID,
				FxN:       f[0],
				FyN:       f[1],
				FzN:       f[2],
				DurationS: duration,
			}); err != nil {
				return fmt.Errorf("applying force: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"fx": f[0], "fy": f[1], "fz": f[2], "durationS": duration,
				})
			}
			cliSuccess("Pushed %s: force (%.1f, %.1f, %.1f) N for %.2f s.", robotID, f[0], f[1], f[2], duration)
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().StringVar(&force, "force", "", "World-frame force as x,y,z in Newtons (required)")
	cmd.Flags().Float64Var(&duration, "duration", 0.1, "How long the force is held, in sim seconds")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	_ = cmd.MarkFlagRequired("force")
	return cmd
}

func newSimTeleportCmd() *cobra.Command {
	var worldID, robotID, pos string

	cmd := &cobra.Command{
		Use:   "teleport",
		Short: "Move a robot to a position directly (velocities are zeroed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			pose, err := simutil.ParsePosition(pos)
			if err != nil {
				return err
			}
			if pose == nil {
				return fmt.Errorf("--pos is required (x,y,z in meters)")
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.Teleport(ctx, &simpb.TeleportRequest{
				WorldId:      worldID,
				RobotId:      robotID,
				Pose:         pose,
				ZeroVelocity: true,
			}); err != nil {
				return fmt.Errorf("teleporting robot: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"x": pose.GetX(), "y": pose.GetY(), "z": pose.GetZ(),
				})
			}
			cliSuccess("Teleported %s to (%.3f, %.3f, %.3f).", robotID, pose.GetX(), pose.GetY(), pose.GetZ())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().StringVar(&pos, "pos", "", "Target position as x,y,z in meters (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	_ = cmd.MarkFlagRequired("pos")
	return cmd
}

func newSimSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Save and restore exact physics-state snapshots of a world",
	}
	cmd.AddCommand(newSimSnapshotSaveCmd(), newSimSnapshotRestoreCmd())
	return cmd
}

func newSimSnapshotSaveCmd() *cobra.Command {
	var worldID string

	cmd := &cobra.Command{
		Use:   "save",
		Short: "Capture a world's exact physics state",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.SaveSnapshot(ctx, &simpb.SaveSnapshotRequest{WorldId: worldID})
			if err != nil {
				return fmt.Errorf("saving snapshot: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"snapshotId": resp.GetSnapshotId()})
			}
			cliSuccess("Snapshot saved.")
			cliLogln("  Snapshot ID: %s", resp.GetSnapshotId())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimSnapshotRestoreCmd() *cobra.Command {
	var worldID string

	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Rewind a world to a saved snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.RestoreSnapshot(ctx, &simpb.RestoreSnapshotRequest{
				WorldId:    worldID,
				SnapshotId: args[0],
			}); err != nil {
				return fmt.Errorf("restoring snapshot: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"snapshotId": args[0], "status": "restored"})
			}
			cliSuccess("Snapshot %s restored.", args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	_ = cmd.MarkFlagRequired("world")
	return cmd
}

func newSimSensorsCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "sensors",
		Short: "Read a robot's declared sensors (IMU, force, touch, ...)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.ReadSensors(ctx, &simpb.ReadSensorsRequest{
				WorldId: worldID,
				RobotId: robotID,
			})
			if err != nil {
				return fmt.Errorf("reading sensors: %w", err)
			}

			if jsonOutput {
				type jsonReading struct {
					Name   string    `json:"name"`
					Type   string    `json:"type,omitempty"`
					Values []float64 `json:"values"`
				}
				readings := make([]jsonReading, len(resp.GetReadings()))
				for i, r := range resp.GetReadings() {
					readings[i] = jsonReading{Name: r.GetName(), Type: r.GetType(), Values: r.GetValues()}
				}
				return printJSON(readings)
			}

			if len(resp.GetReadings()) == 0 {
				cliLogln("No sensor readings (does the model declare sensors?).")
				return nil
			}
			headers := []string{"Sensor", "Type", "Values"}
			var rows [][]string
			for _, r := range resp.GetReadings() {
				values := make([]string, len(r.GetValues()))
				for i, v := range r.GetValues() {
					values[i] = fmt.Sprintf("%.4f", v)
				}
				rows = append(rows, []string{r.GetName(), r.GetType(), strings.Join(values, ", ")})
			}
			fmt.Print(tui.RenderTable(headers, rows))
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimSceneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scene",
		Short: "Edit a live world's static scenery",
	}
	cmd.AddCommand(newSimSceneAddBoxCmd(), newSimSceneRemoveCmd())
	return cmd
}

func newSimSceneAddBoxCmd() *cobra.Command {
	var worldID, id, pos, size string

	cmd := &cobra.Command{
		Use:   "add-box",
		Short: "Add a static box obstacle to a live world",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			p, err := simutil.ParseVector3(pos, "position")
			if err != nil {
				return err
			}
			sz, err := simutil.ParseVector3(size, "size")
			if err != nil {
				return err
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.EditScene(ctx, &simpb.EditSceneRequest{
				WorldId: worldID,
				Op: &simpb.EditSceneRequest_AddBox{AddBox: &simpb.SceneBoxSpec{
					Id:       id,
					Position: p[:],
					Size:     sz[:],
				}},
			}); err != nil {
				return fmt.Errorf("adding box: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{"id": id, "position": p[:], "size": sz[:]})
			}
			cliSuccess("Box %s added at (%.2f, %.2f, %.2f).", id, p[0], p[1], p[2])
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&id, "id", "", "Obstacle ID (required; used to remove it later)")
	cmd.Flags().StringVar(&pos, "pos", "", "Center position as x,y,z in meters (required)")
	cmd.Flags().StringVar(&size, "size", "", "Full extents as x,y,z in meters (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("pos")
	_ = cmd.MarkFlagRequired("size")
	return cmd
}

func newSimSceneRemoveCmd() *cobra.Command {
	var worldID, id string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an obstacle from a live world",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.EditScene(ctx, &simpb.EditSceneRequest{
				WorldId: worldID,
				Op:      &simpb.EditSceneRequest_RemoveId{RemoveId: id},
			}); err != nil {
				return fmt.Errorf("removing obstacle: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"id": id, "status": "removed"})
			}
			cliSuccess("Obstacle %s removed.", id)
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&id, "id", "", "Obstacle ID to remove (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newSimPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Load and clear trained control policies on a simulated robot",
	}
	cmd.AddCommand(newSimPolicyLoadCmd(), newSimPolicyClearCmd())
	return cmd
}

func newSimPolicyLoadCmd() *cobra.Command {
	var worldID, robotID, format string

	cmd := &cobra.Command{
		Use:   "load <policy-file>",
		Short: "Load a trained policy (e.g. ONNX) that replaces the built-in controller",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if _, err := os.Stat(args[0]); err != nil {
				return fmt.Errorf("opening policy file: %w", err)
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := conn.SimService.LoadPolicy(ctx)
			if err != nil {
				return fmt.Errorf("loading policy: %w", err)
			}
			if err := stream.Send(&simpb.LoadPolicyChunk{
				Payload: &simpb.LoadPolicyChunk_Source{Source: &simpb.PolicySource{
					WorldId: worldID,
					RobotId: robotID,
					Format:  format,
				}},
			}); err != nil {
				return fmt.Errorf("sending policy source: %w", err)
			}

			sendErr := simutil.StreamFileChunks(args[0], func(data []byte) error {
				return stream.Send(&simpb.LoadPolicyChunk{
					Payload: &simpb.LoadPolicyChunk_Data{Data: data},
				})
			})
			// A local read error aborts the load. Send returning io.EOF means
			// the stream broke; CloseAndRecv below surfaces the cause.
			if sendErr != nil && sendErr != io.EOF {
				return fmt.Errorf("reading policy %s: %w", args[0], sendErr)
			}

			resp, err := stream.CloseAndRecv()
			if err != nil {
				return fmt.Errorf("loading policy: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"policyId": resp.GetPolicyId()})
			}
			cliSuccess("Policy loaded onto %s.", robotID)
			cliLogln("  Policy ID: %s", resp.GetPolicyId())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().StringVar(&format, "format", "onnx", "Policy file format")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimPolicyClearCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Revert a robot to its built-in controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.ClearPolicy(ctx, &simpb.ClearPolicyRequest{
				WorldId: worldID,
				RobotId: robotID,
			}); err != nil {
				return fmt.Errorf("clearing policy: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]string{"robotId": robotID, "status": "cleared"})
			}
			cliSuccess("Policy cleared; %s uses its built-in controller.", robotID)
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimRecordCmd() *cobra.Command {
	var worldID, robotID, cameraName, outputPath string
	var duration float64
	var fps, width, height uint32

	cmd := &cobra.Command{
		Use:   "record",
		Short: "Render a video clip (mp4) of the simulation",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := conn.SimService.RenderVideo(ctx, &simpb.RenderVideoRequest{
				WorldId:    worldID,
				RobotId:    robotID,
				CameraName: cameraName,
				DurationS:  duration,
				Fps:        fps,
				Width:      width,
				Height:     height,
			})
			if err != nil {
				return fmt.Errorf("rendering video: %w", err)
			}

			path := outputPath
			if path == "" {
				path = fmt.Sprintf("clip-%s.mp4", worldID)
			}
			n, err := simutil.WriteReplayFile(path, func() ([]byte, error) {
				chunk, recvErr := stream.Recv()
				if recvErr != nil {
					return nil, recvErr
				}
				return chunk.GetData(), nil
			})
			if err != nil {
				return fmt.Errorf("rendering video: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{"path": path, "bytes": n})
			}
			cliSuccess("Video saved to %s (%d bytes).", path, n)
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID the tracking camera follows (required)")
	cmd.Flags().StringVar(&cameraName, "camera", "", "Model camera name (default: tracking view)")
	cmd.Flags().Float64Var(&duration, "duration", 5, "Clip length in sim seconds")
	cmd.Flags().Uint32Var(&fps, "fps", 15, "Frames per second")
	cmd.Flags().Uint32Var(&width, "width", 640, "Frame width in pixels")
	cmd.Flags().Uint32Var(&height, "height", 480, "Frame height in pixels")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file (default clip-<world>.mp4)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimJointsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "joints",
		Short: "Read and command a robot's joints (joint level, expert-gated)",
	}
	cmd.AddCommand(newSimJointsGetCmd(), newSimJointsSetCmd())
	return cmd
}

func newSimJointsGetCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a robot's joint positions and velocities",
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
				joints := make([]jsonJointState, len(resp.GetJoints()))
				for i, j := range resp.GetJoints() {
					joints[i] = jsonJointState{Name: j.GetName(), Position: j.GetPosition(), Velocity: j.GetVelocity()}
				}
				return printJSON(joints)
			}

			if len(resp.GetJoints()) == 0 {
				cliLogln("No joints reported.")
				return nil
			}
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
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

func newSimJointsSetCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "set <joint>=<target> [<joint>=<target>...]",
		Short: "Command joint position targets (joint level, expert-gated)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			targets, err := parseJointTargets(args)
			if err != nil {
				return err
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			if _, err := conn.SimService.SetJointTargets(ctx, &simpb.SetJointTargetsRequest{
				WorldId: worldID,
				RobotId: robotID,
				Targets: targets,
			}); err != nil {
				return fmt.Errorf("setting joint targets: %w", err)
			}

			if jsonOutput {
				return printJSON(targets)
			}
			cliSuccess("Set %d joint target(s) on %s.", len(targets), robotID)
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

// parseJointTargets parses "name=value" args into a joint-target map.
func parseJointTargets(args []string) (map[string]float64, error) {
	targets := make(map[string]float64, len(args))
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid joint target %q: expected <joint>=<value>", arg)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid joint target %q: %q is not a number", arg, value)
		}
		targets[name] = v
	}
	return targets, nil
}

func newSimStepCmd() *cobra.Command {
	var worldID string
	var seconds float64

	cmd := &cobra.Command{
		Use:   "step",
		Short: "Advance sim time deterministically (pairs with `wendy sim pause`)",
		Long: "Advance a world's simulation time by a fixed amount and return the new\n" +
			"sim time. Deterministic debugging tool: pause the world first\n" +
			"(`wendy sim pause`), then step it in controlled increments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.SimService.Step(ctx, &simpb.StepRequest{
				WorldId: worldID,
				Seconds: seconds,
			})
			if err != nil {
				return fmt.Errorf("stepping simulation: %w", err)
			}

			if jsonOutput {
				return printJSON(map[string]any{"simTimeS": resp.GetSimTimeS()})
			}
			cliSuccess("Stepped %.3f s; sim time is now %.3f s.", seconds, resp.GetSimTimeS())
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().Float64Var(&seconds, "seconds", 1, "Sim seconds to advance")
	_ = cmd.MarkFlagRequired("world")
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
