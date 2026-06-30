package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/cli/swifttoolchain"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"golang.org/x/term"
)

// BuildResult is the output of the build command. Exactly one field is set.
type BuildResult struct {
	// ProviderApp is set when the build used an external provider.
	ProviderApp *providers.BuiltApp
}

type buildOptions struct {
	buildType  string
	dockerfile string
	builder    string
}

var appleContainerLocalProviderHintSupported = func() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

func newBuildCmd() *cobra.Command {
	var opts buildOptions

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the application in the current directory",
		Long:  "Detects the project type and builds a Docker image for the target device architecture.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.dockerfile != "" && opts.buildType != "" && normalizeBuildType(opts.buildType) != "docker" {
				return fmt.Errorf("--dockerfile cannot be used with --build-type=%s", opts.buildType)
			}
			if _, err := normalizeImageBuilder(opts.builder); err != nil {
				return err
			}
			// --dockerfile implies a Docker build; prevent the provider from
			// auto-selecting a Compose file when both markers are present.
			if opts.dockerfile != "" && opts.buildType == "" {
				opts.buildType = "docker"
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}

			if opts.dockerfile != "" {
				if err := validateDockerfileName(opts.dockerfile); err != nil {
					return fmt.Errorf("--dockerfile: %w", err)
				}
				if _, err := confinedDockerfilePath(cwd, opts.dockerfile); err != nil {
					return fmt.Errorf("--dockerfile: %w", err)
				}
			}

			cfgPath := filepath.Join(cwd, "wendy.json")
			appCfg, cfgErr := ensureAppConfig(cfgPath, false)
			if cfgErr == nil {
				if err := appCfg.Validate(); err != nil {
					return fmt.Errorf("invalid wendy.json: %w", err)
				}
				if err := warnAppConfigFile(cfgPath); err != nil {
					return fmt.Errorf("reading wendy.json warnings: %w", err)
				}
			}

			target, _ := resolveTarget(cmd.Context())

			// If the target is an external provider device, use the provider build path.
			if target != nil && target.External != nil && target.Provider != nil {
				if opts.builder != "" {
					return fmt.Errorf("--builder is only used when --device selects a WendyOS device; use --device docker or --device apple-container for local provider builds")
				}
				product := filepath.Base(cwd)
				if cfgErr == nil {
					product = appCfg.AppID
				}
				// For Swift projects, resolve the actual product name from Package.swift
				// rather than using the directory name (which may differ in casing).
				if _, err := os.Stat(filepath.Join(cwd, "Package.swift")); err == nil {
					if swiftProduct, err := swifttoolchain.FindSwiftProduct(cwd); err == nil {
						product = swiftProduct
					}
				}

				projectType, ptErr := resolveRunProjectType(cwd, opts.buildType)
				if ptErr != nil {
					return ptErr
				}
				if err := ensureProviderSupportsProjectType(target.Provider, projectType, cwd); err != nil {
					return err
				}

				// Swift projects without a container build file: cross-compile on the host and
				// build a Docker image, bypassing the provider's normal Build method.
				if projectType == "swift" {
					if _, ok := target.Provider.(providers.ImageBuilder); ok {
						if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
							return fmt.Errorf("`wendy build` for Swift packages is not supported on %s; provide a Dockerfile or Containerfile", runtime.GOOS)
						}
						if err := swifttoolchain.EnsureSwiftVersion(cmd.Context(), &dimWriter{}, os.Stderr); err != nil {
							return err
						}
						cliLogln("Building Swift project for %s...", target.Provider.DisplayName())
						// runtime.GOARCH is correct here: Docker Desktop loads images into the
						// host daemon, so the image must match the host architecture.
						if _, err := buildSwiftDockerImage(cmd.Context(), cwd, product, runtime.GOARCH, &dimWriter{}, os.Stderr); err != nil {
							return fmt.Errorf("building Swift Docker image: %w", err)
						}
						cliSuccess("Build completed successfully.")
						return nil
					}
				}

				// For docker-type projects, resolve which build file to use before
				// calling the provider — shows an interactive picker when multiple
				// build files exist and no --dockerfile flag was given.
				if projectType == "docker" && opts.dockerfile == "" {
					resolved, resolveErr := resolveDockerfile(cwd, "", isInteractiveTerminal())
					if resolveErr != nil {
						return resolveErr
					}
					opts.dockerfile = resolved
					if resolved != "" && opts.buildType == "" {
						opts.buildType = "docker"
					}
				}

				cliLogln("Building with %s provider...", target.Provider.DisplayName())
				var (
					app      *providers.BuiltApp
					buildErr error
				)
				if db, ok := target.Provider.(providers.DockerfileBuilder); ok && opts.dockerfile != "" {
					app, buildErr = db.BuildWithDockerfile(cmd.Context(), *target.External, cwd, product, opts.buildType, opts.dockerfile, false)
				} else if tb, ok := target.Provider.(providers.TypedBuilder); ok {
					app, buildErr = tb.BuildWithType(cmd.Context(), *target.External, cwd, product, opts.buildType, false)
				} else {
					app, buildErr = target.Provider.Build(cmd.Context(), *target.External, cwd, product, false)
				}
				if buildErr != nil {
					return fmt.Errorf("provider build: %w", buildErr)
				}
				cliSuccess("Build completed successfully (%s).", tui.Value(app.ProviderKey))
				return nil
			}

			// Close the agent connection if one was opened during target resolution.
			if target != nil && target.Agent != nil {
				defer target.Agent.Close()
			}

			// Detect all build options and filter by target capabilities.
			options := detectBuildOptions(cwd)
			if target != nil && target.Provider != nil {
				options = filterBuildOptions(options, target.Provider)
			}
			if len(options) == 0 {
				return fmt.Errorf("no supported build type found for this target; check that the project contains the right files")
			}

			selected, err := resolveDetectedBuildOption(options, opts.buildType, opts.dockerfile)
			if err != nil {
				return err
			}

			// Query the device OS and architecture when an agent connection is
			// available and determine the target platform.
			var cfgPlatform string
			if cfgErr == nil {
				cfgPlatform = appCfg.Platform
			}
			platform := "linux/arm64"
			if target != nil && target.Agent != nil {
				versionResp, err := target.Agent.AgentService.GetAgentVersion(cmd.Context(), &agentpb.GetAgentVersionRequest{})
				if err == nil {
					agentOS := versionResp.GetOs()
					if agentOS == "" {
						agentOS = "linux"
					}
					arch := versionResp.GetCpuArchitecture()
					if arch == "" {
						arch = "arm64"
					}
					platform = resolveAgentPlatform(cfgPlatform, agentOS, arch)
				}
			}

			appID := filepath.Base(cwd)
			if cfgErr == nil {
				appID = appCfg.AppID
			}

			return buildProject(cmd.Context(), cwd, selected, appID, platform, opts.builder)
		},
	}

	cmd.Flags().StringVar(&opts.buildType, "build-type", "", "Build type to use when multiple project markers are present: docker, swift, or python")
	cmd.Flags().StringVar(&opts.dockerfile, "dockerfile", "", "Dockerfile or Containerfile to build from (e.g. Dockerfile.prod or Containerfile); shows a selection menu when multiple build files exist")
	cmd.Flags().StringVar(&opts.builder, "builder", "", "Image builder to force for Dockerfile/Containerfile builds: docker or apple-container")

	return cmd
}

func resolveDetectedBuildOption(options []BuildOption, requestedType, requestedDockerfile string) (*BuildOption, error) {
	interactive := term.IsTerminal(int(os.Stdin.Fd()))

	// --dockerfile selects a specific Dockerfile directly, bypassing type detection.
	if strings.TrimSpace(requestedDockerfile) != "" {
		// Normalise "./Dockerfile.prod" → "Dockerfile.prod" so the flag value
		// matches the plain filenames stored in BuildOption.File.
		normalizedDockerfile := filepath.Clean(requestedDockerfile)
		for i := range options {
			if options[i].Type == "docker" && options[i].File == normalizedDockerfile {
				return &options[i], nil
			}
		}
		return nil, fmt.Errorf("dockerfile %q not found; detected %s", requestedDockerfile, strings.Join(buildOptionLabels(options), ", "))
	}

	if strings.TrimSpace(requestedType) != "" {
		return buildOptionForType(options, requestedType, interactive)
	}

	if preferred := preferredBuildOption(options, interactive); preferred != nil {
		return preferred, nil
	}

	// Non-interactive (CI) fallback: when all detected options are container build
	// files, prefer the base "Dockerfile" or "Containerfile" and fall back to the
	// first variant rather than failing with "multiple build types detected".
	// This mirrors the run-command behaviour and lets CI pipelines that omit
	// --dockerfile build predictably.
	if !interactive {
		allDocker := len(options) > 0
		for _, opt := range options {
			if opt.Type != "docker" {
				allDocker = false
				break
			}
		}
		if allDocker {
			if len(options) == 1 {
				return &options[0], nil
			}
			if preferred := preferredContainerBuildFileOption(options); preferred != nil {
				cliNotice("multiple container build files detected; using %q. Use --dockerfile to select explicitly.", preferred.File)
				return preferred, nil
			}
			cliNotice("multiple container build files detected; using %q. Use --dockerfile to select explicitly.", options[0].File)
			return &options[0], nil
		}
	}

	return pickBuildOption(options)
}

// pickBuildOption presents an interactive picker when multiple build options
// are detected. If only one option exists, it is returned directly.
func pickBuildOption(options []BuildOption) (*BuildOption, error) {
	return pickBuildOptionWithTitle(options, "Select a build type")
}

func pickBuildOptionWithTitle(options []BuildOption, title string) (*BuildOption, error) {
	if len(options) == 1 {
		return &options[0], nil
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		var names []string
		for _, o := range options {
			names = append(names, o.Label)
		}
		return nil, fmt.Errorf("multiple build types detected (%s); run in an interactive terminal or remove extra build markers so that only one remains", strings.Join(names, ", "))
	}

	picker := tui.NewPickerWithTitle(title)
	p := tea.NewProgram(picker)

	go func() {
		var items []tui.PickerItem
		for i := range options {
			items = append(items, tui.PickerItem{
				Name:  options[i].Label,
				Value: &options[i],
			})
		}
		p.Send(tui.PickerAddMsg{Items: items})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("build type picker: %w", err)
	}

	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return nil, ErrUserCancelled
	}
	sel := pm.Selected()
	if sel == nil {
		return nil, fmt.Errorf("no build type selected")
	}

	opt, ok := sel.Value.(*BuildOption)
	if !ok {
		return nil, fmt.Errorf("invalid selection")
	}
	return opt, nil
}

func preferredBuildOption(options []BuildOption, interactive bool) *BuildOption {
	hasLanguageMarker := false
	dockerCount := 0
	for i := range options {
		switch {
		case options[i].Type == "swift" || options[i].Type == "python":
			hasLanguageMarker = true
		case options[i].Type == "docker":
			dockerCount++
		}
	}
	buildFile := preferredContainerBuildFileOption(options)
	if !hasLanguageMarker || buildFile == nil {
		return nil
	}
	if dockerCount == 1 || !interactive {
		return buildFile
	}
	return nil
}

func buildOptionForType(options []BuildOption, requestedType string, interactive bool) (*BuildOption, error) {
	buildType := normalizeBuildType(requestedType)
	if buildType == "" {
		return nil, fmt.Errorf("build type must be one of docker, swift, or python")
	}

	var matches []BuildOption
	for _, option := range options {
		if option.Type == buildType {
			matches = append(matches, option)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("build type %q is not available; detected %s", requestedType, strings.Join(buildOptionLabels(options), ", "))
	}

	if buildType == "docker" {
		buildFile := preferredContainerBuildFileOption(matches)
		if buildFile != nil && !interactive {
			return buildFile, nil
		}
		if len(matches) > 1 {
			if interactive {
				return pickBuildOptionWithTitle(matches, "Select a container build file")
			}
			return nil, fmt.Errorf("multiple container build files detected (%s); keep only one build file or omit --build-type to choose interactively", strings.Join(buildOptionLabels(matches), ", "))
		}
		if buildFile != nil {
			return buildFile, nil
		}
	}

	return &matches[0], nil
}

func buildOptionLabels(options []BuildOption) []string {
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	return labels
}

func normalizeBuildType(buildType string) string {
	switch strings.ToLower(strings.TrimSpace(buildType)) {
	case "docker", "swift", "python", "compose":
		return strings.ToLower(strings.TrimSpace(buildType))
	default:
		return ""
	}
}

// filterBuildOptions removes options whose Type is not in the provider's
// SupportedBuildTypes list.
func filterBuildOptions(options []BuildOption, provider providers.DeviceProvider) []BuildOption {
	supported := make(map[string]bool)
	for _, t := range provider.SupportedBuildTypes() {
		supported[t] = true
	}
	var filtered []BuildOption
	for _, o := range options {
		if supported[o.Type] {
			filtered = append(filtered, o)
		}
	}
	return filtered
}

func ensureProviderSupportsProjectType(provider providers.DeviceProvider, projectType, projectPath string) error {
	if projectType == "unknown" && provider.CanBuild(projectPath) {
		return nil
	}
	if providerSupportsProjectType(provider, projectType) {
		return nil
	}

	providerName := provider.DisplayName()

	if provider.Key() == providers.ProviderKeyLocal && (projectType == "docker" || projectType == "compose") {
		containerTargets := "Docker with --device docker"
		if projectType == "docker" && appleContainerLocalProviderHintSupported() {
			containerTargets += " or Apple Container with --device apple-container"
		}
		return fmt.Errorf("%s runs host-native apps and does not support %s projects; choose %s for local container runs", providerName, projectType, containerTargets)
	}

	return fmt.Errorf("%s provider does not support %s projects; supported build types: %s", providerName, projectType, strings.Join(provider.SupportedBuildTypes(), ", "))
}

func providerSupportsProjectType(provider providers.DeviceProvider, projectType string) bool {
	if projectType == "swift" {
		if _, ok := provider.(providers.ImageBuilder); ok {
			return true
		}
	}
	for _, supported := range provider.SupportedBuildTypes() {
		if supported == projectType {
			return true
		}
	}
	return false
}

// detectProjectTypeWithLanguage determines the project type using the wendy.json
// language field as a hint, falling back to filesystem detection.
func detectProjectTypeWithLanguage(dir, language string) string {
	switch language {
	case "python":
		return "python"
	case "swift":
		return "swift"
	}
	t, _ := detectProjectType(dir) // ignore multiple-xcodeproj error for picker pre-filtering
	return t
}

func buildProject(ctx context.Context, dir string, option *BuildOption, appID, platform, builder string) error {
	imageName := strings.ToLower(appID) + ":latest"
	normalizedBuilder, err := normalizeImageBuilder(builder)
	if err != nil {
		return err
	}

	switch option.Type {
	case "compose":
		if normalizedBuilder == imageBuilderAppleContainer {
			return fmt.Errorf("Apple Container builder does not support Compose builds; use --builder docker")
		}
		return buildComposeProject(dir)
	case "docker":
		return buildDockerProjectWithBuilder(ctx, builder, dir, imageName, platform, option.File)
	case "python":
		return buildPythonProject(ctx, builder, dir, imageName, platform)
	case "swift":
		if normalizedBuilder == imageBuilderAppleContainer {
			return fmt.Errorf("Apple Container builder is only supported for Dockerfile/Containerfile builds; provide a build file or omit --builder")
		}
		// Cross-compiling Swift requires a host toolchain; only darwin and linux ship one.
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			return fmt.Errorf("`wendy build` for Swift packages is not supported on %s; provide a Dockerfile or Containerfile", runtime.GOOS)
		}
		return buildSwiftContainerProject(ctx, dir, appID, platform)
	case "xcode":
		if normalizedBuilder == imageBuilderAppleContainer {
			return fmt.Errorf("Apple Container builder is only supported for Dockerfile/Containerfile builds; provide a build file or omit --builder")
		}
		return buildXcodeProject(ctx, dir, option.File)
	default:
		return fmt.Errorf("unknown project type; add a Dockerfile/Containerfile, a Compose file (docker-compose.yml, docker-compose.yaml, compose.yml, or compose.yaml), Package.swift, or requirements.txt")
	}
}

func buildComposeProject(dir string) error {
	cliLogln("Building Compose services...")
	cmd := exec.Command("docker", "compose", "build")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}
	cliSuccess("Build completed successfully.")
	return nil
}

func buildDockerProject(dir, imageName, platform, dockerfile string) error {
	cliLogln("Building Docker image %s for %s...", tui.Value(imageName), tui.Value(platform))

	cmd := exec.Command("docker", "buildx", "build",
		"--platform", platform,
		"-f", dockerfile,
		"-t", imageName,
		"--load",
		".")
	cmd.Dir = dir

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		cliSuccess("Build completed successfully.")
		return nil
	}

	s := tui.NewSpinner("Building Docker image...")
	p := tui.NewProgressProgram(s)

	go func() {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		p.Send(tui.SpinnerDoneMsg{Err: err})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	model := finalModel.(tui.SpinnerModel)
	_, buildErr := model.Result()
	if buildErr != nil {
		return buildErr
	}

	cliSuccess("Build completed successfully.")
	return nil
}

func buildDockerProjectWithBuilder(ctx context.Context, builder, dir, imageName, platform, dockerfile string) error {
	normalized, err := normalizeImageBuilder(builder)
	if err != nil {
		return err
	}
	if !imageBuilderWasExplicit(builder) && shouldAutoAttemptAppleContainerBuilder() {
		if err := checkAppleContainerBuilder(ctx); err == nil {
			if err := runAppleContainerBuildWithProgress(ctx, dir, imageName, platform, dockerfile); err == nil {
				return nil
			} else {
				logAppleContainerFallback(os.Stderr, err)
			}
		} else {
			logAppleContainerFallback(os.Stderr, err)
		}
	}
	if normalized == imageBuilderDocker {
		return buildDockerProjectWithDocker(dir, imageName, platform, dockerfile)
	}

	if err := ensureAppleContainerSystem(ctx, false); err != nil {
		return err
	}
	return runAppleContainerBuildWithProgress(ctx, dir, imageName, platform, dockerfile)
}

func buildPythonProject(ctx context.Context, builder, dir, imageName, platform string) error {
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	generatedDockerfile := false
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		cliLogln("No Dockerfile found. Generating one for Python project...")
		if _, genErr := generatePythonDockerfile(dir, false); genErr != nil {
			return fmt.Errorf("generating Dockerfile: %w", genErr)
		}
		generatedDockerfile = true
		cliSuccess("Generated Dockerfile.")
	}

	err := buildDockerProjectWithBuilder(ctx, builder, dir, imageName, platform, "Dockerfile")

	if generatedDockerfile {
		os.Remove(dockerfilePath)
	}

	return err
}

func buildXcodeProject(ctx context.Context, dir, xcodeproj string) error {
	// Resolve scheme: honour wendy.json override, then auto-detect.
	scheme := ""
	if cfg, err := appconfig.LoadFromFile(filepath.Join(dir, "wendy.json")); err == nil && cfg.Xcode != nil {
		scheme = cfg.Xcode.Scheme
	}
	if scheme == "" {
		var err error
		scheme, err = findXcodeScheme(ctx, dir)
		if err != nil {
			return err
		}
	}

	cliLogln("Building Xcode project %s (scheme: %s)...", tui.Value(xcodeproj), tui.Value(scheme))
	// SECURITY: Xcode project support exists for native Mac packages that cannot be
	// built correctly with SwiftPM alone today, for example packages that require
	// Xcode-only resource or shader build steps (see
	// docs/clients/wendy-cli/commands/build.md).
	// Xcode's macro/plugin prompts are an interactive consent layer on top of
	// SwiftPM's build-time code/sandbox model; headless Wendy CLI builds cannot
	// answer those prompts, so we deliberately make xcodebuild behave like CLI
	// build tools and rely on a trusted, pinned Package.resolved.
	if err := runXcodebuild(ctx, dir,
		"-project", xcodeproj,
		"-scheme", scheme,
		"-configuration", "Release",
		"-derivedDataPath", ".xcode/",
		"-skipMacroValidation",
		"-skipPackagePluginValidation",
	); err != nil {
		return err
	}
	cliSuccess("Build completed successfully.")
	return nil
}

func buildSwiftContainerProject(ctx context.Context, dir, appID, platform string) error {
	if err := swifttoolchain.EnsureSwiftVersion(ctx, &dimWriter{}, os.Stderr); err != nil {
		return err
	}

	product, err := swifttoolchain.FindSwiftProduct(dir)
	if err != nil {
		cliLogln("Warning: could not detect Swift product name (%v); using %q", err, appID)
		product = appID
	}

	arch := runtime.GOARCH
	if parts := strings.SplitN(platform, "/", 2); len(parts) == 2 {
		arch = parts[1]
	}

	if _, err := buildSwiftDockerImage(ctx, dir, product, arch, &dimWriter{}, os.Stderr); err != nil {
		return err
	}
	cliSuccess("Build completed successfully.")
	return nil
}
