package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/simutil"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

func newSimEvalCmd() *cobra.Command {
	var worldID, robotID, controlLevel string
	var episodes int

	cmd := &cobra.Command{
		Use:   "eval <task.yaml>",
		Short: "Evaluate a task spec over multiple episodes and report statistics",
		Long: "Run a task spec repeatedly (resetting the world before each episode) and\n" +
			"report the success rate, distance statistics, and fall count. Exits\n" +
			"non-zero when no episode succeeds.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if episodes < 1 {
				return fmt.Errorf("--episodes must be at least 1 (got %d)", episodes)
			}
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

			results := make([]*simpb.TaskResult, 0, episodes)
			for i := 0; i < episodes; i++ {
				if _, err := conn.SimService.ResetSimulation(ctx, &agentpbv2.ResetSimulationRequest{
					WorldId: worldID,
				}); err != nil {
					return fmt.Errorf("resetting simulation before episode %d: %w", i+1, err)
				}

				result, err := runEvalEpisode(ctx, conn.SimService, worldID, robotID, string(specData), level)
				if err != nil {
					return fmt.Errorf("episode %d: %w", i+1, err)
				}
				results = append(results, result)

				if !jsonOutput {
					outcome := "PASS"
					if !result.GetSuccess() {
						outcome = "FAIL"
					}
					fell := ""
					if result.GetFell() {
						fell = " (fell)"
					}
					cliLogln("Episode %d/%d: %s  distance %.2f m%s",
						i+1, episodes, outcome, result.GetDistanceTraveledM(), fell)
				}
			}

			summary := summarizeEval(results)
			if jsonOutput {
				if err := printJSON(summary); err != nil {
					return err
				}
			} else {
				fmt.Print(renderEvalSummary(summary))
			}
			if summary.Successes == 0 {
				return fmt.Errorf("all %d episodes failed", episodes)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	cmd.Flags().IntVar(&episodes, "episodes", 5, "Number of episodes to run")
	cmd.Flags().StringVar(&controlLevel, "control-level", "motion",
		"Highest control level the task may use (task, motion, joint, physics)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

// runEvalEpisode runs the task once and returns its final result, draining
// (and discarding) progress/log events.
func runEvalEpisode(ctx context.Context, client agentpbv2.WendySimServiceClient,
	worldID, robotID, specYAML string, level simpb.ControlLevel) (*simpb.TaskResult, error) {
	stream, err := client.RunTask(ctx, &agentpbv2.RunSimTaskRequest{
		Task: &simpb.RunTaskRequest{
			WorldId:         worldID,
			RobotId:         robotID,
			SpecYaml:        specYAML,
			MaxControlLevel: level,
		},
		SessionControlLevel: level,
	})
	if err != nil {
		return nil, err
	}
	var result *simpb.TaskResult
	for {
		ev, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if res := ev.GetResult(); res != nil {
			result = res
		}
	}
	if result == nil {
		return nil, fmt.Errorf("task stream ended without a result")
	}
	return result, nil
}

// evalSummary aggregates episode results for `wendy sim eval`.
type evalSummary struct {
	Episodes    int     `json:"episodes"`
	Successes   int     `json:"successes"`
	SuccessRate float64 `json:"successRate"`
	Falls       int     `json:"falls"`
	MeanDistM   float64 `json:"meanDistanceM"`
	MinDistM    float64 `json:"minDistanceM"`
	MaxDistM    float64 `json:"maxDistanceM"`
}

// summarizeEval computes success/distance/fall statistics over episodes.
func summarizeEval(results []*simpb.TaskResult) evalSummary {
	s := evalSummary{Episodes: len(results)}
	if len(results) == 0 {
		return s
	}
	var total float64
	s.MinDistM = results[0].GetDistanceTraveledM()
	for _, r := range results {
		if r.GetSuccess() {
			s.Successes++
		}
		if r.GetFell() {
			s.Falls++
		}
		d := r.GetDistanceTraveledM()
		total += d
		if d < s.MinDistM {
			s.MinDistM = d
		}
		if d > s.MaxDistM {
			s.MaxDistM = d
		}
	}
	s.MeanDistM = total / float64(len(results))
	s.SuccessRate = float64(s.Successes) / float64(len(results))
	return s
}

// renderEvalSummary renders the human-readable eval summary block.
func renderEvalSummary(s evalSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Evaluation over %d episode(s)\n", s.Episodes)
	fmt.Fprintf(&b, "  Success rate: %.0f%% (%d/%d)\n", s.SuccessRate*100, s.Successes, s.Episodes)
	fmt.Fprintf(&b, "  Distance:     mean %.2f m (min %.2f, max %.2f)\n", s.MeanDistM, s.MinDistM, s.MaxDistM)
	fmt.Fprintf(&b, "  Falls:        %d\n", s.Falls)
	return b.String()
}
