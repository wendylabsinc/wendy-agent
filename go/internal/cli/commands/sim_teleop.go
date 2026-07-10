package commands

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

// Teleop increments per keypress.
const (
	teleopVxStepMps    = 0.1
	teleopYawStepRadps = 0.2
)

func newSimTeleopCmd() *cobra.Command {
	var worldID, robotID string

	cmd := &cobra.Command{
		Use:   "teleop",
		Short: "Drive a simulated robot from the keyboard (WASD/arrows)",
		Long: "Drive a simulated robot interactively. Each keypress adjusts the held\n" +
			"velocity command and sends it to the robot:\n\n" +
			"  w / up       forward  +0.1 m/s\n" +
			"  s / down     backward -0.1 m/s\n" +
			"  a / left     turn left  +0.2 rad/s\n" +
			"  d / right    turn right -0.2 rad/s\n" +
			"  space        stop (zero all velocities)\n" +
			"  q / Ctrl-C   stop and quit",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			fd := int(os.Stdin.Fd())
			if !term.IsTerminal(fd) {
				return fmt.Errorf("teleop needs an interactive terminal (use `wendy sim drive` for scripted control)")
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			oldState, err := term.MakeRaw(fd)
			if err != nil {
				return fmt.Errorf("entering raw terminal mode: %w", err)
			}
			defer func() { _ = term.Restore(fd, oldState) }()

			// Raw mode: lines end \r\n and Ctrl-C arrives as a plain byte
			// (0x03) that the key loop turns into a stop-and-quit.
			fmt.Print("wendy sim teleop — w/s speed, a/d turn, space stop, q quit\r\n")
			err = runTeleopLoop(ctx, bufio.NewReader(os.Stdin), func(c teleopCommand) error {
				if _, sendErr := conn.SimService.SetVelocity(ctx, &simpb.SetVelocityRequest{
					WorldId:      worldID,
					RobotId:      robotID,
					VxMps:        c.vx,
					YawRateRadps: c.yaw,
				}); sendErr != nil {
					return sendErr
				}
				fmt.Printf("\r%s", renderTeleopStatus(c))
				return nil
			})
			fmt.Print("\r\n")
			if err != nil {
				return fmt.Errorf("teleop: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&worldID, "world", "", "World ID (required)")
	cmd.Flags().StringVar(&robotID, "robot", "", "Robot ID (required)")
	_ = cmd.MarkFlagRequired("world")
	_ = cmd.MarkFlagRequired("robot")
	return cmd
}

// teleopCommand is the velocity command teleop holds on the robot.
type teleopCommand struct {
	vx  float64 // m/s, forward
	yaw float64 // rad/s, counter-clockwise
}

// runTeleopLoop reads keys until quit/EOF, calling send whenever the held
// command changes. It always sends a final stop (zeros) before returning.
func runTeleopLoop(ctx context.Context, in *bufio.Reader, send func(teleopCommand) error) error {
	var cmd teleopCommand
	for {
		if ctx.Err() != nil {
			return sendTeleopStop(send)
		}
		key, err := readTeleopKey(in)
		if err != nil {
			// EOF or a closed stdin ends the session like q.
			return sendTeleopStop(send)
		}
		changed, quit := applyTeleopKey(&cmd, key)
		if quit {
			return sendTeleopStop(send)
		}
		if changed {
			if err := send(cmd); err != nil {
				_ = sendTeleopStop(send)
				return err
			}
		}
	}
}

// sendTeleopStop sends the zero command; its error is returned so a dead
// connection still surfaces at exit.
func sendTeleopStop(send func(teleopCommand) error) error {
	return send(teleopCommand{})
}

// readTeleopKey reads one key, decoding ANSI arrow-key escape sequences to
// their WASD equivalents.
func readTeleopKey(in *bufio.Reader) (byte, error) {
	b, err := in.ReadByte()
	if err != nil {
		return 0, err
	}
	if b != 0x1b { // not an escape sequence
		return b, nil
	}
	next, err := in.ReadByte()
	if err != nil {
		return 0, err
	}
	if next != '[' {
		// A bare Escape (or unknown sequence): ignore via an unmapped byte.
		return 0, nil
	}
	final, err := in.ReadByte()
	if err != nil {
		return 0, err
	}
	switch final {
	case 'A': // up
		return 'w', nil
	case 'B': // down
		return 's', nil
	case 'C': // right
		return 'd', nil
	case 'D': // left
		return 'a', nil
	default:
		return 0, nil
	}
}

// applyTeleopKey applies one (already arrow-decoded) key to the held command.
// changed reports whether the command should be (re)sent; quit ends the
// session (the caller sends the final stop).
func applyTeleopKey(c *teleopCommand, key byte) (changed, quit bool) {
	switch key {
	case 'w':
		c.vx = roundTeleop(c.vx + teleopVxStepMps)
	case 's':
		c.vx = roundTeleop(c.vx - teleopVxStepMps)
	case 'a':
		c.yaw = roundTeleop(c.yaw + teleopYawStepRadps)
	case 'd':
		c.yaw = roundTeleop(c.yaw - teleopYawStepRadps)
	case ' ':
		c.vx, c.yaw = 0, 0
	case 'q', 0x03: // q or Ctrl-C
		c.vx, c.yaw = 0, 0
		return false, true
	default:
		return false, false
	}
	return true, false
}

// roundTeleop keeps accumulated steps at one-decimal precision so repeated
// float additions never drift (0.1+0.2 style artifacts).
func roundTeleop(v float64) float64 {
	return math.Round(v*10) / 10
}

// renderTeleopStatus renders the one-line held-command status (padded so a
// shorter line fully overwrites a longer one under \r redraws).
func renderTeleopStatus(c teleopCommand) string {
	return fmt.Sprintf("vx %+5.1f m/s   yaw %+5.1f rad/s   (space stop, q quit)    ", c.vx, c.yaw)
}
