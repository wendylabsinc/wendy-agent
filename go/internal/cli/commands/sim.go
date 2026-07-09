package commands

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

// simModelChunkSize is the data-chunk size for streaming model archives to
// the agent; small enough to stay well under gRPC message limits.
const simModelChunkSize = 64 * 1024

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
		newSimResetCmd(),
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
	var menageriePath, name string

	cmd := &cobra.Command{
		Use:   "import-model [local-dir]",
		Short: "Import a robot model into the simulation backend",
		Long: "Import a robot model into the simulation backend.\n\n" +
			"Either reference a bundled MuJoCo Menagerie model (--menagerie unitree_go2/go2.xml)\n" +
			"or upload a local MJCF model directory (or .tar/.tar.gz archive of one).",
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
					Format:        simpb.ModelFormat_MODEL_FORMAT_MJCF,
					MenageriePath: menageriePath,
				}},
			}); err != nil {
				return fmt.Errorf("sending model source: %w", err)
			}

			if localPath != "" {
				sendErr := streamLocalModel(localPath, func(data []byte) error {
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
				levels[i] = controlLevelName(l)
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

			pose, err := parseSimPosition(pos)
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

// controlLevelName renders a simpb.ControlLevel as a short lowercase name
// (e.g. "motion").
func controlLevelName(l simpb.ControlLevel) string {
	return strings.ToLower(strings.TrimPrefix(l.String(), "CONTROL_LEVEL_"))
}

// parseSimPosition parses an "x,y,z" position (meters) into a Pose with
// identity orientation. An empty string yields nil (backend default pose).
func parseSimPosition(pos string) (*simpb.Pose, error) {
	if pos == "" {
		return nil, nil
	}
	parts := strings.Split(pos, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid --pos %q: expected x,y,z", pos)
	}
	coords := make([]float64, 3)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid --pos %q: %q is not a number", pos, strings.TrimSpace(p))
		}
		coords[i] = v
	}
	// Identity orientation (qw=1); the proto also treats an all-zero
	// quaternion as identity, but being explicit costs nothing.
	return &simpb.Pose{X: coords[0], Y: coords[1], Z: coords[2], Qw: 1}, nil
}

// streamLocalModel streams a local model as tar data chunks. A directory is
// tarred on the fly; a file is streamed as-is, transparently gunzipping
// .tar.gz/.tgz archives so the wire always carries a plain tar.
func streamLocalModel(path string, send func([]byte) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(tarDirectory(path, pw))
		}()
		if err := sendDataChunks(pr, send); err != nil {
			_ = pr.CloseWithError(err)
			return err
		}
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	var r io.Reader = br
	// Accept gzipped archives: sniff the gzip magic and decompress so the
	// backend always receives a plain tar stream.
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("reading gzip archive: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	return sendDataChunks(r, send)
}

// tarDirectory writes a tar archive of dir to w. Entry names are relative to
// dir (forward-slashed); only directories and regular files are archived.
func tarDirectory(dir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // skip symlinks, sockets, etc.
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// sendDataChunks reads r and calls send with successive chunks of at most
// simModelChunkSize bytes.
func sendDataChunks(r io.Reader, send func([]byte) error) error {
	buf := make([]byte, simModelChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := send(chunk); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
