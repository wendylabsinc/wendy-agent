package buildspec

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type resolvedAdapter struct {
	DependencyInputs  []string
	DependencyCommand []string
	CompileInputs     []string
	CompileCommand    []string
	Warnings          []string
}

func resolveAdapter(project fs.FS, name string, build BuildSpec) (resolvedAdapter, error) {
	var resolved resolvedAdapter
	var err error
	switch name {
	case "swift":
		resolved, err = resolveSwift(project)
	case "node":
		resolved, err = resolveNode(project)
	case "python":
		resolved, err = resolvePython(project)
	case "go":
		resolved, err = resolveGo(project)
	case "rust":
		resolved, err = resolveRust(project)
	case "custom":
		resolved, err = resolveCustom(build)
	default:
		return resolvedAdapter{}, fmt.Errorf("build.adapter must be one of swift, node, python, go, rust, or custom")
	}
	if err != nil {
		return resolvedAdapter{}, err
	}
	if len(build.Dependencies.Inputs) > 0 {
		resolved.DependencyInputs = append([]string(nil), build.Dependencies.Inputs...)
	}
	if len(build.Dependencies.Command) > 0 {
		resolved.DependencyCommand = append([]string(nil), build.Dependencies.Command...)
	}
	if len(build.Compile.Inputs) > 0 {
		resolved.CompileInputs = append([]string(nil), build.Compile.Inputs...)
	}
	if build.Compile.Command != nil {
		resolved.CompileCommand = append([]string(nil), build.Compile.Command...)
	}
	return resolved, nil
}

func resolveSwift(project fs.FS) (resolvedAdapter, error) {
	manifest, err := requiredFile(project, "Swift", "Package.swift")
	if err != nil {
		return resolvedAdapter{}, err
	}
	text := strings.ToLower(string(manifest))
	if strings.Contains(text, ".package(path:") ||
		(strings.Contains(text, ".package(") && strings.Contains(text, "path:")) {
		return resolvedAdapter{}, fmt.Errorf("Swift local path dependencies are not supported in version 1")
	}
	inputs := []string{"Package.swift"}
	if fileExists(project, "Package.resolved") {
		inputs = append(inputs, "Package.resolved")
	}
	return resolvedAdapter{
		DependencyInputs:  inputs,
		DependencyCommand: []string{"swift", "package", "resolve"},
		CompileInputs:     []string{"."},
		CompileCommand:    []string{"swift", "build", "-c", "release"},
	}, nil
}

func resolveNode(project fs.FS) (resolvedAdapter, error) {
	manifest, err := requiredFile(project, "Node", "package.json")
	if err != nil {
		return resolvedAdapter{}, err
	}
	var metadata struct {
		Workspaces   json.RawMessage   `json:"workspaces"`
		Dependencies map[string]string `json:"dependencies"`
		Dev          map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		return resolvedAdapter{}, fmt.Errorf("Node adapter parse package.json: %w", err)
	}
	if len(metadata.Workspaces) > 0 && string(metadata.Workspaces) != "null" {
		return resolvedAdapter{}, fmt.Errorf("Node workspaces are not supported in version 1")
	}
	for name, value := range mergeDependencies(metadata.Dependencies, metadata.Dev) {
		if strings.HasPrefix(value, "file:") || strings.HasPrefix(value, "workspace:") {
			return resolvedAdapter{}, fmt.Errorf("Node local dependency %q is not supported in version 1", name)
		}
	}
	locks := existingFiles(project, "package-lock.json", "pnpm-lock.yaml", "yarn.lock")
	if len(locks) != 1 {
		return resolvedAdapter{}, fmt.Errorf("Node adapter requires exactly one of package-lock.json, pnpm-lock.yaml, or yarn.lock")
	}
	resolved := resolvedAdapter{DependencyInputs: []string{"package.json", locks[0]}, CompileInputs: []string{"."}}
	switch locks[0] {
	case "package-lock.json":
		resolved.DependencyCommand = []string{"npm", "ci"}
		resolved.CompileCommand = []string{"npm", "run", "build"}
	case "pnpm-lock.yaml":
		resolved.DependencyCommand = []string{"pnpm", "install", "--frozen-lockfile"}
		resolved.CompileCommand = []string{"pnpm", "run", "build"}
	case "yarn.lock":
		resolved.DependencyCommand = []string{"yarn", "install", "--frozen-lockfile"}
		resolved.CompileCommand = []string{"yarn", "build"}
	}
	return resolved, nil
}

func resolvePython(project fs.FS) (resolvedAdapter, error) {
	hasPyproject := fileExists(project, "pyproject.toml")
	hasUV := fileExists(project, "uv.lock")
	hasPoetry := fileExists(project, "poetry.lock")
	hasRequirements := fileExists(project, "requirements.txt")
	choices := 0
	if hasUV {
		choices++
	}
	if hasPoetry {
		choices++
	}
	if hasRequirements {
		choices++
	}
	if choices != 1 {
		return resolvedAdapter{}, fmt.Errorf("Python adapter requires exactly one dependency mode: uv.lock, poetry.lock, or requirements.txt")
	}
	resolved := resolvedAdapter{CompileInputs: []string{"."}, CompileCommand: []string{}}
	switch {
	case hasUV:
		if !hasPyproject {
			return resolvedAdapter{}, fmt.Errorf("Python uv adapter requires pyproject.toml with uv.lock")
		}
		resolved.DependencyInputs = []string{"pyproject.toml", "uv.lock"}
		resolved.DependencyCommand = []string{"uv", "sync", "--frozen", "--no-install-project"}
	case hasPoetry:
		if !hasPyproject {
			return resolvedAdapter{}, fmt.Errorf("Python Poetry adapter requires pyproject.toml with poetry.lock")
		}
		resolved.DependencyInputs = []string{"pyproject.toml", "poetry.lock"}
		resolved.DependencyCommand = []string{"poetry", "install", "--no-root", "--no-interaction"}
	case hasRequirements:
		requirements, err := fs.ReadFile(project, "requirements.txt")
		if err != nil {
			return resolvedAdapter{}, fmt.Errorf("Python adapter read requirements.txt: %w", err)
		}
		for _, line := range strings.Split(string(requirements), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if line == "" {
				continue
			}
			if line == "." || strings.HasPrefix(line, "-e ") || strings.HasPrefix(line, "--editable ") ||
				strings.HasPrefix(line, "-r ") || strings.HasPrefix(line, "--requirement ") ||
				strings.HasPrefix(line, "-c ") || strings.HasPrefix(line, "--constraint ") {
				return resolvedAdapter{}, fmt.Errorf("Python requirements include local or nested input %q, unsupported in version 1", line)
			}
		}
		resolved.DependencyInputs = []string{"requirements.txt"}
		resolved.DependencyCommand = []string{"python", "-m", "pip", "install", "-r", "requirements.txt"}
	}
	return resolved, nil
}

func resolveGo(project fs.FS) (resolvedAdapter, error) {
	manifest, err := requiredFile(project, "Go", "go.mod")
	if err != nil {
		return resolvedAdapter{}, err
	}
	text := string(manifest)
	if strings.Contains(text, "=> ./") || strings.Contains(text, "=> ../") {
		return resolvedAdapter{}, fmt.Errorf("Go local replace directives are not supported in version 1")
	}
	inputs := []string{"go.mod"}
	if fileExists(project, "go.sum") {
		inputs = append(inputs, "go.sum")
	}
	return resolvedAdapter{
		DependencyInputs:  inputs,
		DependencyCommand: []string{"go", "mod", "download"},
		CompileInputs:     []string{"."},
		CompileCommand:    []string{"go", "build", "-o", "/out/app", "."},
	}, nil
}

func resolveRust(project fs.FS) (resolvedAdapter, error) {
	manifest, err := requiredFile(project, "Rust", "Cargo.toml")
	if err != nil {
		return resolvedAdapter{}, err
	}
	text := strings.ToLower(string(manifest))
	if strings.Contains(text, "[workspace]") || strings.Contains(text, "path =") || strings.Contains(text, "path=") {
		return resolvedAdapter{}, fmt.Errorf("Rust workspaces and local path dependencies are not supported in version 1")
	}
	inputs := []string{"Cargo.toml"}
	command := []string{"cargo", "fetch"}
	warnings := []string{"Cargo.lock is absent; Rust dependency resolution is not fully reproducible"}
	if fileExists(project, "Cargo.lock") {
		inputs = append(inputs, "Cargo.lock")
		command = []string{"cargo", "fetch", "--locked"}
		warnings = nil
	}
	compileCommand := []string{"cargo", "build", "--release"}
	if len(warnings) == 0 {
		compileCommand = append(compileCommand, "--locked")
	}
	return resolvedAdapter{
		DependencyInputs:  inputs,
		DependencyCommand: command,
		CompileInputs:     []string{"."},
		CompileCommand:    compileCommand,
		Warnings:          warnings,
	}, nil
}

func resolveCustom(build BuildSpec) (resolvedAdapter, error) {
	if len(build.Dependencies.Inputs) == 0 || len(build.Dependencies.Command) == 0 {
		return resolvedAdapter{}, fmt.Errorf("custom adapter requires build.dependencies.inputs and build.dependencies.command")
	}
	compileInputs := append([]string(nil), build.Compile.Inputs...)
	if len(compileInputs) == 0 {
		compileInputs = []string{"."}
	}
	return resolvedAdapter{
		DependencyInputs:  append([]string(nil), build.Dependencies.Inputs...),
		DependencyCommand: append([]string(nil), build.Dependencies.Command...),
		CompileInputs:     compileInputs,
		CompileCommand:    append([]string(nil), build.Compile.Command...),
	}, nil
}

func requiredFile(project fs.FS, adapter, name string) ([]byte, error) {
	data, err := fs.ReadFile(project, name)
	if err != nil {
		return nil, fmt.Errorf("%s adapter requires %s: %w", adapter, name, err)
	}
	return data, nil
}

func fileExists(project fs.FS, name string) bool {
	_, err := fs.Stat(project, name)
	return err == nil
}

func existingFiles(project fs.FS, names ...string) []string {
	var result []string
	for _, name := range names {
		if fileExists(project, name) {
			result = append(result, name)
		}
	}
	return result
}

func mergeDependencies(first, second map[string]string) map[string]string {
	result := make(map[string]string, len(first)+len(second))
	for name, value := range first {
		result[name] = value
	}
	for name, value := range second {
		result[name] = value
	}
	return result
}
