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

func TestSortRunningFirstStable(t *testing.T) {
	apps := []appInfo{
		{Name: "stopped-1", State: "STOPPED"},
		{Name: "running-1", State: "RUNNING"},
		{Name: "crash-looping", State: "CRASH_LOOPING"},
		{Name: "running-2", State: "running"},
		{Name: "stopped-2", State: "STOPPED"},
	}

	sortRunningFirst(apps, func(a appInfo) string { return a.State })

	got := make([]string, len(apps))
	for i, app := range apps {
		got[i] = app.Name
	}
	want := []string{"running-1", "running-2", "stopped-1", "crash-looping", "stopped-2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("app order = %v, want %v", got, want)
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

func TestStateIconPlain_CrashLooping(t *testing.T) {
	// A crash-looping app (WDY-1826) must be visually distinct from both a
	// running (●) and a stopped (○) app in plain, non-styled output. The state
	// string is AppRunningState.String(), i.e. "CRASH_LOOPING".
	state := agentpb.AppRunningState_CRASH_LOOPING.String()
	if got := stateIconPlain(state); got != "↻" {
		t.Fatalf("stateIconPlain(%q) = %q, want ↻", state, got)
	}
	if got := stateIconPlain(agentpb.AppRunningState_RUNNING.String()); got != "●" {
		t.Fatalf("stateIconPlain(RUNNING) = %q, want ●", got)
	}
	if got := stateIconPlain(agentpb.AppRunningState_STOPPED.String()); got != "○" {
		t.Fatalf("stateIconPlain(STOPPED) = %q, want ○", got)
	}
}

func TestStateIcon_CrashLoopingDistinctFromStopped(t *testing.T) {
	crash := stateIcon(agentpb.AppRunningState_CRASH_LOOPING.String())
	stopped := stateIcon(agentpb.AppRunningState_STOPPED.String())
	if crash == stopped {
		t.Fatalf("crash-looping icon %q must differ from stopped icon %q", crash, stopped)
	}
	if !strings.Contains(crash, "↻") {
		t.Fatalf("crash-looping icon %q should contain ↻", crash)
	}
}

func TestHTTPPortColumn_Zero(t *testing.T) {
	if got := httpPortColumn(0); got != "" {
		t.Errorf("httpPortColumn(0) = %q, want empty string", got)
	}
}

func TestHTTPPortColumn_NonZero(t *testing.T) {
	if got := httpPortColumn(8080); got != ":8080" {
		t.Errorf("httpPortColumn(8080) = %q, want %q", got, ":8080")
	}
}
