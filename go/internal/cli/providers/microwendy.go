package providers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/swifttoolchain"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

const (
	// microWendyUDPPort is the port ESP32 devices listen on for WENDY_RELOAD messages.
	microWendyUDPPort = 4210

	// microWendyServiceType is the mDNS service type advertised by ESP32 Wendy devices.
	microWendyServiceType = "_wendy-lite._tcp"

	// Currently supported SDK for WASI on Wendy Lite.
	// This also works for older projects that expected to build for wasm32-unknown-none-wasm
	microWendyEmbeddedSDK = "-embedded"
	microWendySwiftTarget = "wasm32-unknown-wasip1"
)

// microWendyBuildContext is stored in BuiltApp.Context for WASM builds.
type microWendyBuildContext struct {
	WASMPath string
}

// MicroWendyProvider builds Swift packages to WASM and serves them to ESP32 devices.
type MicroWendyProvider struct{}

func (p *MicroWendyProvider) Key() string         { return "wendy-lite" }
func (p *MicroWendyProvider) DisplayName() string { return "Micro Wendy (WASM)" }

func (p *MicroWendyProvider) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "swiftly", "--version")
	return cmd.Run() == nil
}

func (p *MicroWendyProvider) CheckRequirements(ctx context.Context) error {
	if !p.IsAvailable(ctx) {
		return fmt.Errorf("swiftly is not installed or not in PATH")
	}
	return nil
}

func (p *MicroWendyProvider) DiscoverDevices(ctx context.Context) ([]models.ExternalDevice, error) {
	services, err := discovery.BrowseMDNSServices(ctx, microWendyServiceType, 3*time.Second)
	if err != nil {
		return nil, err
	}

	var devices []models.ExternalDevice
	for _, svc := range services {
		displayName := svc.InstanceName
		if displayName == "" {
			displayName = svc.Hostname
		}

		devices = append(devices, models.ExternalDevice{
			ID:          fmt.Sprintf("wendy-lite:%s", svc.Hostname),
			DisplayName: displayName,
			ProviderKey: p.Key(),
			ConnectionInfo: map[string]string{
				"hostname": svc.Hostname,
				"ip":       svc.IPAddress,
				"port":     fmt.Sprintf("%d", svc.Port),
			},
			IsWendyDevice:   true,
			CPUArchitecture: "wasm32",
		})
	}

	return devices, nil
}

func (p *MicroWendyProvider) SupportedBuildTypes() []string {
	return []string{"swift"}
}

func (p *MicroWendyProvider) CanBuild(projectPath string) bool {
	_, err := os.Stat(filepath.Join(projectPath, "Package.swift"))
	return err == nil
}

func (p *MicroWendyProvider) Build(ctx context.Context, device models.ExternalDevice, projectPath, product string, debug bool) (*BuiltApp, error) {
	if err := swifttoolchain.EnsureSwiftVersion(ctx, os.Stdout, os.Stderr); err != nil {
		return nil, err
	}

	sdk, err := swifttoolchain.FindSwiftSDK(ctx, "wasm32", os.Stdout, os.Stderr)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(sdk, microWendyEmbeddedSDK) {
		sdk += microWendyEmbeddedSDK
	}

	args := []string{"build", "--swift-sdk", sdk, "--triple", microWendySwiftTarget}
	if !debug {
		args = append(args, "-c", "release")
	}
	cmd := swifttoolchain.SwiftCommandContext(ctx, args...)
	cmd.Dir = projectPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("swift build (wasi): %w", err)
	}

	binArgs := []string{
		"build",
		"--swift-sdk", sdk,
		"--triple", microWendySwiftTarget,
		"--product", product,
		"--show-bin-path",
	}
	if !debug {
		binArgs = append(binArgs, "-c", "release")
	}
	binCmd := swifttoolchain.SwiftCommandContext(ctx, binArgs...)
	binCmd.Dir = projectPath
	binCmd.Stderr = os.Stderr
	out, err := binCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("swift build --show-bin-path: %w", err)
	}

	binDir := strings.TrimSpace(string(out))
	if binDir == "" {
		return nil, fmt.Errorf("swift build --show-bin-path returned an empty path for %s", product)
	}

	wasmPath := filepath.Join(binDir, product+".wasm")
	if _, err := os.Stat(wasmPath); err != nil {
		return nil, fmt.Errorf("expected WASM output at %s: %w", wasmPath, err)
	}

	// Collect IPs of all known devices for unicast delivery.
	var targetIPs []string
	if ip := device.ConnectionInfo["ip"]; ip != "" {
		targetIPs = append(targetIPs, ip)
	}

	return &BuiltApp{
		ProviderKey: p.Key(),
		Device:      device,
		AppName:     product,
		Context:     &microWendyBuildContext{WASMPath: wasmPath},
	}, nil
}

func (p *MicroWendyProvider) Run(ctx context.Context, app *BuiltApp, detach bool, output chan<- RunOutput) error {
	defer close(output)

	bc, ok := app.Context.(*microWendyBuildContext)
	if !ok {
		return fmt.Errorf("wendy-lite provider: invalid build context")
	}

	ip := app.Device.ConnectionInfo["ip"]
	port := app.Device.ConnectionInfo["port"]
	if ip == "" || port == "" {
		return fmt.Errorf("wendy-lite provider: missing device address in connection info")
	}

	client := liteclient.NewWendyLiteClient(net.JoinHostPort(ip, port))
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect to device: %w", err)
	}
	defer client.Close()

	output <- RunOutput{Type: RunOutputStarted}

	if err := client.StopApp(); err != nil {
		output <- RunOutput{Type: RunOutputStdout, Data: []byte(fmt.Sprintf("warning: app stop: %v\n", err))}
	}

	if detach {
		if err := client.PushApp(bc.WASMPath, nil); err != nil {
			return fmt.Errorf("push app: %w", err)
		}
	} else {
		pushProg := tui.NewProgress("Pushing app...")
		pp := tea.NewProgram(pushProg)
		go func() {
			pushErr := client.PushApp(bc.WASMPath, func(written, total uint32) {
				var pct float64
				if total > 0 {
					pct = float64(written) / float64(total)
				}
				pp.Send(tui.ProgressUpdateMsg{
					Percent: pct,
					Written: int64(written),
					Total:   int64(total),
				})
			})
			pp.Send(tui.ProgressDoneMsg{Err: pushErr})
		}()
		finalModel, err := pp.Run()
		if err != nil {
			return fmt.Errorf("progress TUI: %w", err)
		}
		if finalModel.(tui.ProgressModel).Err() != nil {
			return fmt.Errorf("push app: %w", finalModel.(tui.ProgressModel).Err())
		}
	}

	output <- RunOutput{Type: RunOutputStdout, Data: []byte("Starting app...\n")}
	if err := client.StartApp(); err != nil {
		return fmt.Errorf("app start: %w", err)
	}

	output <- RunOutput{Type: RunOutputStdout, Data: []byte("App started.\n")}
	return nil
}

func (p *MicroWendyProvider) Stop(_ context.Context, app *BuiltApp) error {
	return nil
}
