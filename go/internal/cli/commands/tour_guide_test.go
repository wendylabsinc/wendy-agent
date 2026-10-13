//go:build darwin || linux || windows

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
)

func stubTourGuide(t *testing.T, actions ...string) {
	t.Helper()
	oldPick, oldWait, oldWalk, oldBrowser := tourGuidePick, tourGuideWait, tourGuideWalk, tourGuideBrowser
	oldScaffold, oldDeploy, oldControl, oldURL := tourGuideScaffold, tourGuideDeploy, tourGuideControlApp, tourGuideAppURL
	oldCleanup := tourGuidePrepareCleanup
	oldEditor, oldStop, oldInteractive := tourGuideEditor, tourGuideStop, isInteractiveTerminalFn
	t.Cleanup(func() {
		tourGuidePick, tourGuideWait, tourGuideWalk, tourGuideBrowser = oldPick, oldWait, oldWalk, oldBrowser
		tourGuideScaffold, tourGuideDeploy, tourGuideControlApp, tourGuideAppURL = oldScaffold, oldDeploy, oldControl, oldURL
		tourGuidePrepareCleanup = oldCleanup
		tourGuideEditor, tourGuideStop, isInteractiveTerminalFn = oldEditor, oldStop, oldInteractive
	})
	isInteractiveTerminalFn = func() bool { return true }
	index := 0
	tourGuidePick = func(_ context.Context, _ io.Writer, title string, items []tui.PickerItem) (string, error) {
		if index >= len(actions) {
			t.Fatalf("unexpected menu %q", title)
		}
		action := actions[index]
		index++
		if action == ":q" {
			return "", errTourGuideBack
		}
		if action == ":cancel" {
			return "", ErrUserCancelled
		}
		for i, item := range items {
			if item.SortKey == "" || (i > 0 && item.SortKey <= items[i-1].SortKey) {
				t.Fatalf("menu %q has no explicit order", title)
			}
			if item.Value == action {
				return action, nil
			}
		}
		t.Fatalf("action %q unavailable in %q", action, title)
		return "", nil
	}
	t.Cleanup(func() {
		if index != len(actions) {
			t.Errorf("used %d of %d menu actions", index, len(actions))
		}
	})
	tourGuideWait = func(context.Context, io.Writer, string) error { t.Fatal("unexpected wait"); return nil }
	tourGuideWalk = func(context.Context, int, vm.RobotProfile, tourRobotProject) (float64, error) {
		t.Fatal("unexpected movement")
		return 0, nil
	}
	tourGuideBrowser = func(string) error { t.Fatal("unexpected browser launch"); return nil }
	tourGuideScaffold = func(string, string) (string, error) { t.Fatal("unexpected scaffold"); return "", nil }
	tourGuideDeploy = func(context.Context, string, string) error { t.Fatal("unexpected deployment"); return nil }
	tourGuidePrepareCleanup = func(string, string, string) (func(context.Context) error, error) {
		return func(context.Context) error { return nil }, nil
	}
	tourGuideControlApp = func(context.Context, string, string, string) (string, error) {
		t.Fatal("unexpected app control")
		return "", nil
	}
	tourGuideAppURL = func(context.Context, string, string) (string, error) {
		t.Fatal("unexpected app URL lookup")
		return "", nil
	}
	tourGuideEditor = func(context.Context, string) (bool, error) { t.Fatal("unexpected editor launch"); return false, nil }
	tourGuideStop = func(context.Context, string) error { t.Fatal("unexpected simulator stop"); return nil }
}

func guideFixture(t *testing.T) (*cobra.Command, *bytes.Buffer, tourRobotProject, string) {
	t.Helper()
	p, path, err := loadTourRobotProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	return cmd, &out, p, path
}

func TestTourGuideWalkKeepsLatestEditsAndTarget(t *testing.T) {
	stubTourGuide(t, "walk", "right", "finish", "leave")
	cmd, _, p, path := guideFixture(t)
	latest := p
	latest.DurationSeconds, latest.ForwardMPS = 2, .6
	data, _ := json.Marshal(latest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	walks := 0
	tourGuideWalk = func(_ context.Context, port int, _ vm.RobotProfile, got tourRobotProject) (float64, error) {
		walks++
		if port != 1234 || got.VMName != p.VMName || got.DurationSeconds != 2 || got.ForwardMPS != .6 || got.TurnRPS != -.5 {
			t.Fatalf("wrong walk or target: port=%d %+v", port, got)
		}
		return .4, nil
	}
	if err := runTourGuide(context.Background(), cmd, p, path, 1234, vm.RobotProfile{}); err != nil {
		t.Fatal(err)
	}
	saved, _, err := loadTourRobotProject(filepath.Dir(path))
	if err != nil || walks != 1 || saved.DurationSeconds != 2 || saved.ForwardMPS != .6 || saved.TurnRPS != -.5 {
		t.Fatalf("saved=%+v walks=%d err=%v", saved, walks, err)
	}
}

func TestSaveTourWalkDoesNotRedirectMovement(t *testing.T) {
	_, _, p, path := guideFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveTourWalk(path, "another-vm", 0); err == nil {
		t.Fatal("accepted a different VM")
	}
	if _, err := saveTourWalk(path, p.VMName, 99); err == nil {
		t.Fatal("accepted unbounded movement")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("invalid edit changed starter")
	}
}

func TestTourGuideProgrammingDeploysOnlyExplicitlyToSameVM(t *testing.T) {
	stubTourGuide(t, "app", "patrol", "edit", "deploy", "start", "stop", "deploy", "back", "finish", "leave")
	cmd, out, p, path := guideFixture(t)
	appPath := filepath.Join(filepath.Dir(path), "apps", "patrol")
	tourGuideScaffold = func(dir, kind string) (string, error) {
		if dir != filepath.Dir(path) || kind != "patrol" {
			t.Fatalf("wrong scaffold %q %q", dir, kind)
		}
		return appPath, nil
	}
	tourGuideEditor = func(_ context.Context, source string) (bool, error) {
		if source != filepath.Join(appPath, "patrol.py") {
			t.Fatalf("wrong source %s", source)
		}
		return true, nil
	}
	deploys := 0
	tourGuideDeploy = func(_ context.Context, name, dir string) error {
		if name != p.VMName || dir != appPath {
			t.Fatalf("wrong deploy target %q %q", name, dir)
		}
		deploys++
		return nil
	}
	var controls []string
	tourGuideControlApp = func(_ context.Context, name, kind, action string) (string, error) {
		if name != p.VMName || kind != "patrol" || deploys == 0 {
			t.Fatalf("invalid control target or undeployed app: %q %q", name, kind)
		}
		controls = append(controls, action)
		return "Robot app " + action, nil
	}
	if err := runTourGuide(context.Background(), cmd, p, path, 1234, vm.RobotProfile{}); err != nil {
		t.Fatal(err)
	}
	if deploys != 2 || strings.Join(controls, ",") != "start,stop" {
		t.Fatalf("deploys=%d controls=%v", deploys, controls)
	}
	if strings.Count(out.String(), "1. Edit your app") != 1 || !strings.Contains(out.String(), "DEFAULT_WAYPOINTS") {
		t.Fatalf("missing or repeated edit instructions: %s", out.String())
	}
}

func TestTourGuideStopsStartedAppOnBackOrCancellation(t *testing.T) {
	for _, exit := range []string{"back", ":cancel"} {
		t.Run(exit, func(t *testing.T) {
			stubTourGuide(t, "deploy", "start", exit)
			tourGuideDeploy = func(context.Context, string, string) error { return nil }
			var actions []string
			tourGuideControlApp = func(ctx context.Context, name, kind, action string) (string, error) {
				if ctx.Err() != nil || name != "tour-test" || kind != "patrol" {
					t.Fatal("cleanup lost context or target")
				}
				actions = append(actions, action)
				return "ok", nil
			}
			err := runTourEditableApp(context.Background(), io.Discard, "tour-test", "patrol", t.TempDir())
			if (exit == ":cancel" && !errors.Is(err, ErrUserCancelled)) || (exit == "back" && err != nil) {
				t.Fatalf("error=%v", err)
			}
			if strings.Join(actions, ",") != "start,stop" {
				t.Fatalf("actions=%v", actions)
			}
		})
	}
}

func TestTourGuideBrowserWaitsAndStopIsExplicit(t *testing.T) {
	stubTourGuide(t, "browser", "finish", "stop")
	cmd, _, p, path := guideFixture(t)
	opens, waits, stops := 0, 0, 0
	tourGuideBrowser = func(url string) error {
		opens++
		if url != robotURL(1234) {
			t.Fatal(url)
		}
		return nil
	}
	tourGuideWait = func(context.Context, io.Writer, string) error { waits++; return nil }
	tourGuideStop = func(_ context.Context, name string) error {
		stops++
		if name != p.VMName {
			t.Fatal(name)
		}
		return nil
	}
	if err := runTourGuide(context.Background(), cmd, p, path, 1234, vm.RobotProfile{}); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || waits != 1 || stops != 1 {
		t.Fatalf("opens=%d waits=%d stops=%d", opens, waits, stops)
	}
}

func TestTourGuideQuitAndNoninteractiveHaveNoSideEffects(t *testing.T) {
	for _, mode := range []string{"quit", "cancel", "noninteractive"} {
		t.Run(mode, func(t *testing.T) {
			actions := []string{}
			if mode == "quit" {
				actions = []string{":q"}
			}
			if mode == "cancel" {
				actions = []string{"app", ":cancel"}
			}
			stubTourGuide(t, actions...)
			isInteractiveTerminalFn = func() bool { return mode != "noninteractive" }
			cmd, out, p, path := guideFixture(t)
			err := runTourGuide(context.Background(), cmd, p, path, 1234, vm.RobotProfile{})
			if mode == "cancel" {
				if !errors.Is(err, ErrUserCancelled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "noninteractive" && !strings.Contains(out.String(), "wendy vm stop "+p.VMName) {
				t.Fatalf("missing completion instructions: %s", out.String())
			}
		})
	}
}

func TestTourContinueDistinguishesBackAndCancellation(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
		model, cmd := (tourContinueModel{}).Update(key)
		got := model.(tourContinueModel)
		if cmd == nil || got.done != (key.Type == tea.KeyEnter) || got.cancelled != (key.Type == tea.KeyCtrlC) || got.back != (key.String() == "q") {
			t.Fatalf("key=%v model=%+v", key, got)
		}
	}
}

func TestTourGuidePickerDefaultAndCancellation(t *testing.T) {
	items := []tui.PickerItem{
		guideItem("1", "Change the walk", "Try a walk", "walk"),
		guideItem("0", "Build a robot app", "Edit and deploy", "app"),
	}
	for _, tc := range []struct {
		name, input, want string
		err               error
	}{
		{"enter selects programming", "\r", "app", nil},
		{"q goes back", "q", "", errTourGuideBack},
		{"ctrl-c cancels", "\x03", "", ErrUserCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := pickTourGuideWithOptions(context.Background(), io.Discard, "Next", items, tea.WithInput(strings.NewReader(tc.input)), tea.WithoutRenderer(), tea.WithoutSignals())
			if selected != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("selected=%q err=%v, want %q %v", selected, err, tc.want, tc.err)
			}
		})
	}
}

func TestTourGuideAppDefaultAdvancesWithProgress(t *testing.T) {
	for _, kind := range []string{"patrol", "roam", "teleop", "sensors"} {
		t.Run(kind, func(t *testing.T) {
			stubTourGuide(t)
			defaults := []string{"deploy", "open"}
			actions := []string{"deploy", "back"}
			if kind == "patrol" || kind == "roam" {
				defaults = []string{"deploy", "start", "stop", "start"}
				actions = []string{"deploy", "start", "stop", "back"}
			}
			index := 0
			tourGuidePick = func(_ context.Context, _ io.Writer, _ string, items []tui.PickerItem) (string, error) {
				if index >= len(defaults) || items[0].Value != defaults[index] {
					t.Fatalf("step %d default=%v, want %v", index, items[0].Value, defaults)
				}
				action := actions[index]
				index++
				return action, nil
			}
			tourGuideDeploy = func(context.Context, string, string) error { return nil }
			tourGuideControlApp = func(context.Context, string, string, string) (string, error) { return "ok", nil }
			if err := runTourEditableApp(context.Background(), io.Discard, "tour-test", kind, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			if index != len(defaults) {
				t.Fatalf("visited %d menus, want %d", index, len(defaults))
			}
		})
	}
}

func TestTourGuideReleasesDeploymentWithoutStartingMovement(t *testing.T) {
	deployErr := errors.New("deployment failed after selecting ROS control")
	for _, mode := range []string{"back", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			actions := []string{"deploy"}
			if mode == "back" {
				actions = append(actions, "back")
			}
			stubTourGuide(t, actions...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := t.TempDir()
			var events []string
			tourGuidePrepareCleanup = func(name, kind, source string) (func(context.Context) error, error) {
				if name != "tour-selected" || kind != "teleop" || source != path {
					t.Fatalf("wrong cleanup target %q %q %q", name, kind, source)
				}
				events = append(events, "capture")
				return func(cleanCtx context.Context) error {
					if cleanCtx.Err() != nil {
						t.Fatal("cleanup reused cancelled context")
					}
					events = append(events, "release")
					return nil
				}, nil
			}
			tourGuideDeploy = func(context.Context, string, string) error {
				events = append(events, "deploy")
				if mode == "failure" {
					return deployErr
				}
				if mode == "cancel" {
					cancel()
				}
				return nil
			}
			if mode == "failure" {
				isInteractiveTerminalFn = func() bool { return false }
			}
			err := runTourEditableApp(ctx, io.Discard, "tour-selected", "teleop", path)
			wantErr := map[string]error{"failure": deployErr, "cancel": context.Canceled}[mode]
			if !errors.Is(err, wantErr) || strings.Join(events, ",") != "capture,deploy,release" {
				t.Fatalf("err=%v events=%v, want %v and cleanup after deployment", err, events, wantErr)
			}
		})
	}
}

func TestTourGuideReleasesBeforeRedeployAndOnExit(t *testing.T) {
	stubTourGuide(t, "deploy", "deploy", "back")
	var events []string
	tourGuidePrepareCleanup = func(string, string, string) (func(context.Context) error, error) {
		events = append(events, "capture")
		return func(context.Context) error { events = append(events, "release"); return nil }, nil
	}
	tourGuideDeploy = func(context.Context, string, string) error { events = append(events, "deploy"); return nil }
	if err := runTourEditableApp(context.Background(), io.Discard, "tour-test", "patrol", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "capture,deploy,release,capture,deploy,release" {
		t.Fatalf("events=%v", events)
	}
}

func TestTourGuideReleaseStillRunsWhenMovementStopFails(t *testing.T) {
	stubTourGuide(t, "deploy", "start", "back")
	stopErr := errors.New("stop response lost")
	var actions []string
	tourGuideDeploy = func(context.Context, string, string) error { return nil }
	tourGuideControlApp = func(_ context.Context, _, _, action string) (string, error) {
		actions = append(actions, action)
		if action == "stop" {
			return "", stopErr
		}
		return "ok", nil
	}
	tourGuidePrepareCleanup = func(string, string, string) (func(context.Context) error, error) {
		return func(context.Context) error { actions = append(actions, "release"); return nil }, nil
	}
	err := runTourEditableApp(context.Background(), io.Discard, "tour-test", "patrol", t.TempDir())
	if !errors.Is(err, stopErr) || strings.Join(actions, ",") != "start,stop,release" {
		t.Fatalf("err=%v actions=%v", err, actions)
	}
}

func TestTourGuideRecapturesAppIdentityOnDeploymentRetry(t *testing.T) {
	stubTourGuide(t, "deploy", "back")
	oldConfirm := confirmTourRetry
	t.Cleanup(func() { confirmTourRetry = oldConfirm })
	identity := "first-app"
	confirmTourRetry = func(context.Context, io.Writer) (bool, error) { identity = "edited-app"; return true, nil }
	var events []string
	tourGuidePrepareCleanup = func(string, string, string) (func(context.Context) error, error) {
		captured := identity
		events = append(events, "capture "+captured)
		return func(context.Context) error { events = append(events, "release "+captured); return nil }, nil
	}
	tourGuideDeploy = func(context.Context, string, string) error {
		events = append(events, "deploy "+identity)
		if identity == "first-app" {
			return errors.New("fix the manifest and retry")
		}
		return nil
	}
	if err := runTourEditableApp(context.Background(), io.Discard, "tour-test", "patrol", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	want := "capture first-app,deploy first-app,release first-app,capture edited-app,deploy edited-app,release edited-app"
	if strings.Join(events, ",") != want {
		t.Fatalf("events=%v, want %s", events, want)
	}
}
