package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestBuildExecStart_DefaultsAndCmd(t *testing.T) {
	// No explicit command -> defaults to claude (the app's purpose).
	req := buildExecStart("claude-on-device", nil, 40, 120)
	st := req.GetStart()
	if st == nil {
		t.Fatal("expected ExecStart")
	}
	if st.GetAppName() != "claude-on-device" {
		t.Fatalf("app = %q", st.GetAppName())
	}
	if !st.GetTty() {
		t.Fatal("tty should default true")
	}
	if len(st.GetCommand()) != 1 || st.GetCommand()[0] != "claude" {
		t.Fatalf("command = %v, want [claude]", st.GetCommand())
	}
	if st.GetTermSize().GetRows() != 40 || st.GetTermSize().GetCols() != 120 {
		t.Fatalf("term size = %v", st.GetTermSize())
	}

	// Explicit command is forwarded verbatim.
	req2 := buildExecStart("app", []string{"bash", "-l"}, 10, 20)
	if got := req2.GetStart().GetCommand(); len(got) != 2 || got[0] != "bash" || got[1] != "-l" {
		t.Fatalf("command = %v, want [bash -l]", got)
	}
}

func TestWinSizeFrame(t *testing.T) {
	f := winSizeFrame(50, 200)
	if f.GetResize().GetRows() != 50 || f.GetResize().GetCols() != 200 {
		t.Fatalf("resize = %v", f.GetResize())
	}
}

func TestExecStartType(t *testing.T) {
	// Guard: the generated oneof wrapper names we depend on exist.
	var _ *agentpb.ExecContainerRequest_Start = &agentpb.ExecContainerRequest_Start{}
	var _ *agentpb.ExecContainerRequest_Resize = &agentpb.ExecContainerRequest_Resize{}
}
