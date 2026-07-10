package commands

import (
	"bufio"
	"context"
	"strings"
	"testing"
)

func TestApplyTeleopKey(t *testing.T) {
	var c teleopCommand

	// Three forward steps accumulate without float drift.
	for i := 0; i < 3; i++ {
		if changed, quit := applyTeleopKey(&c, 'w'); !changed || quit {
			t.Fatalf("w: changed/quit = %v/%v; want true/false", changed, quit)
		}
	}
	if c.vx != 0.3 {
		t.Errorf("vx after www = %v; want 0.3", c.vx)
	}

	applyTeleopKey(&c, 's')
	if c.vx != 0.2 {
		t.Errorf("vx after s = %v; want 0.2", c.vx)
	}

	applyTeleopKey(&c, 'a')
	applyTeleopKey(&c, 'a')
	if c.yaw != 0.4 {
		t.Errorf("yaw after aa = %v; want 0.4", c.yaw)
	}
	applyTeleopKey(&c, 'd')
	if c.yaw != 0.2 {
		t.Errorf("yaw after d = %v; want 0.2", c.yaw)
	}

	// Space zeroes the command but keeps the session running.
	if changed, quit := applyTeleopKey(&c, ' '); !changed || quit {
		t.Errorf("space: changed/quit = %v/%v; want true/false", changed, quit)
	}
	if c.vx != 0 || c.yaw != 0 {
		t.Errorf("command after space = %+v; want zeros", c)
	}

	// Unmapped keys change nothing.
	c = teleopCommand{vx: 0.1}
	if changed, quit := applyTeleopKey(&c, 'x'); changed || quit {
		t.Errorf("x: changed/quit = %v/%v; want false/false", changed, quit)
	}

	// q and Ctrl-C quit with a zeroed command.
	for _, key := range []byte{'q', 0x03} {
		c = teleopCommand{vx: 0.5, yaw: 0.2}
		if _, quit := applyTeleopKey(&c, key); !quit {
			t.Errorf("key %#x should quit", key)
		}
		if c.vx != 0 || c.yaw != 0 {
			t.Errorf("command after quit key %#x = %+v; want zeros", key, c)
		}
	}
}

func TestReadTeleopKey_DecodesArrows(t *testing.T) {
	tests := []struct {
		in   string
		want byte
	}{
		{in: "w", want: 'w'},
		{in: "\x1b[A", want: 'w'}, // up
		{in: "\x1b[B", want: 's'}, // down
		{in: "\x1b[C", want: 'd'}, // right
		{in: "\x1b[D", want: 'a'}, // left
		{in: "\x1b[Z", want: 0},   // unknown CSI final byte
		{in: "\x1bx", want: 0},    // bare escape + non-CSI
		{in: " ", want: ' '},
	}
	for _, tt := range tests {
		got, err := readTeleopKey(bufio.NewReader(strings.NewReader(tt.in)))
		if err != nil {
			t.Errorf("readTeleopKey(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("readTeleopKey(%q) = %#x; want %#x", tt.in, got, tt.want)
		}
	}
}

func TestRunTeleopLoop_SendsCommandsAndFinalStop(t *testing.T) {
	// wwd then q: three command sends plus the final stop.
	in := bufio.NewReader(strings.NewReader("wwdq"))
	var sent []teleopCommand
	err := runTeleopLoop(context.Background(), in, func(c teleopCommand) error {
		sent = append(sent, c)
		return nil
	})
	if err != nil {
		t.Fatalf("runTeleopLoop: %v", err)
	}
	want := []teleopCommand{
		{vx: 0.1},
		{vx: 0.2},
		{vx: 0.2, yaw: -0.2},
		{}, // final stop from q
	}
	if len(sent) != len(want) {
		t.Fatalf("sent %d commands (%v); want %d", len(sent), sent, len(want))
	}
	for i := range want {
		if sent[i] != want[i] {
			t.Errorf("sent[%d] = %+v; want %+v", i, sent[i], want[i])
		}
	}
}

func TestRunTeleopLoop_EOFSendsStop(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("w"))
	var sent []teleopCommand
	err := runTeleopLoop(context.Background(), in, func(c teleopCommand) error {
		sent = append(sent, c)
		return nil
	})
	if err != nil {
		t.Fatalf("runTeleopLoop: %v", err)
	}
	if len(sent) != 2 || sent[1] != (teleopCommand{}) {
		t.Errorf("sent = %v; want command then stop", sent)
	}
}

func TestRenderTeleopStatus(t *testing.T) {
	got := renderTeleopStatus(teleopCommand{vx: 0.3, yaw: -0.2})
	for _, want := range []string{"vx  +0.3 m/s", "yaw  -0.2 rad/s", "q quit"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %q", got, want)
		}
	}
}

func TestSimTeleopCmd_Flags(t *testing.T) {
	cmd := newSimTeleopCmd()
	for _, f := range []string{"world", "robot"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("teleop missing flag --%s", f)
		}
	}
}
