// Package buildspec compiles Wendy Build Spec into a canonical build plan and
// a compatibility Dockerfile.
package buildspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	DefaultFilename = "Wendyfile.toml"
	SchemaVersion   = 1
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Spec is the v1 authoring format.
type Spec struct {
	Version int         `toml:"version" json:"version"`
	Build   BuildSpec   `toml:"build" json:"build"`
	Runtime RuntimeSpec `toml:"runtime" json:"runtime"`
}

type BuildSpec struct {
	Adapter      string            `toml:"adapter" json:"adapter"`
	Base         string            `toml:"base" json:"base"`
	Workdir      string            `toml:"workdir" json:"workdir"`
	Setup        []string          `toml:"setup" json:"setup,omitempty"`
	Env          map[string]string `toml:"env" json:"env,omitempty"`
	Dependencies PhaseSpec         `toml:"dependencies" json:"dependencies"`
	Compile      PhaseSpec         `toml:"compile" json:"compile"`
}

type PhaseSpec struct {
	Command []string `toml:"command" json:"command,omitempty"`
	Inputs  []string `toml:"inputs" json:"inputs,omitempty"`
}

type RuntimeSpec struct {
	Base       string            `toml:"base" json:"base"`
	Workdir    string            `toml:"workdir" json:"workdir"`
	Setup      []string          `toml:"setup" json:"setup,omitempty"`
	Env        map[string]string `toml:"env" json:"env,omitempty"`
	Artifacts  []ArtifactSpec    `toml:"artifacts" json:"artifacts"`
	Entrypoint []string          `toml:"entrypoint" json:"entrypoint"`
}

type ArtifactSpec struct {
	Source      string `toml:"source" json:"source"`
	Destination string `toml:"destination" json:"destination"`
}

// Plan is the canonical, execution-oriented representation.
type Plan struct {
	SchemaVersion int      `json:"schema_version"`
	PlanID        string   `json:"plan_id"`
	Adapter       string   `json:"adapter"`
	Warnings      []string `json:"warnings,omitempty"`
	Steps         []Step   `json:"steps"`
}

// Step is one typed build-graph operation. Fields irrelevant to Kind are empty.
type Step struct {
	ID          string   `json:"id"`
	Stage       string   `json:"stage"`
	Kind        string   `json:"kind"`
	Image       string   `json:"image,omitempty"`
	Name        string   `json:"name,omitempty"`
	Value       string   `json:"value,omitempty"`
	Script      string   `json:"script,omitempty"`
	Directory   string   `json:"directory,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Command     []string `json:"command,omitempty"`
	From        string   `json:"from,omitempty"`
}

// Result is the complete deterministic compiler output.
type Result struct {
	Plan             Plan   `json:"plan"`
	Dockerfile       string `json:"dockerfile"`
	DockerfileSHA256 string `json:"dockerfile_sha256"`
}

// Compile parses, validates, and compiles a spec using project for input
// discovery. It performs no writes and does not inspect process-global state.
func Compile(project fs.FS, data []byte) (Result, error) {
	var spec Spec
	metadata, err := toml.Decode(string(data), &spec)
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", DefaultFilename, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		values := make([]string, len(undecoded))
		for index, key := range undecoded {
			values[index] = key.String()
		}
		sort.Strings(values)
		return Result{}, fmt.Errorf("unknown field(s): %s", strings.Join(values, ", "))
	}
	plan, err := compileSpec(project, spec)
	if err != nil {
		return Result{}, err
	}
	dockerfile, err := renderDockerfile(plan)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Plan:             plan,
		Dockerfile:       dockerfile,
		DockerfileSHA256: digest([]byte(dockerfile)),
	}, nil
}

func compileSpec(project fs.FS, spec Spec) (Plan, error) {
	if spec.Version != SchemaVersion {
		return Plan{}, fmt.Errorf("version must be %d", SchemaVersion)
	}
	adapterName := strings.ToLower(strings.TrimSpace(spec.Build.Adapter))
	if err := validateImage("build.base", spec.Build.Base); err != nil {
		return Plan{}, err
	}
	if spec.Runtime.Base == "" {
		spec.Runtime.Base = spec.Build.Base
	}
	if err := validateImage("runtime.base", spec.Runtime.Base); err != nil {
		return Plan{}, err
	}
	if spec.Build.Workdir == "" {
		spec.Build.Workdir = "/workspace"
	}
	if spec.Runtime.Workdir == "" {
		spec.Runtime.Workdir = "/app"
	}
	if !path.IsAbs(spec.Build.Workdir) || !path.IsAbs(spec.Runtime.Workdir) {
		return Plan{}, fmt.Errorf("build.workdir and runtime.workdir must be absolute paths")
	}
	if err := validateScripts("build.setup", spec.Build.Setup); err != nil {
		return Plan{}, err
	}
	if err := validateScripts("runtime.setup", spec.Runtime.Setup); err != nil {
		return Plan{}, err
	}
	if err := validateCommand("runtime.entrypoint", spec.Runtime.Entrypoint); err != nil {
		return Plan{}, err
	}

	resolved, err := resolveAdapter(project, adapterName, spec.Build)
	if err != nil {
		return Plan{}, err
	}
	dependencyInputs := resolved.DependencyInputs
	if err := validateInputs(project, "build.dependencies.inputs", dependencyInputs); err != nil {
		return Plan{}, err
	}
	compileInputs := resolved.CompileInputs
	if err := validateInputs(project, "build.compile.inputs", compileInputs); err != nil {
		return Plan{}, err
	}
	dependencyCommand := resolved.DependencyCommand
	compileCommand := resolved.CompileCommand
	if err := validateCommand("build.dependencies.command", dependencyCommand); err != nil {
		return Plan{}, err
	}
	if len(compileCommand) > 0 {
		if err := validateCommand("build.compile.command", compileCommand); err != nil {
			return Plan{}, err
		}
	}
	if len(spec.Runtime.Artifacts) == 0 {
		return Plan{}, fmt.Errorf("runtime.artifacts must contain at least one artifact")
	}
	for index, artifact := range spec.Runtime.Artifacts {
		if err := validateArtifact(index, artifact); err != nil {
			return Plan{}, err
		}
	}

	plan := Plan{SchemaVersion: SchemaVersion, Adapter: adapterName}
	if !strings.Contains(spec.Build.Base, "@sha256:") {
		plan.Warnings = append(plan.Warnings, "build.base is not digest-pinned")
	}
	if !strings.Contains(spec.Runtime.Base, "@sha256:") {
		plan.Warnings = append(plan.Warnings, "runtime.base is not digest-pinned")
	}
	plan.Warnings = append(plan.Warnings, resolved.Warnings...)
	plan.Steps = append(plan.Steps, Step{ID: "build.from", Stage: "build", Kind: "from", Image: spec.Build.Base})
	plan.Steps = append(plan.Steps, environmentSteps("build", spec.Build.Env)...)
	for index, script := range spec.Build.Setup {
		plan.Steps = append(plan.Steps, Step{ID: fmt.Sprintf("build.setup.%d", index+1), Stage: "build", Kind: "run-shell", Script: script})
	}
	plan.Steps = append(plan.Steps,
		Step{ID: "build.workdir", Stage: "build", Kind: "workdir", Directory: spec.Build.Workdir},
		Step{ID: "build.dependencies.inputs", Stage: "build", Kind: "copy-inputs", Sources: dependencyInputs, Destination: "./"},
		Step{ID: "build.dependencies.run", Stage: "build", Kind: "run", Command: dependencyCommand},
	)
	plan.Steps = append(plan.Steps, Step{ID: "build.compile.inputs", Stage: "build", Kind: "copy-inputs", Sources: compileInputs, Destination: "./"})
	if len(compileCommand) > 0 {
		plan.Steps = append(plan.Steps, Step{ID: "build.compile.run", Stage: "build", Kind: "run", Command: compileCommand})
	}
	plan.Steps = append(plan.Steps, Step{ID: "runtime.from", Stage: "runtime", Kind: "from", Image: spec.Runtime.Base})
	plan.Steps = append(plan.Steps, environmentSteps("runtime", spec.Runtime.Env)...)
	for index, script := range spec.Runtime.Setup {
		plan.Steps = append(plan.Steps, Step{ID: fmt.Sprintf("runtime.setup.%d", index+1), Stage: "runtime", Kind: "run-shell", Script: script})
	}
	plan.Steps = append(plan.Steps, Step{ID: "runtime.workdir", Stage: "runtime", Kind: "workdir", Directory: spec.Runtime.Workdir})
	for index, artifact := range spec.Runtime.Artifacts {
		plan.Steps = append(plan.Steps, Step{
			ID:          fmt.Sprintf("runtime.artifact.%d", index+1),
			Stage:       "runtime",
			Kind:        "copy-artifact",
			From:        "build",
			Sources:     []string{artifact.Source},
			Destination: artifact.Destination,
		})
	}
	plan.Steps = append(plan.Steps, Step{ID: "runtime.entrypoint", Stage: "runtime", Kind: "entrypoint", Command: append([]string(nil), spec.Runtime.Entrypoint...)})

	canonical, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, fmt.Errorf("encode canonical build plan: %w", err)
	}
	plan.PlanID = digest(canonical)
	return plan, nil
}

func environmentSteps(stage string, values map[string]string) []Step {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	steps := make([]Step, 0, len(names))
	for _, name := range names {
		steps = append(steps, Step{ID: stage + ".env." + name, Stage: stage, Kind: "env", Name: name, Value: values[name]})
	}
	return steps
}

func validateImage(field, value string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\t ") {
		return fmt.Errorf("%s must be a non-empty image reference without whitespace", field)
	}
	return nil
}

func validateScripts(field string, scripts []string) error {
	for index, script := range scripts {
		if strings.TrimSpace(script) == "" || strings.ContainsRune(script, 0) {
			return fmt.Errorf("%s[%d] must be a non-empty script without NUL bytes", field, index)
		}
	}
	return nil
}

func validateCommand(field string, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	for index, value := range command {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s[%d] must be a non-empty argv value without control characters", field, index)
		}
	}
	return nil
}

func validateInputs(project fs.FS, field string, inputs []string) error {
	seen := map[string]bool{}
	for index, input := range inputs {
		if input == "." {
			continue
		}
		clean := path.Clean(input)
		if clean != input || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsAny(clean, "*?[") {
			return fmt.Errorf("%s[%d] must be a normalized project-relative path without globs", field, index)
		}
		if seen[clean] {
			return fmt.Errorf("%s contains duplicate input %q", field, clean)
		}
		seen[clean] = true
		if _, err := fs.Stat(project, clean); err != nil {
			return fmt.Errorf("%s input %q: %w", field, clean, err)
		}
	}
	return nil
}

func validateArtifact(index int, artifact ArtifactSpec) error {
	if strings.TrimSpace(artifact.Source) == "" || strings.Contains(artifact.Source, "..") {
		return fmt.Errorf("runtime.artifacts[%d].source must not be empty or traverse parents", index)
	}
	if !path.IsAbs(artifact.Destination) || strings.Contains(artifact.Destination, "..") {
		return fmt.Errorf("runtime.artifacts[%d].destination must be an absolute path without parent traversal", index)
	}
	return nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validateEnvironmentStep(step Step) error {
	if !envNamePattern.MatchString(step.Name) {
		return fmt.Errorf("invalid environment variable name %q", step.Name)
	}
	if strings.ContainsRune(step.Value, 0) {
		return fmt.Errorf("environment variable %s contains a NUL byte", step.Name)
	}
	return nil
}
