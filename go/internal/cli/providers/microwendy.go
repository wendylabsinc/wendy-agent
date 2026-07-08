package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/espidftoolchain"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/swifttoolchain"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

const (
	// microWendyServiceType is the mDNS service type advertised by ESP32 Wendy devices.
	microWendyServiceType = "_wendy-lite._tcp"

	// Currently supported SDK for WASI on Wendy Lite.
	// This also works for older projects that expected to build for wasm32-unknown-none-wasm
	microWendyEmbeddedSDK = "-embedded"
	microWendySwiftTarget = "wasm32-unknown-wasip1"
)

// microWendyBuildContext is stored in BuiltApp.Context.
type microWendyBuildContext struct {
	AppPath string
	AppType liteclient.AppType
}

// MicroWendyProvider builds Swift packages to WASM and serves them to ESP32 devices.
type MicroWendyProvider struct{}

func (p *MicroWendyProvider) Key() string         { return "wendy-lite" }
func (p *MicroWendyProvider) DisplayName() string { return "Wendy Lite" }

func (p *MicroWendyProvider) IsAvailable(ctx context.Context) bool {
	return true
}

func (p *MicroWendyProvider) CheckRequirements(ctx context.Context) error {
	return nil
}

func (p *MicroWendyProvider) DiscoverDevices(ctx context.Context) ([]models.ExternalDevice, error) {
	sd := discovery.GetSerialDiscovery()
	sd.StartScan(0)

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
				"type":     "LAN",
				"name":     svc.TXTRecords["name"],
				"hostname": svc.Hostname,
				"ip":       svc.IPAddress,
				"port":     fmt.Sprintf("%d", svc.Port),
				"mtls":     fmt.Sprintf("%t", svc.TXTRecords["mtls"] == "true"),
			},
			IsWendyDevice: true,
		})
	}

	for _, dev := range sd.Devices() {
		devices = append(devices, models.ExternalDevice{
			ID:          fmt.Sprintf("wendy-lite:%s", dev.Port),
			DisplayName: dev.DisplayName,
			ProviderKey: p.Key(),
			ConnectionInfo: map[string]string{
				"type":       "USB",
				"name":       dev.Name,
				"serialPort": dev.Port,
			},
			IsWendyDevice: true,
		})
	}

	return devices, nil
}

func (p *MicroWendyProvider) SupportedBuildTypes() []string {
	return []string{"swift", "esp-idf"}
}

func (p *MicroWendyProvider) CanBuild(projectPath string) bool {
	_, err := os.Stat(filepath.Join(projectPath, "Package.swift"))
	return err == nil
}

func (p *MicroWendyProvider) Build(ctx context.Context, device models.ExternalDevice, projectPath, projectType, product string, debug bool) (*BuiltApp, error) {
	switch projectType {
	case "swift":
		return p.buildSwift(ctx, device, projectPath, product, debug)
	case "esp-idf":
		return p.buildEspIdf(ctx, device, projectPath, product)
	default:
		return nil, fmt.Errorf("wendy-lite provider: unsupported project type %q", projectType)
	}
}

// buildSwift compiles a Swift package to WASM for the embedded WASI target.
func (p *MicroWendyProvider) buildSwift(ctx context.Context, device models.ExternalDevice, projectPath, product string, debug bool) (*BuiltApp, error) {
	swiftlyTestCmd := exec.CommandContext(ctx, "swiftly", "--version")
	if swiftlyTestCmd.Run() != nil {
		return nil, fmt.Errorf("swiftly is not installed or not in PATH")
	}

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

	return &BuiltApp{
		ProviderKey: p.Key(),
		Device:      device,
		AppName:     product,
		Context:     &microWendyBuildContext{AppPath: wasmPath, AppType: liteclient.AppTypeWasm},
	}, nil
}

// boardToTarget returns the IDF target (SoC name, e.g. "esp32c6") firmware
// must be built for to run on the given board. A board and a target are
// different concepts: the device reports "esp32c6" meaning "generic esp32c6
// board", not the SoC name — they merely coincide for the boards supported
// today, so the mapping is the identity for now. A real lookup goes here once
// board names diverge from SoC names.
func boardToTarget(board string) string {
	return board
}

// buildEspIdf builds an ESP-IDF project with idf.py (via eim) and picks up
// the firmware binary from the project's build folder. The binary is named
// after the CMake project() name, which may differ from the app ID.
func (p *MicroWendyProvider) buildEspIdf(ctx context.Context, device models.ExternalDevice, projectPath, product string) (*BuiltApp, error) {
	// get device info
	di, err := p.GetDeviceInfo(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("getting device info: %w", err)
	}

	// check device capability
	if !di.NativeAppSupport {
		return nil, fmt.Errorf("device %s does not support native apps", device.DisplayName)
	}

	// ensures the ESP-IDF toolchain is available, install it if not
	if err := espidftoolchain.EnsureVersion(ctx); err != nil {
		return nil, err
	}

	// check if the project has been configured for the right target
	target := boardToTarget(di.DeviceType)
	configuredTarget := espidftoolchain.ProjectTarget(projectPath)
	if target != "" && target != configuredTarget {
		cmd := espidftoolchain.IdfCommandContext(ctx, "set-target", target)
		cmd.Dir = projectPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("idf.py set-target %s: %w", target, err)
		}
	}

	// build the project
	cmd := espidftoolchain.IdfCommandContext(ctx, "build")
	cmd.Dir = projectPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("idf.py build: %w", err)
	}

	// verify the presence of the output bin file
	binName := espidftoolchain.ProjectName(projectPath)
	if binName == "" {
		binName = product
	}
	binPath := filepath.Join(projectPath, "build", binName+".bin")
	if _, err := os.Stat(binPath); err != nil {
		return nil, fmt.Errorf("expected ESP-IDF app binary at %s: %w", binPath, err)
	}

	return &BuiltApp{
		ProviderKey: p.Key(),
		Device:      device,
		AppName:     product,
		Context:     &microWendyBuildContext{AppPath: binPath, AppType: liteclient.AppTypeNative},
	}, nil
}

func (p *MicroWendyProvider) Run(ctx context.Context, app *BuiltApp, detach bool, output chan<- RunOutput) error {
	defer close(output)

	bc, ok := app.Context.(*microWendyBuildContext)
	if !ok {
		return fmt.Errorf("wendy-lite provider: invalid build context")
	}

	client, err := p.connectClient(app.Device)
	if err != nil {
		return err
	}
	defer client.Close()

	if bc.AppType == liteclient.AppTypeWasm {
		if err := client.StopApp(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: app stop: %v\n", err)
		}
	}

	if detach {
		if err := client.PushApp(bc.AppPath, bc.AppType, nil); err != nil {
			return fmt.Errorf("push app: %w", err)
		}
	} else {
		fmt.Println()
		pushProg := tui.NewProgress("Pushing app...")
		pp := tui.NewProgressProgram(pushProg)
		go func() {
			pushErr := client.PushApp(bc.AppPath, bc.AppType, func(written, total uint32) {
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

	fmt.Println()
	switch bc.AppType {
	case liteclient.AppTypeNative:
		// A native app is a full firmware image; the device boots into it on
		// reset rather than via AppStart. The deferred Close runs on return.
		fmt.Println("Rebooting device...")
		if err := client.ResetTargetDevice(); err != nil {
			return fmt.Errorf("device reset: %w", err)
		}
	default:
		fmt.Println("Starting app...")
		if err := client.StartApp(); err != nil {
			return fmt.Errorf("app start: %w", err)
		}
	}

	output <- RunOutput{Type: RunOutputStarted}

	return nil
}

func (p *MicroWendyProvider) Stop(_ context.Context, app *BuiltApp) error {
	return nil
}

func (p *MicroWendyProvider) GetDeviceInfo(ctx context.Context, device models.ExternalDevice) (*ProviderDeviceInfo, error) {
	client, err := p.connectClient(device)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	di, err := client.GetDeviceInfo(3 * time.Second)
	if err != nil {
		return nil, err
	}
	return &ProviderDeviceInfo{
		// On wendy-lite, OS version and agent version are the same.
		AgentVersion:     di.OSVersion,
		OS:               di.OS,
		OSVersion:        di.OSVersion,
		CPUArchitecture:  di.CPUArchitecture,
		DeviceType:       di.Board,
		WasmAppSupport:   di.WasmAppSupport,
		NativeAppSupport: di.NativeAppSupport,
	}, nil
}

// connectClient opens a WendyLiteClient connection to the device over serial
// or LAN (with mTLS when advertised). The caller must Close the client.
func (p *MicroWendyProvider) connectClient(device models.ExternalDevice) (*liteclient.WendyLiteClient, error) {
	client := liteclient.NewWendyLiteClient()
	if device.ConnectionInfo["type"] == "USB" {
		serialPort := device.ConnectionInfo["serialPort"]
		if serialPort == "" {
			return nil, fmt.Errorf("wendy-lite provider: missing serial port in connection info")
		}
		if err := client.ConnectToSerial(serialPort); err != nil {
			return nil, fmt.Errorf("connect to device via serial: %w", err)
		}
	} else if device.ConnectionInfo["type"] == "LAN" {
		ip := device.ConnectionInfo["ip"]
		port := device.ConnectionInfo["port"]
		if ip == "" || port == "" {
			return nil, fmt.Errorf("wendy-lite provider: missing IP or port in connection info")
		}
		addr := net.JoinHostPort(ip, port)
		if device.ConnectionInfo["mtls"] == "true" {
			certInfos, err := loadAllCLICerts()
			if err != nil {
				return nil, fmt.Errorf("wendy-lite provider: loading mTLS certs: %w", err)
			}
			var connectErrs []error
			connected := false
			for _, certInfo := range certInfos {
				cert, err := tls.X509KeyPair([]byte(certInfo.PemCertificate), []byte(certInfo.PemPrivateKey))
				if err != nil {
					return nil, fmt.Errorf("wendy-lite provider: parsing mTLS cert: %w", err)
				}
				rootCAs := x509.NewCertPool()
				if certInfo.PemCertificateChain != "" {
					rootCAs.AppendCertsFromPEM([]byte(certInfo.PemCertificateChain))
				}
				if err := client.ConnectWithMutualAuthentication(addr, cert, *rootCAs); err != nil {
					connectErrs = append(connectErrs, err)
				} else {
					connected = true
					break
				}
			}
			if !connected {
				var b strings.Builder
				fmt.Fprintf(&b, "Wendy Lite connection error")
				for i, e := range connectErrs {
					if i == 0 {
						fmt.Fprintf(&b, ": identity %d: %v", i+1, e)
					} else {
						fmt.Fprintf(&b, "; identity %d: %v", i+1, e)
					}
				}
				return nil, errors.New(b.String())
			}
		} else {
			if err := client.ConnectInsecure(addr); err != nil {
				return nil, fmt.Errorf("connect to device: %w", err)
			}
		}
	} else {
		return nil, fmt.Errorf("wendy-lite provider: unsupported connection type: %s", device.ConnectionInfo["type"])
	}
	return client, nil
}

func loadAllCLICerts() ([]config.CertificateInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var out []config.CertificateInfo
	for _, auth := range cfg.Auth {
		out = append(out, auth.Certificates...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("not logged in (no certificate found)")
	}
	return out, nil
}
