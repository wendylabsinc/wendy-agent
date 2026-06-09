package providers

import (
	"bufio"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// androidBuildContext is stored in BuiltApp.Context for ADB builds.
type androidBuildContext struct {
	APKPath    string
	PackageID  string
	ActivityID string
	Serial     string
}

// AndroidProvider builds Swift packages into APKs and deploys via ADB.
type AndroidProvider struct{}

func (p *AndroidProvider) Key() string         { return "android" }
func (p *AndroidProvider) DisplayName() string { return "Android (ADB)" }

func (p *AndroidProvider) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "adb", "version")
	return cmd.Run() == nil
}

func (p *AndroidProvider) CheckRequirements(ctx context.Context) error {
	if !p.IsAvailable(ctx) {
		return fmt.Errorf("adb is not installed or not in PATH")
	}
	if cmd := exec.CommandContext(ctx, "swiftly", "--version"); cmd.Run() != nil {
		return fmt.Errorf("swiftly is not installed (needed for Swift Android SDK)")
	}
	return nil
}

func (p *AndroidProvider) DiscoverDevices(ctx context.Context) ([]models.ExternalDevice, error) {
	cmd := exec.CommandContext(ctx, "adb", "devices", "-l")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	var devices []models.ExternalDevice
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		// Skip header and empty lines.
		if strings.HasPrefix(line, "List of") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "device" {
			continue
		}
		serial := fields[0]

		// Extract model from the key=value pairs.
		displayName := serial
		for _, f := range fields[2:] {
			if strings.HasPrefix(f, "model:") {
				displayName = strings.TrimPrefix(f, "model:")
				break
			}
		}

		devices = append(devices, models.ExternalDevice{
			ID:              "adb:" + serial,
			DisplayName:     displayName,
			ProviderKey:     p.Key(),
			ConnectionInfo:  map[string]string{"serial": serial},
			IsWendyDevice:   false,
			OS:              "android",
			CPUArchitecture: "arm64",
		})
	}
	return devices, nil
}

func (p *AndroidProvider) SupportedBuildTypes() []string {
	return []string{"swift"}
}

func (p *AndroidProvider) CanBuild(projectPath string) bool {
	_, err := os.Stat(projectPath + "/Package.swift")
	return err == nil
}

func (p *AndroidProvider) Build(ctx context.Context, device models.ExternalDevice, projectPath, product string, debug bool) (*BuiltApp, error) {
	// Use swiftly to invoke the Swift Android SDK bundle-apk command.
	args := []string{"run", "+main-snapshot", "swift", "package", "--disable-sandbox", "--allow-writing-to-package-directory", "bundle-apk", "--product", product}
	cmd := exec.CommandContext(ctx, "swiftly", args...)
	cmd.Dir = projectPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("swift bundle-apk: %w", err)
	}

	packageID, activityName, err := parseAndroidManifest(projectPath)
	if err != nil {
		return nil, fmt.Errorf("parsing AndroidManifest.xml: %w", err)
	}

	serial := device.ConnectionInfo["serial"]
	return &BuiltApp{
		ProviderKey: p.Key(),
		Device:      device,
		AppName:     product,
		Context: &androidBuildContext{
			APKPath:    ".build-apk/" + product + ".apk",
			PackageID:  packageID,
			ActivityID: packageID + "/" + activityName,
			Serial:     serial,
		},
	}, nil
}

// parseAndroidManifest reads AndroidManifest.xml from the project directory
func parseAndroidManifest(projectPath string) (packageID, activityName string, err error) {
	data, err := os.ReadFile(filepath.Join(projectPath, "AndroidManifest.xml"))
	if err != nil {
		return "", "", fmt.Errorf("reading AndroidManifest.xml: %w", err)
	}

	var manifest struct {
		Package     string `xml:"package,attr"`
		Application struct {
			Activities []struct {
				Name         string `xml:"http://schemas.android.com/apk/res/android name,attr"`
				IntentFilter []struct {
					Actions []struct {
						Name string `xml:"http://schemas.android.com/apk/res/android name,attr"`
					} `xml:"action"`
					Categories []struct {
						Name string `xml:"http://schemas.android.com/apk/res/android name,attr"`
					} `xml:"category"`
				} `xml:"intent-filter"`
			} `xml:"activity"`
		} `xml:"application"`
	}

	if err := xml.Unmarshal(data, &manifest); err != nil {
		return "", "", fmt.Errorf("parsing AndroidManifest.xml: %w", err)
	}

	if manifest.Package == "" {
		return "", "", fmt.Errorf("no package attribute in AndroidManifest.xml")
	}

	// Find the launcher activity.
	for _, activity := range manifest.Application.Activities {
		for _, filter := range activity.IntentFilter {
			hasMain := false
			hasLauncher := false
			for _, action := range filter.Actions {
				if action.Name == "android.intent.action.MAIN" {
					hasMain = true
				}
			}
			for _, cat := range filter.Categories {
				if cat.Name == "android.intent.category.LAUNCHER" {
					hasLauncher = true
				}
			}
			if hasMain && hasLauncher {
				return manifest.Package, activity.Name, nil
			}
		}
	}

	return "", "", fmt.Errorf("no launcher activity found in AndroidManifest.xml")
}

func (p *AndroidProvider) Run(ctx context.Context, app *BuiltApp, detach bool, output chan<- RunOutput) error {
	defer close(output)

	bc, ok := app.Context.(*androidBuildContext)
	if !ok {
		return fmt.Errorf("android provider: invalid build context")
	}

	serialArgs := []string{}
	if bc.Serial != "" {
		serialArgs = []string{"-s", bc.Serial}
	}

	// Install the APK.
	installArgs := append(serialArgs, "install", "-r", bc.APKPath)
	cmd := exec.CommandContext(ctx, "adb", installArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("adb install: %w", err)
	}

	// Launch the activity.
	startArgs := append(serialArgs, "shell", "am", "start", "-n", bc.ActivityID)
	cmd = exec.CommandContext(ctx, "adb", startArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("adb am start: %w", err)
	}

	output <- RunOutput{Type: RunOutputStarted}

	if detach {
		return nil
	}

	// Stream logcat for this package.
	logcatArgs := append(serialArgs, "logcat", "--pid", fmt.Sprintf("$(adb %s shell pidof %s)", strings.Join(serialArgs, " "), bc.PackageID))
	// Simplified: stream all logcat filtered by tag.
	logcatArgs = append(serialArgs, "logcat", "-v", "brief")
	logCmd := exec.CommandContext(ctx, "adb", logcatArgs...)
	stdoutPipe, err := logCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("logcat stdout pipe: %w", err)
	}
	if err := logCmd.Start(); err != nil {
		return fmt.Errorf("adb logcat: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		output <- RunOutput{Type: RunOutputStdout, Data: append(scanner.Bytes(), '\n')}
	}
	return logCmd.Wait()
}

func (p *AndroidProvider) Stop(ctx context.Context, app *BuiltApp) error {
	bc, ok := app.Context.(*androidBuildContext)
	if !ok {
		return fmt.Errorf("android provider: invalid build context")
	}
	serialArgs := []string{}
	if bc.Serial != "" {
		serialArgs = []string{"-s", bc.Serial}
	}
	stopArgs := append(serialArgs, "shell", "am", "force-stop", bc.PackageID)
	cmd := exec.CommandContext(ctx, "adb", stopArgs...)
	return cmd.Run()
}
