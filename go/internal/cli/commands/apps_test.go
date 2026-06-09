package commands

import (
	"bytes"
	"strings"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestDeviceAppsListCommand_HelpDescribesDeployedApps(t *testing.T) {
	cmd := newDeviceCmd()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"apps", "list", "--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "List deployed applications") {
		t.Fatalf("expected help output to contain %q, got %q", "List deployed applications", output)
	}
	if strings.Contains(output, "List running applications") {
		t.Fatalf("expected help output to avoid stale wording, got %q", output)
	}
}

func TestAppsList_GroupDisplayShowsServiceSubRows(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{
			AppName:      "com.example.robot",
			AppVersion:   "v1.0.0",
			RunningState: agentpb.AppRunningState_RUNNING,
			Services: []*agentpb.ServiceEntry{
				{Name: "camera", RunningState: agentpb.AppRunningState_RUNNING},
				{Name: "detector", RunningState: agentpb.AppRunningState_STOPPED},
			},
		},
		{
			AppName:      "com.example.simple",
			AppVersion:   "v2.0.0",
			RunningState: agentpb.AppRunningState_STOPPED,
		},
	}

	var rows [][]string
	for _, c := range containers {
		services := c.GetServices()
		if len(services) > 1 {
			rows = append(rows, []string{"", c.GetAppName() + " [group]", c.GetAppVersion(), "0"})
			for _, s := range services {
				rows = append(rows, []string{"", "  ↳ " + s.GetName(), "", ""})
			}
		} else {
			rows = append(rows, []string{"", c.GetAppName(), c.GetAppVersion(), "0"})
		}
	}

	// Group app should produce 3 rows (header + 2 services); single app 1 row.
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows (1 group header + 2 services + 1 single), got %d", len(rows))
	}
	if !strings.Contains(rows[0][1], "[group]") {
		t.Errorf("group header row should contain [group], got %q", rows[0][1])
	}
	if !strings.Contains(rows[1][1], "↳") || !strings.Contains(rows[1][1], "camera") {
		t.Errorf("first service sub-row should contain ↳ and camera, got %q", rows[1][1])
	}
	if !strings.Contains(rows[2][1], "↳") || !strings.Contains(rows[2][1], "detector") {
		t.Errorf("second service sub-row should contain ↳ and detector, got %q", rows[2][1])
	}
	if strings.Contains(rows[3][1], "[group]") {
		t.Errorf("single-service app should not be marked as group, got %q", rows[3][1])
	}
}

func TestAppsList_SingleServiceNoGroupMark(t *testing.T) {
	containers := []*agentpb.AppContainer{
		{
			AppName:      "com.example.simple",
			AppVersion:   "v1.0.0",
			RunningState: agentpb.AppRunningState_RUNNING,
		},
	}

	var rows [][]string
	for _, c := range containers {
		if len(c.GetServices()) > 1 {
			rows = append(rows, []string{"", c.GetAppName() + " [group]", c.GetAppVersion(), "0"})
		} else {
			rows = append(rows, []string{"", c.GetAppName(), c.GetAppVersion(), "0"})
		}
	}

	if len(rows) != 1 {
		t.Fatalf("expected 1 row for single-service app, got %d", len(rows))
	}
	if strings.Contains(rows[0][1], "[group]") {
		t.Errorf("single-service app should not be marked as group")
	}
}
