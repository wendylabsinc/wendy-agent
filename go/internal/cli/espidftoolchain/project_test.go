package espidftoolchain

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestProjectToolchain(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "sample with spaces")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	image := "espressif/idf@sha256:" + strings.Repeat("a", 64)
	write := func(p ProjectToolchain) {
		data, _ := json.Marshal(p)
		if err := os.WriteFile(filepath.Join(project, "wendy-idf.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := LoadProjectToolchain(project, "esp32s31")
	if err != nil || p.Image != "" {
		t.Fatalf("default: %+v, %v", p, err)
	}
	calls := stubExecCommandContext(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	})
	p.Command(context.Background(), "build")
	if !reflect.DeepEqual((*calls)[0], []string{"eim", "run", "idf.py build", DefaultVersion}) {
		t.Fatal(*calls)
	}
	write(ProjectToolchain{Image: image, WorkspaceRoot: "..", Target: "esp32s31", Preview: true})
	p, err = LoadProjectToolchain(project, "esp32s31")
	if err != nil {
		t.Fatal(err)
	}
	p.Command(context.Background(), "set-target", "esp32s31")
	got := (*calls)[1]
	if len(got) < 5 || got[3] != "--name" || !strings.HasPrefix(got[4], "wendy-idf-") {
		t.Fatalf("container is not named for cancellation: %v", got)
	}
	want := []string{"docker", "run", "--rm", "--name", got[4], "-v", p.root + ":/project", "-w", "/project/sample with spaces", image, "idf.py", "--preview", "set-target", "esp32s31"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := LoadProjectToolchain(project, "esp32s3"); err == nil {
		t.Fatal("accepted mismatched target")
	}
	for _, bad := range []ProjectToolchain{
		{Image: "espressif/idf:latest", Target: "esp32s31"},
		{Image: image, Target: "esp32s31", WorkspaceRoot: root},
		{Image: image, Target: "esp32s31", WorkspaceRoot: "../elsewhere"},
	} {
		write(bad)
		if _, err := LoadProjectToolchain(project, "esp32s31"); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestProjectToolchainCancelRemovesContainer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep")
	}
	calls := stubExecCommandContext(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if args[0] == "run" {
			return exec.CommandContext(ctx, "sleep", "30")
		}
		return exec.CommandContext(ctx, "true")
	})
	p := &ProjectToolchain{Image: "espressif/idf@sha256:" + strings.Repeat("a", 64), root: t.TempDir(), relative: "."}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := p.Command(ctx, "build")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled build succeeded")
	}
	name := (*calls)[0][4]
	if len(*calls) != 2 || !reflect.DeepEqual((*calls)[1], []string{"docker", "rm", "--force", name}) {
		t.Fatalf("cancellation left the container running: %v", *calls)
	}
}
