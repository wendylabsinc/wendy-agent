package commands

import "testing"

func TestShellNeedsTTY(t *testing.T) {
	if !shellNeedsTTY(nil) {
		t.Fatal("nil command (bare login shell) should need a TTY")
	}
	if shellNeedsTTY([]string{"ls"}) {
		t.Fatal("explicit command should not need a TTY")
	}
}

func TestBuildShellStart_CommandAndSize(t *testing.T) {
	// Explicit command is forwarded verbatim.
	req := buildShellStart([]string{"ls", "-la"}, 24, 80)
	st := req.GetStart()
	if st == nil {
		t.Fatal("expected Start")
	}
	if got := st.GetCommand(); len(got) != 2 || got[0] != "ls" || got[1] != "-la" {
		t.Fatalf("command = %v, want [ls -la]", got)
	}
	if st.GetTermSize().GetRows() != 24 || st.GetTermSize().GetCols() != 80 {
		t.Fatalf("term size = %v", st.GetTermSize())
	}

	// Empty command -> empty Command (agent resolves the login shell).
	req2 := buildShellStart(nil, 24, 80)
	if got := req2.GetStart().GetCommand(); len(got) != 0 {
		t.Fatalf("command = %v, want empty (login shell)", got)
	}

	// 24x80 default propagates through.
	req3 := buildShellStart([]string{"sh"}, 24, 80)
	if req3.GetStart().GetTermSize().GetRows() != 24 || req3.GetStart().GetTermSize().GetCols() != 80 {
		t.Fatalf("term size = %v, want 24x80", req3.GetStart().GetTermSize())
	}
}
