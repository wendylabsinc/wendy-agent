package espidftoolchain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectToolchain selects an opt-in, digest-pinned ESP-IDF container. Projects
// without wendy-idf.json retain the standard EIM build path.
type ProjectToolchain struct {
	Image          string `json:"image"`
	WorkspaceRoot  string `json:"workspaceRoot"`
	Target         string `json:"target"`
	Preview        bool   `json:"preview"`
	root, relative string
}

func LoadProjectToolchain(projectPath, target string) (*ProjectToolchain, error) {
	data, err := os.ReadFile(filepath.Join(projectPath, "wendy-idf.json"))
	if os.IsNotExist(err) {
		return &ProjectToolchain{}, nil
	}
	if err != nil {
		return nil, err
	}
	var p ProjectToolchain
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("wendy-idf.json: %w", err)
	}
	if !regexp.MustCompile(`^espressif/idf@sha256:[0-9a-f]{64}$`).MatchString(p.Image) {
		return nil, fmt.Errorf("wendy-idf.json image must pin espressif/idf by SHA-256 digest")
	}
	if p.Target == "" || p.Target != target {
		return nil, fmt.Errorf("project targets %q, device targets %q", p.Target, target)
	}
	if filepath.IsAbs(p.WorkspaceRoot) {
		return nil, fmt.Errorf("workspaceRoot must be relative to the project")
	}
	project, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	p.root, err = filepath.EvalSymlinks(filepath.Join(project, p.WorkspaceRoot))
	if err != nil {
		return nil, err
	}
	p.relative, err = filepath.Rel(p.root, project)
	if err != nil || p.relative == ".." || strings.HasPrefix(p.relative, ".."+string(filepath.Separator)) || p.root == filepath.VolumeName(p.root)+string(filepath.Separator) {
		return nil, fmt.Errorf("workspaceRoot must be the project directory or a containing workspace, not the filesystem root")
	}
	return &p, nil
}

func (p *ProjectToolchain) Ensure(ctx context.Context) error {
	if p.Image == "" {
		return EnsureVersion(ctx)
	}
	cmd := execCommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ESP-IDF container requires Docker: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Command runs idf.py with args. A nil or zero toolchain uses EIM.
func (p *ProjectToolchain) Command(ctx context.Context, args ...string) *exec.Cmd {
	if p == nil || p.Image == "" {
		return IdfCommandContext(ctx, args...)
	}
	command := []string{"run", "--rm", "-v", p.root + ":/project", "-w", filepath.ToSlash(filepath.Join("/project", p.relative)), p.Image, "idf.py"}
	if p.Preview {
		command = append(command, "--preview")
	}
	return execCommandContext(ctx, "docker", append(command, args...)...)
}
