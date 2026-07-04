package commands

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func newDeviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Manage WendyOS devices",
	}

	cmd.AddGroup(
		&cobra.Group{ID: "common", Title: "Common Commands:"},
		&cobra.Group{ID: "manage", Title: "Device Management:"},
		&cobra.Group{ID: "hardware", Title: "Hardware:"},
	)

	addToGroup := func(groupID string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = groupID
			cmd.AddCommand(c)
		}
	}

	// Common Commands: the subcommands used in everyday workflows, surfaced at
	// the top in rough order of usefulness.
	addToGroup("common",
		newAppsCmd(),
		newDeviceLogsCmd(),
		newDeviceCrashesCmd(),
		newROS2Cmd(),
		newDeviceDashboardCmd(),
		newTopCmd(),
	)
	addToGroup("manage",
		newDeviceInfoCmd(),
		newDeprecatedDeviceVersionCmd(),
		newDeviceSetDefaultCmd(),
		newDeviceGetDefaultCmd(),
		newDeviceUnsetDefaultCmd(),
		newDeviceSetupCmd(),
		newDeviceEnrollCmd(),
		newDeviceUnenrollCmd(),
		newDeviceUpdateCmd(),
		newDeviceSyncTimeCmd(),
		newVolumesCmd(),
	)
	addToGroup("hardware",
		newWifiCmd(),
		newBluetoothCmd(),
		newAudioCmd(),
		newCameraCmd(),
		newHardwareCmd(),
	)
	// Hidden commands stay registered (and runnable) but are kept off the help
	// menu; they are hidden via their own constructors.
	addToGroup("manage",
		newDeviceTelemetryStreamCmd(),
		newPsCmd(),
	)

	return cmd
}

func newDeviceInfoCmd() *cobra.Command {
	return newDeviceInfoLikeCmd("info", false)
}

func newDeprecatedDeviceVersionCmd() *cobra.Command {
	return newDeviceInfoLikeCmd("version", true)
}

func newDeviceInfoLikeCmd(use string, deprecated bool) *cobra.Command {
	var checkUpdates bool
	var prerelease bool

	cmd := &cobra.Command{
		Use:    use,
		Short:  "Show agent version, OS, architecture, GPU, and hardware info for the target device",
		Hidden: deprecated,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if deprecated && !jsonOutput {
				if _, ok := cloudDeviceConfigFromContext(ctx); ok {
					cmd.PrintErrln("Warning: 'wendy cloud device version' is deprecated; use 'wendy cloud device info' instead.")
				} else {
					cmd.PrintErrln("Warning: 'wendy device version' is deprecated; use 'wendy device info' instead.")
				}
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()

			var agentVersion, osName, osVersion, cpuArch, deviceType, storageMedium, gpuVendor, jetpackVersion, cudaVersion, gpuArch string
			var diskUsedBytes, diskTotalBytes *int64
			var partitions []*agentpb.DiskPartition
			var hasGPU bool

			if target.Bluetooth != nil && target.Bluetooth.IsWendyAgent() {
				cliLogln("Connecting to %s via Bluetooth...", tui.Device(target.Bluetooth.DisplayName))
				bleClient, bleErr := connectBLEAgent(target.Bluetooth)
				if bleErr != nil {
					return bleErr
				}
				defer bleClient.Close()
				bleResp, bleErr := bleClient.AgentVersion()
				if bleErr != nil {
					return fmt.Errorf("getting agent version: %w", bleErr)
				}
				agentVersion = bleResp.GetVersion()
				osName = bleResp.GetOs()
				osVersion = bleResp.GetOsVersion()
				cpuArch = bleResp.GetCpuArchitecture()
			} else if target.Agent != nil {
				resp, respErr := target.Agent.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if respErr != nil {
					return fmt.Errorf("getting agent version: %w", respErr)
				}
				agentVersion = resp.GetVersion()
				osName = resp.GetOs()
				osVersion = resp.GetOsVersion()
				cpuArch = resp.GetCpuArchitecture()
				deviceType = resp.GetDeviceType()
				storageMedium = resp.GetStorageMedium()
				hasGPU = resp.GetHasGpu()
				gpuVendor = resp.GetGpuVendor()
				jetpackVersion = resp.GetJetpackVersion()
				cudaVersion = resp.GetCudaVersion()
				gpuArch = resp.GetGpuArch()
				diskUsedBytes = resp.DiskUsedBytes
				diskTotalBytes = resp.DiskTotalBytes
				partitions = resp.GetPartitions()
			} else {
				return fmt.Errorf("selected device does not support this command")
			}

			var latestVersion string
			if checkUpdates {
				release, err := fetchAgentRelease(prerelease)
				if err != nil {
					return fmt.Errorf("checking for updates: %w", err)
				}
				latestVersion = release.TagName
			}

			if jsonOutput {
				out := map[string]any{
					"version":         agentVersion,
					"os":              osName,
					"osVersion":       osVersion,
					"cpuArchitecture": cpuArch,
					"deviceType":      deviceType,
					"cliVersion":      version.Version,
					"hasGpu":          hasGPU,
				}
				if storageMedium != "" {
					out["storageMedium"] = storageMedium
				}
				if diskUsedBytes != nil && diskTotalBytes != nil {
					out["diskUsedBytes"] = *diskUsedBytes
					out["diskTotalBytes"] = *diskTotalBytes
				}
				if len(partitions) > 0 {
					parts := make([]map[string]any, len(partitions))
					for i, p := range partitions {
						parts[i] = map[string]any{
							"mountpoint": p.GetMountpoint(),
							"filesystem": p.GetFilesystem(),
							"device":     p.GetDevice(),
							"usedBytes":  p.GetUsedBytes(),
							"totalBytes": p.GetTotalBytes(),
						}
					}
					out["partitions"] = parts
				}
				if gpuVendor != "" {
					out["gpuVendor"] = gpuVendor
				}
				if jetpackVersion != "" {
					out["jetpackVersion"] = jetpackVersion
				}
				if cudaVersion != "" {
					out["cudaVersion"] = cudaVersion
				}
				if gpuArch != "" {
					out["gpuArch"] = gpuArch
				}
				if checkUpdates {
					out["latestVersion"] = latestVersion
					out["updateAvailable"] = version.CompareVersions(latestVersion, agentVersion) > 0
				}
				data, err := json.MarshalIndent(out, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Printf("%s %s\n", tui.Dim("Agent Version:"), tui.Value(agentVersion))
			fmt.Printf("%s %s\n", tui.Dim("OS:"), tui.Value(osName+" "+osVersion))
			fmt.Printf("%s %s\n", tui.Dim("Architecture:"), tui.Value(cpuArch))
			if deviceType != "" {
				fmt.Printf("%s %s\n", tui.Dim("Device Type:"), tui.Value(deviceType))
			}
			if storageMedium != "" {
				fmt.Printf("%s %s\n", tui.Dim("Storage:"), tui.Value(storageMedium))
			}
			if len(partitions) > 0 {
				fmt.Print(formatPartitionTable(partitions))
			} else if diskUsedBytes != nil && diskTotalBytes != nil {
				fmt.Printf("%s %s\n", tui.Dim("Disk Usage:"), tui.Value(formatDiskUsage(*diskUsedBytes, *diskTotalBytes)))
			}
			if hasGPU {
				vendor := gpuVendor
				if vendor == "" {
					vendor = "unknown"
				}
				fmt.Printf("%s %s\n", tui.Dim("GPU:"), tui.Value(vendor))
				if jetpackVersion != "" {
					fmt.Printf("%s %s\n", tui.Dim("JetPack:"), tui.Value(jetpackVersion))
				}
				if cudaVersion != "" {
					fmt.Printf("%s %s\n", tui.Dim("CUDA:"), tui.Value(cudaVersion))
				}
				if gpuArch != "" {
					fmt.Printf("%s %s\n", tui.Dim("GPU Arch:"), tui.Value(gpuArch))
				}
			}
			fmt.Printf("%s %s\n", tui.Dim("CLI Version:"), tui.Value(version.Version))

			if cmp := version.CompareVersions(version.Version, agentVersion); cmp > 0 && agentVersion != "dev" {
				fmt.Println()
				fmt.Println(tui.WarningMessage("Agent is behind the CLI — run 'wendy device update' to update."))
			} else if cmp < 0 {
				fmt.Println()
				fmt.Println(tui.WarningMessage("CLI is behind the agent — consider updating the CLI."))
			}

			if checkUpdates {
				if version.CompareVersions(latestVersion, agentVersion) > 0 {
					fmt.Println()
					fmt.Printf("%s %s %s %s\n",
						tui.WarningMessage("Update available:"),
						tui.Value(latestVersion),
						tui.Dim("(you have"),
						tui.Value(agentVersion)+tui.Dim(")"),
					)
					fmt.Printf("%s %s\n", tui.Dim("Update with:"), tui.Command("wendy device update"))
				} else {
					fmt.Println("\nAgent is up to date.")
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&checkUpdates, "check-updates", false, "Check for available agent updates on GitHub")
	cmd.Flags().BoolVar(&prerelease, "prerelease", false, "Include prerelease (nightly) builds when checking for updates")

	return cmd
}

func newDeviceSetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-default [hostname]",
		Short: "Set the default device hostname",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var device string
			if len(args) > 0 {
				device = args[0]
			} else {
				sel, err := pickDeviceForDefault(cmd.Context())
				if err != nil {
					return err
				}
				device = sel
			}

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			cfg.DefaultDevice = device
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Printf("Default device set to: %s\n", tui.Device(device))

			// WDY-1149: pin the device's (organisation, cloud host) identity now
			// if it is reachable, so later connections detect a swapped device or
			// MITM. Best-effort and non-interactive: an offline device is pinned
			// instead on its first successful connection. The pin itself is
			// established inside connectToAgent's default-device path.
			if conn, connErr := connectToAgent(cmd.Context(), SuppressProvisioningHint(), SuppressUpdateCheck(), NonInteractive()); connErr == nil {
				_ = conn.Close()
			}
			return nil
		},
	}
}

func newDeviceGetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "get-default",
		Short:  "Show the current default device",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			if jsonOutput {
				data, err := json.MarshalIndent(map[string]string{"defaultDevice": cfg.DefaultDevice}, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}

			if cfg.DefaultDevice == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No default device set. Set one with 'wendy device set-default'.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Default device: %s\n", cfg.DefaultDevice)
			return nil
		},
	}
}

// pickDeviceForDefault runs the interactive device picker and returns a
// hostname or provider key suitable for storing as the default device.
func pickDeviceForDefault(ctx context.Context) (string, error) {
	selected, err := pickDevice(ctx, nil, false, false)
	if err != nil {
		return "", err
	}
	defer selected.Close()

	if selected.Agent != nil {
		return selected.Agent.Host, nil
	}
	if selected.External != nil {
		return selected.External.ProviderKey, nil
	}
	return "", fmt.Errorf("no device selected")
}

func newDeviceUnsetDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset-default",
		Short: "Clear the default device",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			cfg.DefaultDevice = ""
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Println("Default device cleared.")
			return nil
		},
	}
}

func newDeviceSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "setup",
		Short:  "Interactive device setup: enroll, name, and configure WiFi",
		Long:   "Walks through enrollment (with device naming) and WiFi configuration for a new device.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			reader := bufio.NewReader(os.Stdin)

			// Step 1: Enroll (and name) the device.
			provResp, err := conn.ProvisioningService.IsProvisioned(ctx, &agentpb.IsProvisionedRequest{})
			if err != nil {
				return fmt.Errorf("checking enrollment status: %w", err)
			}

			if provResp.GetProvisioned() != nil {
				prov := provResp.GetProvisioned()
				fmt.Printf("Device is already enrolled (org: %d, asset: %d, cloud: %s).\n",
					prov.GetOrganizationId(), prov.GetAssetId(), prov.GetCloudHost())
			} else {
				fmt.Println("Device is not enrolled.")
				if loadCLICert() == nil {
					fmt.Println("You are not logged in to Wendy Cloud.")
					fmt.Print("Log in now? [Y/n] ")
					answer, _ := reader.ReadString('\n')
					answer = strings.TrimSpace(strings.ToLower(answer))
					if answer == "" || answer == "y" || answer == "yes" {
						if loginErr := performLogin(ctx, defaultCloudDashboard, defaultCloudGRPC); loginErr != nil {
							return fmt.Errorf("login failed: %w", loginErr)
						}
					}
				}

				if auth := loadCLIAuth(); auth != nil {
					// Collect the device name before enrolling (name cannot be changed after).
					fmt.Print("Device name: ")
					line, _ := reader.ReadString('\n')
					deviceName := strings.TrimSpace(line)
					if deviceName == "" {
						return fmt.Errorf("device name is required")
					}
					if enrollErr := runEnrollDevice(ctx, conn, auth, deviceName); enrollErr != nil {
						fmt.Printf("Enrollment failed: %v\n", enrollErr)
					}
				}
				fmt.Println()
			}

			// Step 2: WiFi setup.
			target := &SelectedDevice{Agent: conn}
			ssid, pickErr := pickWifiNetwork(ctx, target)
			if pickErr != nil {
				if errors.Is(pickErr, ErrUserCancelled) {
					fmt.Println("WiFi setup skipped.")
				} else {
					fmt.Printf("WiFi scan failed: %v\n", pickErr)
				}
			} else {
				fmt.Print("Password (leave empty for open networks): ")
				passwordBytes, readErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if readErr != nil {
					fmt.Printf("Failed to read password: %v\n", readErr)
				} else {
					password := strings.TrimSpace(string(passwordBytes))
					fmt.Printf("Connecting to %s...\n", ssid)
					wifiConnResp, connectErr := conn.AgentService.ConnectToWiFi(ctx, &agentpb.ConnectToWiFiRequest{
						Ssid:     ssid,
						Password: password,
					})
					if connectErr != nil {
						fmt.Printf("Failed to connect to WiFi: %v\n", connectErr)
					} else if !wifiConnResp.GetSuccess() {
						fmt.Printf("Failed to connect: %s\n", wifiConnResp.GetErrorMessage())
					} else {
						fmt.Printf("Connected to %s.\n", ssid)
					}
				}
			}

			// Step 3: Check agent version.
			fmt.Println()
			versionResp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
			if err != nil {
				fmt.Printf("Unable to check agent version: %v\n", err)
			} else {
				fmt.Printf("Agent version: %s\n", versionResp.GetVersion())
				if cmp := version.CompareVersions(version.Version, versionResp.GetVersion()); cmp > 0 && versionResp.GetVersion() != "dev" {
					fmt.Println("Agent is behind the CLI — consider running 'wendy device update'.")
				}
			}

			fmt.Println("\nSetup complete.")
			return nil
		},
	}
}

func newDeviceEnrollCmd() *cobra.Command {
	var name string
	var cloudGRPC string

	cmd := &cobra.Command{
		Use:    "enroll",
		Short:  "Enroll this device with Wendy Cloud or a local pki-core",
		Long:   "Creates an enrollment token using your stored auth session and provisions the connected device with mTLS certificates. Run 'wendy cloud login' first.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			promptWifiIfNeeded(ctx, conn)

			auth, err := pickAuthEntry(cloudGRPC)
			if err != nil {
				return err
			}

			return runEnrollDevice(ctx, conn, auth, name)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Device name")
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud/pki-core gRPC endpoint to use (optional when a default session is set via 'wendy auth use')")
	return cmd
}

// promptWifiIfNeeded checks whether the device is connected to WiFi, and if
// not, offers an interactive flow to connect before enrollment. Errors from the
// status check are silently ignored so the function degrades gracefully on
// devices that don't support WiFi (e.g. local, docker).
func promptWifiIfNeeded(ctx context.Context, conn *grpcclient.AgentConnection) {
	if !isInteractiveTerminal() {
		return
	}

	statusResp, err := conn.AgentService.GetWiFiStatus(ctx, &agentpb.GetWiFiStatusRequest{})
	if err != nil || statusResp.GetConnected() {
		return
	}

	fmt.Println("No WiFi connection detected on the device.")
	fmt.Print("Set up WiFi before enrolling? [Y/n] ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	if answer != "" && answer != "y" && answer != "yes" {
		return
	}

	target := &SelectedDevice{Agent: conn}
	ssid, pickErr := pickWifiNetwork(ctx, target)
	if pickErr != nil {
		if !errors.Is(pickErr, ErrUserCancelled) {
			fmt.Printf("WiFi setup failed: %v\n", pickErr)
		}
		return
	}

	fmt.Print("Password (leave empty for open networks): ")
	passwordBytes, readErr := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if readErr != nil {
		fmt.Printf("Failed to read password: %v\n", readErr)
		return
	}
	password := strings.TrimSpace(string(passwordBytes))

	fmt.Printf("Connecting to %s...\n", ssid)
	wifiResp, connectErr := conn.AgentService.ConnectToWiFi(ctx, &agentpb.ConnectToWiFiRequest{
		Ssid:     ssid,
		Password: password,
	})
	if connectErr != nil {
		fmt.Printf("WiFi connection failed: %v\n", connectErr)
	} else if !wifiResp.GetSuccess() {
		fmt.Printf("WiFi connection failed: %s\n", wifiResp.GetErrorMessage())
	} else {
		fmt.Printf("Connected to %s.\n", ssid)
	}
}

// defaultEnrollmentName derives a device name from the connected host,
// stripping a .local suffix. Returns "" for bare IP addresses (no usable name).
func defaultEnrollmentName(host string) string {
	h := strings.TrimSpace(host)
	if h == "" || net.ParseIP(h) != nil {
		return ""
	}
	return strings.TrimSuffix(h, ".local")
}

func runEnrollDevice(ctx context.Context, conn *grpcclient.AgentConnection, auth *config.AuthConfig, name string) error {
	if len(auth.Certificates) == 0 {
		return fmt.Errorf("selected auth entry has no certificates; re-run 'wendy auth login'")
	}

	if name == "" {
		defaultName := defaultEnrollmentName(conn.Host)
		if !isInteractiveTerminal() {
			if defaultName != "" {
				name = defaultName
			} else {
				return fmt.Errorf("device name is required; pass --name when not running interactively")
			}
		} else {
			prompt := "Device name"
			if defaultName != "" {
				prompt = fmt.Sprintf("Device name [%s]", defaultName)
			}
			fmt.Printf("%s: ", prompt)
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			name = strings.TrimSpace(line)
			if name == "" {
				name = defaultName
			}
			if name == "" {
				return fmt.Errorf("device name is required")
			}
		}
	}

	if auth == nil || len(auth.Certificates) == 0 {
		return fmt.Errorf("missing authentication certificate in selected auth entry")
	}
	cert := auth.Certificates[0]

	var cloudTransport grpc.DialOption
	if strings.HasSuffix(auth.CloudGRPC, ":443") {
		tlsCfg, err := certs.LoadTLSConfig(
			cert.PemCertificate,
			cert.PemCertificateChain,
			cert.PemPrivateKey,
			"",
		)
		if err != nil {
			return fmt.Errorf("loading TLS config: %w", err)
		}
		cloudTransport = grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
	} else {
		cloudTransport = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	cloudConn, err := grpc.NewClient(auth.CloudGRPC, cloudTransport)
	if err != nil {
		return fmt.Errorf("connecting to cloud: %w", err)
	}
	defer cloudConn.Close()

	tokenCtx := cloudContext(ctx, auth)

	certClient := cloudpb.NewCertificateServiceClient(cloudConn)
	tokenResp, err := certClient.CreateAssetEnrollmentToken(tokenCtx, &cloudpb.CreateAssetEnrollmentTokenRequest{
		OrganizationId: int32(cert.OrganizationID),
		Name:           name,
		TtlSeconds:     600,
	})
	if err != nil {
		return fmt.Errorf("creating enrollment token: %w", err)
	}

	fmt.Println("Enrolling device...")
	_, err = conn.ProvisioningService.StartProvisioning(ctx, &agentpb.StartProvisioningRequest{
		OrganizationId:  tokenResp.GetOrganizationId(),
		AssetId:         tokenResp.GetAssetId(),
		EnrollmentToken: tokenResp.GetEnrollmentToken(),
		CloudHost:       auth.CloudGRPC,
	})
	if err != nil {
		return fmt.Errorf("enrolling device: %w", err)
	}

	fmt.Printf("Device enrolled (org: %d, asset: %d).\n",
		tokenResp.GetOrganizationId(), tokenResp.GetAssetId())
	return nil
}

func pickAuthEntry(cloudGRPC string) (*config.AuthConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	// A default that points at a removed session is treated as unset; warn so
	// the user understands why the picker appeared instead of auto-selecting.
	if cloudGRPC == "" && cfg.DefaultCloudGRPC != "" {
		if _, ok := cfg.DefaultAuth(); !ok {
			fmt.Fprintf(os.Stderr, "warning: default session %s no longer exists; clear it with 'wendy auth default --clear'\n", cfg.DefaultCloudGRPC)
		}
	}
	var pick config.SessionPicker
	if isInteractiveTerminal() {
		pick = pickAuthSessionFn
	}
	return config.ResolveAuth(cfg, cloudGRPC, pick)
}

func newDeviceUnenrollCmd() *cobra.Command {
	var assumeYes bool
	var cloudGRPC string

	cmd := &cobra.Command{
		Use:   "unenroll",
		Short: "Unenroll a device and remove it from Wendy Cloud",
		Long: "Reverses 'wendy device enroll': deletes the device's enrollment certificates and " +
			"provisioning state (the agent restarts into unprovisioned mode), then revokes the " +
			"device's certificates and deletes its asset record in Wendy Cloud.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			conn, err := connectToAgent(ctx, SuppressProvisioningHint())
			if err != nil {
				return err
			}
			defer conn.Close()

			// Determine the device's current enrollment so we know which cloud
			// asset to clean up afterwards.
			provResp, err := conn.ProvisioningService.IsProvisioned(ctx, &agentpb.IsProvisionedRequest{})
			if err != nil {
				return fmt.Errorf("checking provisioning status: %w", err)
			}
			prov := provResp.GetProvisioned()
			if prov == nil {
				return fmt.Errorf("device is not provisioned")
			}
			cloudHost := prov.GetCloudHost()
			orgID := prov.GetOrganizationId()
			assetID := prov.GetAssetId()

			if !assumeYes {
				if !isInteractiveTerminal() {
					return fmt.Errorf("unenroll is destructive; pass --yes to confirm when not running interactively")
				}
				fmt.Printf("This will unenroll the device (org: %d, asset: %d) and delete its asset in Wendy Cloud.\n", orgID, assetID)
				fmt.Print("Continue? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				answer := strings.TrimSpace(strings.ToLower(line))
				if answer != "y" && answer != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// Step 1: reset the device. The agent deletes its state and restarts,
			// so the connection may drop right after the response — tolerate that.
			if _, err := conn.ProvisioningService.Unprovision(ctx, &agentpb.UnprovisionRequest{}); err != nil {
				if status.Code(err) == codes.Unavailable {
					cliLogln("Device connection closed (agent is restarting).")
				} else {
					return fmt.Errorf("unprovisioning device: %w", err)
				}
			}

			// Step 2: clean up the cloud asset. Best-effort — a failure here leaves
			// a dangling asset that can be removed from the dashboard, but the
			// device itself is already reset.
			certsRevoked, assetDeleted, cloudErr := cloudUnenrollCleanup(ctx, cloudGRPC, cloudHost, assetID)

			if jsonOutput {
				out := map[string]any{
					"deviceReset":  true,
					"certsRevoked": certsRevoked,
					"assetDeleted": assetDeleted,
				}
				if cloudErr != nil {
					out["cloudError"] = cloudErr.Error()
				}
				data, marshalErr := json.MarshalIndent(out, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				fmt.Println(string(data))
			} else {
				fmt.Println("Device reset to unprovisioned state.")
				if cloudErr != nil {
					fmt.Printf("Warning: cloud cleanup failed: %v\n", cloudErr)
					fmt.Printf("Delete asset %d from the Wendy Cloud dashboard to finish.\n", assetID)
				} else {
					fmt.Printf("Revoked %d certificate(s) and deleted asset %d from Wendy Cloud.\n", certsRevoked, assetID)
				}
			}

			if cloudErr != nil {
				return cloudErr
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&assumeYes, "yes", false, "Skip the confirmation prompt")
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint to use for cleanup (defaults to the device's enrolled cloud host)")
	return cmd
}

// cloudUnenrollCleanup revokes the asset's active certificates and then
// deletes the asset record in Wendy Cloud. It authenticates with the user's
// stored session for the device's cloud host (or cloudGRPC if provided).
func cloudUnenrollCleanup(ctx context.Context, cloudGRPC, deviceCloudHost string, assetID int32) (certsRevoked int, assetDeleted bool, err error) {
	target := cloudGRPC
	if target == "" {
		target = deviceCloudHost
	}
	auth, err := pickAuthEntry(target)
	if err != nil {
		return 0, false, fmt.Errorf("selecting cloud auth session: %w", err)
	}
	if len(auth.Certificates) == 0 {
		return 0, false, fmt.Errorf("auth session has no certificates; re-run 'wendy auth login'")
	}
	cert := auth.Certificates[0]

	var transport grpc.DialOption
	if strings.HasSuffix(auth.CloudGRPC, ":443") {
		tlsCfg, tlsErr := certs.LoadTLSConfig(
			cert.PemCertificate,
			cert.PemCertificateChain,
			cert.PemPrivateKey,
			"",
		)
		if tlsErr != nil {
			return 0, false, fmt.Errorf("loading TLS config: %w", tlsErr)
		}
		transport = grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
	} else {
		transport = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	cloudConn, dialErr := grpc.NewClient(auth.CloudGRPC, transport)
	if dialErr != nil {
		return 0, false, fmt.Errorf("connecting to cloud: %w", dialErr)
	}
	defer cloudConn.Close()

	tokenCtx := cloudContext(ctx, auth)

	// Revoke the asset's active certificates first so a stale identity cannot be
	// reused, then delete the asset record.
	certClient := cloudpb.NewCertificateServiceClient(cloudConn)
	stream, listErr := certClient.ListCertificates(tokenCtx, &cloudpb.ListCertificatesRequest{AssetId: assetID})
	if listErr != nil {
		return 0, false, fmt.Errorf("listing certificates: %w", listErr)
	}
	for {
		resp, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return certsRevoked, false, fmt.Errorf("listing certificates: %w", recvErr)
		}
		c := resp.GetCertificate()
		if c == nil || c.GetStatus() != cloudpb.CertificateStatus_CERTIFICATE_STATUS_ACTIVE {
			continue
		}
		if _, revErr := certClient.RevokeCertificate(tokenCtx, &cloudpb.RevokeCertificateRequest{
			CertificateId: c.GetId(),
			Reason:        "device unprovisioned",
		}); revErr != nil {
			return certsRevoked, false, fmt.Errorf("revoking certificate %d: %w", c.GetId(), revErr)
		}
		certsRevoked++
	}

	assetClient := cloudpb.NewAssetServiceClient(cloudConn)
	if _, delErr := assetClient.DeleteAsset(tokenCtx, &cloudpb.DeleteAssetRequest{Id: assetID}); delErr != nil {
		return certsRevoked, false, fmt.Errorf("deleting asset %d: %w", assetID, delErr)
	}
	return certsRevoked, true, nil
}

// scanWiFiNetworks queries the agent for available WiFi networks.
func scanWiFiNetworks(ctx context.Context, conn *grpcclient.AgentConnection) ([]*agentpb.ListWiFiNetworksResponse_WiFiNetwork, error) {
	resp, err := conn.AgentService.ListWiFiNetworks(ctx, &agentpb.ListWiFiNetworksRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing WiFi networks: %w", err)
	}
	return resp.GetNetworks(), nil
}

// parseSeverityLevel converts a severity name (e.g. "trace", "info") to its
// OpenTelemetry severity number. Returns 0 if the name is not recognized.
func parseSeverityLevel(name string) int32 {
	switch strings.ToLower(name) {
	case "trace":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE)
	case "debug":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG)
	case "info":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_INFO)
	case "warn", "warning":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_WARN)
	case "error":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR)
	case "fatal":
		return int32(otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL)
	default:
		return 0
	}
}

func newDeviceLogsCmd() *cobra.Command {
	var appName string
	var serviceName string
	var minSeverity int32
	var level string
	var tail int32
	var since time.Duration
	var previous bool

	cmd := &cobra.Command{
		Use:   "logs [app]",
		Short: "Stream logs from containers on the device",
		Long: "Stream logs from containers on the device.\n\n" +
			"Pass an app name (positionally or with --app) to see only that app's\n" +
			"logs. Without a filter, logs from every container and the agent itself\n" +
			"are streamed, which can include agent lifecycle messages.\n\n" +
			"Use --tail N to replay recent history before going live. --since 5m\n" +
			"replays only history newer than the given age. --previous replays the\n" +
			"crashed/stopped container's last run (the logs up to its most recent\n" +
			"exit) and then stops — useful for a post-mortem of an app that exited.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Accept the app name positionally (e.g. `wendy device logs IronPaws`)
			// as well as via --app. Without this the positional argument was
			// silently ignored, so the command streamed every container's logs
			// instead of the requested app's (see issue #1169).
			if len(args) == 1 {
				if appName != "" && appName != args[0] {
					return fmt.Errorf("conflicting app names: %q (positional) and %q (--app)", args[0], appName)
				}
				appName = args[0]
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			// --level takes precedence over --min-severity when both are set.
			if level != "" {
				if sev := parseSeverityLevel(level); sev > 0 {
					minSeverity = sev
				} else {
					return fmt.Errorf("unknown log level %q (use trace, debug, info, warn, error, or fatal)", level)
				}
			}

			if since < 0 {
				return fmt.Errorf("--since must be a positive duration")
			}

			// --since and --previous both work over replayed history. The agent
			// has no server-side time/run filter, so request a generous replay
			// window and narrow it client-side. Honour an explicit, larger --tail.
			historyOnly := previous
			if (since > 0 || previous) && tail <= 0 {
				tail = maxLogReplay
			}

			req := &agentpb.StreamLogsRequest{}
			if appName != "" {
				req.AppName = &appName
			}
			if serviceName != "" {
				req.ServiceName = &serviceName
			}
			// Default to INFO so dmesg debug/trace output is hidden unless the
			// user explicitly requests a lower level.
			if !cmd.Flags().Changed("level") && !cmd.Flags().Changed("min-severity") {
				infoSev := parseSeverityLevel("info")
				req.MinSeverity = &infoSev
			} else if minSeverity > 0 {
				req.MinSeverity = &minSeverity
			}
			if tail > 0 {
				req.LastN = &tail
			}

			var sinceCutoffNanos uint64
			if since > 0 {
				sinceCutoffNanos = uint64(time.Now().Add(-since).UnixNano())
			}
			stream, err := conn.TelemetryService.StreamLogs(ctx, req)
			if err != nil {
				return fmt.Errorf("starting log stream: %w", err)
			}

			// Tell the user the stream is live and that waiting is expected.
			// Without this the command appears to hang after connecting, with
			// no way to tell streaming from a stuck command (see issue #1169).
			// Printed to stderr (via cliLogln) so it never mixes into piped or
			// --json log output.
			if !jsonOutput {
				target := "the device"
				switch {
				case appName != "":
					target = appName
				case serviceName != "":
					target = serviceName
				}
				switch {
				case previous:
					cliLogln("Showing the previous run of %s (logs up to its last exit). Done when output ends.", target)
				case since > 0:
					cliLogln("Replaying logs from %s newer than %s, then live. Press Ctrl-C to stop.", target, since)
				case tail > 0:
					cliLogln("Streaming logs from %s — replaying up to %d recent, then live. Press Ctrl-C to stop.", target, tail)
				default:
					cliLogln("Streaming logs from %s. Waiting for new logs — press Ctrl-C to stop.", target)
				}
			}

			liveSeparatorPrinted := tail == 0
			seenHistory := false

			emit := func(rl *otelpb.ResourceLogs) {
				svcName := resourceServiceName(rl.GetResource())
				for _, sl := range rl.GetScopeLogs() {
					for _, lr := range sl.GetLogRecords() {
						if jsonOutput {
							printLogRecordJSON(svcName, lr)
						} else {
							printLogRecord(svcName, lr)
						}
					}
				}
			}

			// --previous buffers history so we can cut at the most recent
			// container-exit boundary (the previous run is the logs before it).
			var prevHistory []*otelpb.ResourceLogs

			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fmt.Errorf("receiving logs: %w", err)
				}

				logs := resp.GetLogs()
				if logs == nil {
					continue
				}

				// Drop history older than the --since cutoff.
				if resp.IsHistory && sinceCutoffNanos > 0 {
					logs = filterResourceLogsAfter(logs, sinceCutoffNanos)
					if logs == nil {
						continue
					}
				}

				// Track whether any history was received.
				if resp.IsHistory {
					seenHistory = true
				}

				if previous {
					if resp.IsHistory {
						prevHistory = append(prevHistory, logs.GetResourceLogs()...)
					}
					// Live records are the current run; --previous ignores them.
					continue
				}

				// Print separator only when transitioning from actual history to live.
				if !liveSeparatorPrinted && seenHistory && !resp.IsHistory {
					liveSeparatorPrinted = true
					if !jsonOutput {
						fmt.Println(logMetaStyle.Render("── live ──────────────────────"))
					}
				}

				for _, rl := range logs.GetResourceLogs() {
					emit(rl)
				}
			}

			if historyOnly {
				for _, rl := range previousRunLogs(prevHistory) {
					emit(rl)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "Filter by application name")
	cmd.Flags().StringVar(&serviceName, "service", "", "Filter by service name")
	cmd.Flags().Int32Var(&minSeverity, "min-severity", 0, "Minimum log severity number")
	cmd.Flags().StringVar(&level, "level", "", "Minimum log level (trace, debug, info, warn, error, fatal)")
	cmd.Flags().Int32Var(&tail, "tail", 0, "Replay the last N log batches before streaming live (0 = live only)")
	cmd.Flags().DurationVar(&since, "since", 0, "Replay only history newer than this age (e.g. 5m, 1h); implies history replay")
	cmd.Flags().BoolVar(&previous, "previous", false, "Show the previous run of a stopped/crashed container (logs up to its last exit), then stop")

	return cmd
}

// maxLogReplay is the history window requested when --since/--previous is used
// without an explicit --tail. It matches the agent-side replay cap.
const maxLogReplay = 1000

// containerExitScope is the OTel instrumentation scope the agent stamps on
// structured container-exit events. Must match the agent's exitEventScope in
// go/internal/agent/services/container_log_manager.go.
const containerExitScope = "wendy.container.exit"

// container-exit event attribute keys, mirrored from the agent.
const (
	containerExitAttrCode  = "exit.code"
	containerExitAttrCrash = "crash"
)

// newDeviceCrashesCmd lists recent container exits/crashes by replaying the
// structured container-exit events the agent records in its telemetry buffer.
// It is a thin reader over StreamLogs history — no new RPC — so it surfaces the
// same retained, eviction-pinned crash records used by `wendy device logs`.
func newDeviceCrashesCmd() *cobra.Command {
	var appName string
	var limit int32
	var crashesOnly bool

	cmd := &cobra.Command{
		Use:   "crashes [app]",
		Short: "List recent container exits and crashes",
		Long: "List recent container exits and crashes recorded on the device.\n\n" +
			"Each row is a structured exit event (app, exit code, when). Crashes\n" +
			"(non-zero or unobservable exits) are retained even after ordinary log\n" +
			"history rolls over. Use `wendy device logs <app> --previous` to see the\n" +
			"crashed run's output.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if len(args) == 1 {
				if appName != "" && appName != args[0] {
					return fmt.Errorf("conflicting app names: %q (positional) and %q (--app)", args[0], appName)
				}
				appName = args[0]
			}

			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			// Replay history only; we never go live. Request records down to ERROR
			// so crash events (ERROR) and clean exits (INFO) both arrive, then
			// filter to exit events client-side.
			n := limit
			if n <= 0 {
				n = maxLogReplay
			}
			debugSev := parseSeverityLevel("debug")
			req := &agentpb.StreamLogsRequest{LastN: &n, MinSeverity: &debugSev}
			if appName != "" {
				req.AppName = &appName
			}

			stream, err := conn.TelemetryService.StreamLogs(ctx, req)
			if err != nil {
				return fmt.Errorf("starting log stream: %w", err)
			}

			var events []crashRow
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fmt.Errorf("receiving logs: %w", err)
				}
				// Once the live stream starts, history is complete.
				if !resp.GetIsHistory() {
					break
				}
				logs := resp.GetLogs()
				if logs == nil {
					continue
				}
				for _, rl := range logs.GetResourceLogs() {
					svcName := resourceServiceName(rl.GetResource())
					for _, sl := range rl.GetScopeLogs() {
						if sl.GetScope().GetName() != containerExitScope {
							continue
						}
						for _, lr := range sl.GetLogRecords() {
							row := crashRowFromRecord(svcName, lr)
							if crashesOnly && !row.crash {
								continue
							}
							events = append(events, row)
						}
					}
				}
			}

			if jsonOutput {
				return printCrashesJSON(events)
			}
			printCrashesTable(events)
			return nil
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "Filter by application name")
	cmd.Flags().Int32Var(&limit, "limit", 0, "Maximum number of recent exit events to scan (0 = default window)")
	cmd.Flags().BoolVar(&crashesOnly, "crashes-only", false, "Show only crashes (non-zero or unobservable exits), hiding clean exits")

	return cmd
}

// crashRow is a single rendered exit/crash event.
type crashRow struct {
	app      string
	whenNano uint64
	exitCode int64
	hasCode  bool
	crash    bool
	body     string
}

func crashRowFromRecord(svc string, lr *otelpb.LogRecord) crashRow {
	row := crashRow{
		app:      svc,
		whenNano: lr.GetTimeUnixNano(),
		body:     lr.GetBody().GetStringValue(),
	}
	for _, kv := range lr.GetAttributes() {
		switch kv.GetKey() {
		case containerExitAttrCode:
			row.exitCode = kv.GetValue().GetIntValue()
			row.hasCode = true
		case containerExitAttrCrash:
			row.crash = kv.GetValue().GetBoolValue()
		}
	}
	return row
}

func printCrashesTable(events []crashRow) {
	if len(events) == 0 {
		fmt.Println(logMetaStyle.Render("No container exits recorded in the retained window."))
		return
	}
	for _, e := range events {
		when := time.Unix(0, int64(e.whenNano)).Local().Format("2006-01-02 15:04:05")
		code := "—"
		if e.hasCode {
			code = fmt.Sprintf("%d", e.exitCode)
		}
		status, style := "exited", logInfoStyle
		if e.crash {
			status, style = "crashed", logErrorStyle
		}
		var b strings.Builder
		b.WriteString(logTimeStyle.Render(when))
		b.WriteByte(' ')
		b.WriteString(style.Render(fmt.Sprintf("%-7s", status)))
		b.WriteByte(' ')
		b.WriteString(logAppStyle.Render("[" + e.app + "]"))
		b.WriteByte(' ')
		b.WriteString(logMetaStyle.Render("exit=" + code))
		fmt.Println(b.String())
	}
}

func printCrashesJSON(events []crashRow) error {
	type jsonRow struct {
		App       string `json:"app"`
		Time      string `json:"time"`
		ExitCode  *int64 `json:"exitCode,omitempty"`
		Crash     bool   `json:"crash"`
		Message   string `json:"message"`
		TimeNanos uint64 `json:"timeUnixNano"`
	}
	out := make([]jsonRow, 0, len(events))
	for _, e := range events {
		r := jsonRow{
			App:       e.app,
			Time:      time.Unix(0, int64(e.whenNano)).UTC().Format(time.RFC3339),
			Crash:     e.crash,
			Message:   e.body,
			TimeNanos: e.whenNano,
		}
		if e.hasCode {
			c := e.exitCode
			r.ExitCode = &c
		}
		out = append(out, r)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// filterResourceLogsAfter returns a copy of logs keeping only records whose
// timestamp is at or after cutoffNanos, or nil if none remain.
func filterResourceLogsAfter(logs *otelpb.ExportLogsServiceRequest, cutoffNanos uint64) *otelpb.ExportLogsServiceRequest {
	var keptRL []*otelpb.ResourceLogs
	for _, rl := range logs.GetResourceLogs() {
		var keptSL []*otelpb.ScopeLogs
		for _, sl := range rl.GetScopeLogs() {
			var kept []*otelpb.LogRecord
			for _, lr := range sl.GetLogRecords() {
				if lr.GetTimeUnixNano() >= cutoffNanos {
					kept = append(kept, lr)
				}
			}
			if len(kept) > 0 {
				keptSL = append(keptSL, &otelpb.ScopeLogs{Scope: sl.GetScope(), LogRecords: kept})
			}
		}
		if len(keptSL) > 0 {
			keptRL = append(keptRL, &otelpb.ResourceLogs{Resource: rl.GetResource(), ScopeLogs: keptSL})
		}
	}
	if len(keptRL) == 0 {
		return nil
	}
	return &otelpb.ExportLogsServiceRequest{ResourceLogs: keptRL}
}

// previousRunLogs returns the subset of buffered history belonging to the
// container's previous run: everything up to and including the most recent
// container-exit event. If no exit event is present (the app never exited in
// the retained window), the full buffered history is returned.
func previousRunLogs(history []*otelpb.ResourceLogs) []*otelpb.ResourceLogs {
	lastExit := -1
	for i, rl := range history {
		for _, sl := range rl.GetScopeLogs() {
			if sl.GetScope().GetName() == containerExitScope {
				lastExit = i
			}
		}
	}
	if lastExit < 0 {
		return history
	}
	return history[:lastExit+1]
}

var (
	logTraceStyle = lipgloss.NewStyle().Foreground(tui.ColorDim)
	logDebugStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logInfoStyle  = lipgloss.NewStyle().Foreground(tui.Emerald400)
	logWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	logErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	logFatalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	logTimeStyle  = lipgloss.NewStyle().Foreground(tui.ColorDim)
	logAppStyle   = lipgloss.NewStyle().Foreground(tui.Emerald300)
	logMetaStyle  = lipgloss.NewStyle().Foreground(tui.ColorDim)
)

func severityLabel(sev otelpb.SeverityNumber) (string, lipgloss.Style) {
	switch {
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL:
		return "FATAL", logFatalStyle
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR:
		return "ERROR", logErrorStyle
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_WARN:
		return "WARN ", logWarnStyle
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_INFO:
		return "INFO ", logInfoStyle
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG:
		return "DEBUG", logDebugStyle
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE:
		return "TRACE", logTraceStyle
	default:
		return "     ", logInfoStyle
	}
}

func resourceServiceName(res *otelpb.Resource) string {
	if res == nil {
		return ""
	}
	for _, attr := range res.GetAttributes() {
		if attr.GetKey() == "service.name" {
			return attr.GetValue().GetStringValue()
		}
	}
	return ""
}

func anyValueString(v *otelpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch v.Value.(type) {
	case *otelpb.AnyValue_StringValue:
		return v.GetStringValue()
	case *otelpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", v.GetIntValue())
	case *otelpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", v.GetDoubleValue())
	case *otelpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", v.GetBoolValue())
	default:
		return fmt.Sprintf("%v", v)
	}
}

func printLogRecordJSON(service string, lr *otelpb.LogRecord) {
	entry := map[string]any{
		"timestamp": time.Unix(0, int64(lr.GetTimeUnixNano())).UTC().Format(time.RFC3339Nano),
		"severity":  lr.GetSeverityText(),
	}
	if service != "" {
		entry["service"] = service
	}
	if body := lr.GetBody(); body != nil {
		entry["body"] = body.GetStringValue()
	}
	if attrs := lr.GetAttributes(); len(attrs) > 0 {
		meta := make(map[string]string, len(attrs))
		for _, kv := range attrs {
			meta[kv.GetKey()] = anyValueString(kv.GetValue())
		}
		entry["attributes"] = meta
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
}

func printLogRecord(service string, lr *otelpb.LogRecord) {
	ts := time.Unix(0, int64(lr.GetTimeUnixNano())).Local().Format("15:04:05.000")
	label, style := severityLabel(lr.GetSeverityNumber())

	var b strings.Builder
	b.WriteString(logTimeStyle.Render(ts))
	b.WriteByte(' ')
	b.WriteString(style.Render(label))
	if service != "" {
		b.WriteByte(' ')
		b.WriteString(logAppStyle.Render("[" + service + "]"))
	}

	body := lr.GetBody()
	if body != nil {
		b.WriteByte(' ')
		b.WriteString(body.GetStringValue())
	}

	attrs := lr.GetAttributes()
	if len(attrs) > 0 {
		b.WriteByte(' ')
		for i, kv := range attrs {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(logMetaStyle.Render(kv.GetKey() + "=" + anyValueString(kv.GetValue())))
		}
	}

	fmt.Println(b.String())
}

func newDeviceTelemetryStreamCmd() *cobra.Command {
	var appName string
	var serviceName string
	var enableLogs bool
	var enableMetrics bool
	var enableTraces bool

	cmd := &cobra.Command{
		Use:    "telemetry-stream",
		Short:  "Stream telemetry data (logs, metrics, traces) as JSONL",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no flags were explicitly set, enable all streams.
			if !cmd.Flags().Changed("logs") && !cmd.Flags().Changed("metrics") && !cmd.Flags().Changed("traces") {
				enableLogs = true
				enableMetrics = true
				enableTraces = true
			}

			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			enc := json.NewEncoder(os.Stdout)

			var mu sync.Mutex
			emit := func(v any) {
				mu.Lock()
				defer mu.Unlock()
				enc.Encode(v) //nolint:errcheck
			}

			var wg sync.WaitGroup
			errc := make(chan error, 3)

			if enableLogs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					logReq := &agentpb.StreamLogsRequest{}
					if appName != "" {
						logReq.AppName = &appName
					}
					if serviceName != "" {
						logReq.ServiceName = &serviceName
					}
					stream, err := conn.TelemetryService.StreamLogs(ctx, logReq)
					if err != nil {
						errc <- fmt.Errorf("starting log stream: %w", err)
						return
					}
					for {
						resp, err := stream.Recv()
						if err == io.EOF {
							return
						}
						if err != nil {
							errc <- fmt.Errorf("receiving logs: %w", err)
							return
						}
						logs := resp.GetLogs()
						if logs == nil {
							continue
						}
						for _, rl := range logs.GetResourceLogs() {
							res := kvMapFromResource(rl.GetResource())
							svc := res["service.name"]
							for _, sl := range rl.GetScopeLogs() {
								for _, lr := range sl.GetLogRecords() {
									sev, sevNum := severityTextAndNumber(lr.GetSeverityNumber())
									emit(telemetryLogEntry{
										Type:           "log",
										Timestamp:      formatNanoUTC(lr.GetTimeUnixNano()),
										TimestampNano:  lr.GetTimeUnixNano(),
										Severity:       sev,
										SeverityNumber: sevNum,
										Service:        svc,
										Resource:       res,
										Body:           anyValueString(lr.GetBody()),
										Attributes:     kvMapFromKeyValues(lr.GetAttributes()),
									})
								}
							}
						}
					}
				}()
			}

			if enableMetrics {
				wg.Add(1)
				go func() {
					defer wg.Done()
					metricReq := &agentpb.StreamMetricsRequest{}
					if appName != "" {
						metricReq.AppName = &appName
					}
					if serviceName != "" {
						metricReq.ServiceName = &serviceName
					}
					stream, err := conn.TelemetryService.StreamMetrics(ctx, metricReq)
					if err != nil {
						errc <- fmt.Errorf("starting metrics stream: %w", err)
						return
					}
					for {
						resp, err := stream.Recv()
						if err == io.EOF {
							return
						}
						if err != nil {
							errc <- fmt.Errorf("receiving metrics: %w", err)
							return
						}
						metrics := resp.GetMetrics()
						if metrics == nil {
							continue
						}
						for _, rm := range metrics.GetResourceMetrics() {
							res := kvMapFromResource(rm.GetResource())
							svc := res["service.name"]
							for _, sm := range rm.GetScopeMetrics() {
								for _, m := range sm.GetMetrics() {
									emitMetricDataPoints(emit, m, svc, res)
								}
							}
						}
					}
				}()
			}

			if enableTraces {
				wg.Add(1)
				go func() {
					defer wg.Done()
					traceReq := &agentpb.StreamTracesRequest{}
					if appName != "" {
						traceReq.AppName = &appName
					}
					if serviceName != "" {
						traceReq.ServiceName = &serviceName
					}
					stream, err := conn.TelemetryService.StreamTraces(ctx, traceReq)
					if err != nil {
						errc <- fmt.Errorf("starting traces stream: %w", err)
						return
					}
					for {
						resp, err := stream.Recv()
						if err == io.EOF {
							return
						}
						if err != nil {
							errc <- fmt.Errorf("receiving traces: %w", err)
							return
						}
						traces := resp.GetTraces()
						if traces == nil {
							continue
						}
						for _, rs := range traces.GetResourceSpans() {
							res := kvMapFromResource(rs.GetResource())
							svc := res["service.name"]
							for _, ss := range rs.GetScopeSpans() {
								for _, span := range ss.GetSpans() {
									startNano := span.GetStartTimeUnixNano()
									endNano := span.GetEndTimeUnixNano()
									durationMs := float64(endNano-startNano) / 1e6

									status := telemetryTraceStatus{Code: "UNSET"}
									if s := span.GetStatus(); s != nil {
										status.Code = s.GetCode().String()
										status.Message = s.GetMessage()
									}

									emit(telemetryTraceEntry{
										Type:          "span",
										TraceID:       hex.EncodeToString(span.GetTraceId()),
										SpanID:        hex.EncodeToString(span.GetSpanId()),
										ParentSpanID:  hex.EncodeToString(span.GetParentSpanId()),
										Name:          span.GetName(),
										Kind:          span.GetKind().String(),
										StartTime:     formatNanoUTC(startNano),
										EndTime:       formatNanoUTC(endNano),
										StartTimeNano: startNano,
										EndTimeNano:   endNano,
										DurationMs:    durationMs,
										Status:        status,
										Service:       svc,
										Attributes:    kvMapFromKeyValues(span.GetAttributes()),
										Resource:      res,
									})
								}
							}
						}
					}
				}()
			}

			// Wait for all goroutines, return first error if any.
			go func() {
				wg.Wait()
				close(errc)
			}()

			for err := range errc {
				if err != nil && ctx.Err() == nil {
					return err
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "Filter by application name")
	cmd.Flags().StringVar(&serviceName, "service", "", "Filter by service name")
	cmd.Flags().BoolVar(&enableLogs, "logs", false, "Include logs")
	cmd.Flags().BoolVar(&enableMetrics, "metrics", false, "Include metrics")
	cmd.Flags().BoolVar(&enableTraces, "traces", false, "Include traces")

	return cmd
}

type telemetryLogEntry struct {
	Type           string            `json:"type"`
	Timestamp      string            `json:"timestamp"`
	TimestampNano  uint64            `json:"timestampNano"`
	Severity       string            `json:"severity"`
	SeverityNumber int32             `json:"severityNumber"`
	Service        string            `json:"service"`
	Resource       map[string]string `json:"resource"`
	Body           string            `json:"body"`
	Attributes     map[string]string `json:"attributes"`
}

type telemetryMetricEntry struct {
	Type       string            `json:"type"`
	Timestamp  string            `json:"timestamp"`
	Service    string            `json:"service"`
	Resource   map[string]string `json:"resource,omitempty"`
	Name       string            `json:"name"`
	Value      float64           `json:"value"`
	MetricType string            `json:"metricType"`
	Unit       string            `json:"unit,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type telemetryTraceStatus struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type telemetryTraceEntry struct {
	Type          string               `json:"type"`
	TraceID       string               `json:"traceId"`
	SpanID        string               `json:"spanId"`
	ParentSpanID  string               `json:"parentSpanId,omitempty"`
	Name          string               `json:"name"`
	Kind          string               `json:"kind"`
	StartTime     string               `json:"startTime"`
	EndTime       string               `json:"endTime"`
	StartTimeNano uint64               `json:"startTimeNano"`
	EndTimeNano   uint64               `json:"endTimeNano"`
	DurationMs    float64              `json:"durationMs"`
	Status        telemetryTraceStatus `json:"status"`
	Service       string               `json:"service"`
	Attributes    map[string]string    `json:"attributes,omitempty"`
	Resource      map[string]string    `json:"resource,omitempty"`
}

// emitMetricDataPoints extracts the latest value from a metric and emits one
// telemetryMetricEntry per metric (using the last data point's value).
func emitMetricDataPoints(emit func(any), m *otelpb.Metric, svc string, res map[string]string) {
	var value float64
	var metricType string
	var attrs map[string]string

	switch {
	case m.GetGauge() != nil:
		metricType = "gauge"
		dps := m.GetGauge().GetDataPoints()
		if len(dps) > 0 {
			dp := dps[len(dps)-1]
			value = numberDataPointValue(dp)
			attrs = kvMapFromKeyValues(dp.GetAttributes())
		}
	case m.GetSum() != nil:
		metricType = "sum"
		dps := m.GetSum().GetDataPoints()
		if len(dps) > 0 {
			dp := dps[len(dps)-1]
			value = numberDataPointValue(dp)
			attrs = kvMapFromKeyValues(dp.GetAttributes())
		}
	case m.GetHistogram() != nil:
		metricType = "histogram"
		dps := m.GetHistogram().GetDataPoints()
		if len(dps) > 0 {
			dp := dps[len(dps)-1]
			if dp.GetSum() != 0 && dp.GetCount() != 0 {
				value = dp.GetSum() / float64(dp.GetCount())
			}
			attrs = kvMapFromKeyValues(dp.GetAttributes())
		}
	case m.GetSummary() != nil:
		metricType = "summary"
		dps := m.GetSummary().GetDataPoints()
		if len(dps) > 0 {
			dp := dps[len(dps)-1]
			if dp.GetCount() != 0 {
				value = dp.GetSum() / float64(dp.GetCount())
			}
			attrs = kvMapFromKeyValues(dp.GetAttributes())
		}
	default:
		metricType = "unknown"
	}

	emit(telemetryMetricEntry{
		Type:       "metric",
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Service:    svc,
		Resource:   res,
		Name:       m.GetName(),
		Value:      value,
		MetricType: metricType,
		Unit:       m.GetUnit(),
		Attributes: attrs,
	})
}

// numberDataPointValue extracts the numeric value from a NumberDataPoint.
func numberDataPointValue(dp *otelpb.NumberDataPoint) float64 {
	switch dp.GetValue().(type) {
	case *otelpb.NumberDataPoint_AsDouble:
		return dp.GetAsDouble()
	case *otelpb.NumberDataPoint_AsInt:
		return float64(dp.GetAsInt())
	default:
		return 0
	}
}

func formatNanoUTC(nanos uint64) string {
	return time.Unix(0, int64(nanos)).UTC().Format(time.RFC3339Nano)
}

func severityTextAndNumber(sev otelpb.SeverityNumber) (string, int32) {
	num := int32(sev)
	switch {
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL:
		return "FATAL", num
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR:
		return "ERROR", num
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_WARN:
		return "WARN", num
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_INFO:
		return "INFO", num
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG:
		return "DEBUG", num
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE:
		return "TRACE", num
	default:
		return "UNSPECIFIED", num
	}
}

func kvMapFromResource(res *otelpb.Resource) map[string]string {
	m := make(map[string]string)
	if res == nil {
		return m
	}
	for _, attr := range res.GetAttributes() {
		m[attr.GetKey()] = anyValueString(attr.GetValue())
	}
	return m
}

func kvMapFromKeyValues(kvs []*otelpb.KeyValue) map[string]string {
	m := make(map[string]string)
	for _, kv := range kvs {
		m[kv.GetKey()] = anyValueString(kv.GetValue())
	}
	return m
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubReleaseFull struct {
	TagName    string               `json:"tag_name"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

func fetchAgentRelease(nightly bool) (*githubReleaseFull, error) {
	client := newGitHubAPIClient(30 * time.Second)

	if !nightly {
		req, err := newGitHubAPIGetRequest(githubReleasesURL)
		if err != nil {
			return nil, fmt.Errorf("creating GitHub API request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching latest release: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
		}

		var release githubReleaseFull
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			return nil, fmt.Errorf("decoding release: %w", err)
		}
		return &release, nil
	}

	// For nightly, list releases and find the latest prerelease.
	req, err := newGitHubAPIGetRequest("https://api.github.com/repos/wendylabsinc/wendy-agent/releases")
	if err != nil {
		return nil, fmt.Errorf("creating GitHub API request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var releases []githubReleaseFull
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}

	for _, r := range releases {
		if r.Prerelease {
			return &r, nil
		}
	}

	return nil, fmt.Errorf("no nightly (prerelease) found")
}

func downloadAgentBinary(asset githubReleaseAsset) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}

	resp, err := client.Get(asset.BrowserDownloadURL)
	if err != nil {
		return nil, fmt.Errorf("downloading asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("opening gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		if hdr.Typeflag == tar.TypeReg && strings.HasSuffix(hdr.Name, "wendy-agent") {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading binary from tar: %w", err)
			}
			return data, nil
		}
	}

	return nil, fmt.Errorf("wendy-agent binary not found in tarball")
}

// reconnectAgentAfterRestart re-establishes a connection to the SAME device
// conn targets, after the agent has restarted. When the connection carries a
// Reconnect closure (cloud tunnel — pinned to a specific asset id) that is
// used, so the reconnect can never drift to a different device. Otherwise it
// re-dials the known host directly (LAN).
func reconnectAgentAfterRestart(ctx context.Context, conn *grpcclient.AgentConnection) (*grpcclient.AgentConnection, error) {
	if conn != nil && conn.Reconnect != nil {
		return conn.Reconnect(ctx)
	}
	return waitForAgentRestart(ctx, hostPort(conn.Host, defaultAgentPort))
}

// maybeCheckOSUpdate runs the OS-update step for `device update` after the
// agent has been updated. preUpdateVersion is the version queried before the
// agent restart; it is used only for a cheap up-front gate, since whether a
// device runs WendyOS and supports mender does not change across an agent
// update. When artifactURLOverride is set, that exact Mender artifact is
// applied (prompting unless assumeYes) instead of the manifest's latest;
// otherwise the device_type/storage_medium/os_version used for the decision
// are re-read from the agent we just installed — a newer agent can report a
// corrected device_type, so the pre-update snapshot may be stale.
// Non-WendyOS / no-mender targets are skipped silently (no reconnect), and any
// failure to re-read or look up the OS is reported but non-fatal — `device
// update` still succeeds as an agent-only update.
func maybeCheckOSUpdate(ctx context.Context, preUpdateVersion *agentpb.GetAgentVersionResponse, priorConn *grpcclient.AgentConnection, nightly, assumeYes bool, artifactURLOverride string) error {
	if preUpdateVersion == nil {
		return nil
	}
	if !isWendyOSUpdateTarget(preUpdateVersion) || !agentVersionHasFeature(preUpdateVersion, "mender") {
		return nil
	}

	// Any OS work needs a live connection to the just-restarted agent. Reconnect
	// to the SAME device (priorConn pins the cloud asset by id, so this can't
	// drift to another device); the device pulls the artifact straight from its
	// URL (e.g. GCS) while only the control stream is tunneled.
	fmt.Println("Checking for OS updates...")
	conn, err := reconnectAgentAfterRestart(ctx, priorConn)
	if err != nil {
		fmt.Printf("Could not check for OS updates: %v\n", err)
		return nil
	}
	defer conn.Close()

	var otaURL string
	if artifactURLOverride != "" {
		// Explicit artifact: apply it as-is, no manifest lookup or version
		// comparison (mirrors `os update --artifact-url`).
		if !assumeYes {
			if !isInteractiveTerminal() {
				fmt.Printf("OS artifact specified (%s). Re-run with --yes to apply.\n", artifactURLOverride)
				return nil
			}
			if !promptYesNoDefaultNoFn(fmt.Sprintf("Apply OS update from %s? [y/N] ", artifactURLOverride)) {
				fmt.Println("Skipping OS update.")
				return nil
			}
		}
		otaURL = artifactURLOverride
	} else {
		// Re-read the version from the just-installed agent: its reported
		// device_type is the value that must match the OTA manifest key, and a
		// newer agent may report it correctly where an older one did not.
		versionResp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
		if err != nil {
			fmt.Printf("Could not check for OS updates: %v\n", err)
			return nil
		}

		deviceType := versionResp.GetDeviceType()
		if deviceType == "" {
			// No device type → cannot auto-select the GCS artifact; skip quietly.
			return nil
		}

		u, latestVer, err := getLatestOTAInfoForDeviceType(deviceType, versionResp.GetStorageMedium(), nightly)
		if err != nil {
			fmt.Printf("Could not check for OS updates: %v\n", err)
			return nil
		}

		currentOS := versionResp.GetOsVersion()
		fromVer := strings.TrimPrefix(currentOS, "WendyOS-")
		if fromVer == "" {
			fromVer = "unknown"
		}

		switch decideOSUpdate(currentOS, latestVer, nightly, assumeYes, isInteractiveTerminal()) {
		case osActionAlreadyCurrent:
			fmt.Printf("OS is already at the latest version (%s).\n", currentOS)
			return nil
		case osActionReportOnly:
			fmt.Printf("OS update available (%s). Re-run with --yes or run 'wendy os update' to apply.\n", latestVer)
			return nil
		case osActionApply:
			// fall through to apply
		case osActionPrompt:
			if !promptYesNoDefaultNoFn(fmt.Sprintf("OS update available (%s → %s). Apply now? [y/N] ", fromVer, latestVer)) {
				fmt.Println("Skipping OS update. Run 'wendy os update' to apply later.")
				return nil
			}
		}
		otaURL = u
	}

	if err := streamOSUpdate(ctx, conn, otaURL, ""); err != nil {
		return err
	}

	if _, isCloud := cloudDeviceConfigFromContext(ctx); isCloud {
		fmt.Println("OS update applied; the device is rebooting. Reconnect once it is back online.")
		return nil
	}
	fmt.Println("WendyOS update applied. Device is rebooting...")
	if err := waitForDeviceOnline(ctx, priorConn.Host); err != nil {
		return err
	}
	fmt.Println("Device is back online.")
	return nil
}

func newDeviceUpdateCmd() *cobra.Command {
	var binaryPath string
	var nightly bool
	var assumeYes bool
	var artifactURL string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the agent binary on the target device",
		Long: "Updates the agent binary on the device (downloaded from GitHub, or --binary for a local file), then checks for a newer WendyOS image. " +
			"When an OS update is available it prompts before applying (default no); use --yes to apply without prompting. Non-interactive runs report the available update without applying it. " +
			"--nightly selects the nightly channel for both the agent and the OS. " +
			"--artifact-url applies a specific OS (Mender) artifact instead of the manifest's latest; this works over the cloud tunnel (the device downloads the artifact directly from the URL).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			conn, err := connectToAgent(ctx, ExcludeProviders("local", "docker", "wendy-lite"), ExcludeBluetooth(), SuppressUpdateCheck())
			if err != nil {
				return err
			}
			defer func() {
				if conn != nil {
					_ = conn.Close()
				}
			}()

			var preUpdateVersion *agentpb.GetAgentVersionResponse

			var binaryData []byte

			if binaryPath != "" {
				binaryData, err = os.ReadFile(binaryPath)
				if err != nil {
					return fmt.Errorf("reading binary: %w", err)
				}

				// Validate the binary's ELF architecture against the device.
				// If the device is provisioned and only exposes ProvisioningService
				// on plaintext, GetAgentVersion may be unavailable — skip arch
				// validation in that case rather than blocking the upload.
				versionResp, versionErr := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if versionErr == nil {
					preUpdateVersion = versionResp
					deviceArch := versionResp.GetCpuArchitecture()
					if deviceArch != "" {
						if err := checkELFArchitecture(binaryData, deviceArch); err != nil {
							return err
						}
					}
				}
			} else {
				// Auto-download: detect arch, fetch release, download binary.
				if !jsonOutput {
					fmt.Println(tui.InfoMessage("Detecting device architecture..."))
				}
				versionResp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
				if err != nil {
					return fmt.Errorf("getting device info: %w", err)
				}
				preUpdateVersion = versionResp

				arch := versionResp.GetCpuArchitecture()
				if arch == "" {
					return fmt.Errorf("device did not report CPU architecture; use --binary to provide the binary manually")
				}
				if !jsonOutput {
					fmt.Printf("%s %s\n", tui.Dim("Architecture:"), tui.Value(arch))
				}

				releaseType := "stable"
				if nightly {
					releaseType = "nightly"
				}
				if !jsonOutput {
					fmt.Println(tui.InfoMessage(fmt.Sprintf("Fetching latest %s release...", releaseType)))
				}

				release, err := fetchAgentRelease(nightly)
				if err != nil {
					return fmt.Errorf("fetching release: %w", err)
				}
				if !jsonOutput {
					fmt.Printf("%s %s\n", tui.Dim("Release:"), tui.Value(release.TagName))
				}

				// Find matching asset: wendy-agent-linux-{arch}-*.tar.gz
				assetPrefix := fmt.Sprintf("wendy-agent-linux-%s-", arch)
				var matchedAsset *githubReleaseAsset
				for _, a := range release.Assets {
					if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, ".tar.gz") {
						matchedAsset = &a
						break
					}
				}
				if matchedAsset == nil {
					return fmt.Errorf("no asset found for linux/%s in release %s", arch, release.TagName)
				}

				if !jsonOutput {
					fmt.Println(tui.InfoMessage(fmt.Sprintf("Downloading %s...", matchedAsset.Name)))
				}
				binaryData, err = downloadAgentBinary(*matchedAsset)
				if err != nil {
					return fmt.Errorf("downloading binary: %w", err)
				}
			}

			// Compute SHA256.
			h := sha256.Sum256(binaryData)
			sha256Hash := hex.EncodeToString(h[:])

			if isInteractiveTerminal() && !jsonOutput {
				s := tui.NewSpinner("Uploading agent binary...")
				p := tui.NewProgressProgram(s)

				go func() {
					uploadErr := deviceUpdateUpload(ctx, conn.AgentService, binaryData, sha256Hash)
					p.Send(tui.SpinnerDoneMsg{Err: uploadErr})
				}()

				finalModel, runErr := p.Run()
				if runErr != nil {
					return fmt.Errorf("TUI error: %w", runErr)
				}

				model := finalModel.(tui.SpinnerModel)
				_, updateErr := model.Result()
				if updateErr != nil {
					return updateErr
				}
			} else if !jsonOutput {
				fmt.Println(tui.InfoMessage("Uploading agent binary..."))
				if err := deviceUpdateUpload(ctx, conn.AgentService, binaryData, sha256Hash); err != nil {
					return err
				}
			} else {
				if err := deviceUpdateUpload(ctx, conn.AgentService, binaryData, sha256Hash); err != nil {
					return err
				}
			}

			reconnect := updatedAgentReconnectFunc(ctx, conn)
			if conn != nil {
				_ = conn.Close()
				conn = nil
			}
			if isInteractiveTerminal() && !jsonOutput {
				readyConn, err := runAgentConnectionSpinner(ctx, "Waiting for agent to restart...", func(spinCtx context.Context) (*grpcclient.AgentConnection, error) {
					return waitForUpdatedAgentReady(spinCtx, reconnect, agentRestartWaitOptions{})
				})
				if err != nil {
					return err
				}
				if readyConn != nil {
					_ = readyConn.Close()
				}
			} else {
				if !jsonOutput {
					fmt.Println(tui.InfoMessage("Waiting for agent to restart..."))
				}
				readyConn, err := waitForUpdatedAgentReady(ctx, reconnect, agentRestartWaitOptions{})
				if err != nil {
					return err
				}
				if readyConn != nil {
					_ = readyConn.Close()
				}
			}

			if jsonOutput {
				resp := map[string]string{
					"status":  "success",
					"message": "Agent updated successfully.",
				}
				b, err := json.Marshal(resp)
				if err != nil {
					return fmt.Errorf("failed to marshal JSON response: %w", err)
				}
				fmt.Println(string(b))
				// OS update check is skipped in JSON mode to keep output stable.
				return nil
			}
			fmt.Println(tui.SuccessMessage("Agent updated successfully."))
			if err := maybeCheckOSUpdate(ctx, preUpdateVersion, conn, nightly, assumeYes, artifactURL); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&binaryPath, "binary", "", "Path to a local agent binary to upload (skips download)")
	cmd.Flags().BoolVar(&nightly, "nightly", false, "Use the latest nightly (prerelease) build for both the agent and the OS")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Apply an available OS update without prompting")
	cmd.Flags().StringVar(&artifactURL, "artifact-url", "", "Apply this OS (Mender) artifact URL instead of the manifest's latest")

	return cmd
}

func checkELFArchitecture(data []byte, deviceArch string) error {
	// Only amd64 and arm64 are supported targets.
	switch deviceArch {
	case "amd64", "arm64":
		// supported, continue
	default:
		return fmt.Errorf("device reports unsupported architecture %q; only amd64 and arm64 are supported", deviceArch)
	}

	// ELF magic + header fields up to e_machine occupy 20 bytes.
	if len(data) < 20 {
		return nil
	}
	if data[0] != 0x7f || data[1] != 'E' || data[2] != 'L' || data[3] != 'F' {
		return nil // not an ELF binary — skip check
	}

	// Respect EI_DATA (byte 5) when reading the 2-byte e_machine field at offset 18.
	const (
		elfDataLittle = 1 // ELFDATA2LSB
		elfDataBig    = 2 // ELFDATA2MSB

		emX86_64  = 62  // EM_X86_64  → amd64
		emAArch64 = 183 // EM_AARCH64 → arm64
	)

	var machine uint16
	switch data[5] {
	case elfDataLittle:
		machine = uint16(data[18]) | uint16(data[19])<<8
	case elfDataBig:
		machine = uint16(data[18])<<8 | uint16(data[19])
	default:
		return nil // unknown ELF endianness — skip check
	}

	var binaryArch string
	switch machine {
	case emX86_64:
		binaryArch = "amd64"
	case emAArch64:
		binaryArch = "arm64"
	default:
		return nil // unrecognised ELF machine type — let the device decide
	}

	if binaryArch != deviceArch {
		return fmt.Errorf("binary is %s but device is %s; provide the correct binary with --binary", binaryArch, deviceArch)
	}
	return nil
}

type agentRestartWaitOptions struct {
	InitialDelay time.Duration
	Timeout      time.Duration
	PollInterval time.Duration
}

const (
	defaultAgentRestartInitialDelay = time.Second
	defaultAgentRestartTimeout      = 20 * time.Second
	defaultAgentRestartPollInterval = time.Second
)

func deviceUpdateUpload(ctx context.Context, agentService agentpb.WendyAgentServiceClient, binaryData []byte, sha256Hash string) error {
	stream, err := agentService.UpdateAgent(ctx)
	if err != nil {
		return fmt.Errorf("starting agent update: %w", err)
	}

	// Send binary in chunks.
	const chunkSize = 64 * 1024
	for offset := 0; offset < len(binaryData); offset += chunkSize {
		end := offset + chunkSize
		if end > len(binaryData) {
			end = len(binaryData)
		}

		if err := stream.Send(&agentpb.UpdateAgentRequest{
			RequestType: &agentpb.UpdateAgentRequest_Chunk_{
				Chunk: &agentpb.UpdateAgentRequest_Chunk{
					Data: binaryData[offset:end],
				},
			},
		}); err != nil {
			return fmt.Errorf("sending binary chunk: %w", err)
		}
	}

	// Send update control command with SHA256.
	if err := stream.Send(&agentpb.UpdateAgentRequest{
		RequestType: &agentpb.UpdateAgentRequest_Control{
			Control: &agentpb.UpdateAgentRequest_ControlCommand{
				Command: &agentpb.UpdateAgentRequest_ControlCommand_Update_{
					Update: &agentpb.UpdateAgentRequest_ControlCommand_Update{
						Sha256: sha256Hash,
					},
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("sending update command: %w", err)
	}

	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing send: %w", err)
	}

	// Wait for the Updated response.
	for {
		resp, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return fmt.Errorf("receiving update response: %w", recvErr)
		}
		if resp.GetUpdated() != nil {
			return nil
		}
	}

	return nil
}

func updatedAgentReconnectFunc(ctx context.Context, previous *grpcclient.AgentConnection) func(context.Context) (*grpcclient.AgentConnection, error) {
	if cloudCfg, ok := cloudDeviceConfigFromContext(ctx); ok {
		return func(waitCtx context.Context) (*grpcclient.AgentConnection, error) {
			return connectToCloudAgent(waitCtx, cloudCfg.CloudGRPC, cloudCfg.DeviceName, cloudCfg.BrokerURL)
		}
	}

	if addr, _, err := resolveDeviceAddress(); err == nil {
		hostname := addr
		if host, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
			hostname = host
		}
		return func(waitCtx context.Context) (*grpcclient.AgentConnection, error) {
			return connectResolvedAgentWithProvisionedHint(waitCtx, hostname, addr, false, deferProvisionedMTLSCheck(waitCtx, addr))
		}
	}

	if previous != nil && previous.Host != "" {
		addr := hostPort(previous.Host, defaultAgentPort)
		return func(waitCtx context.Context) (*grpcclient.AgentConnection, error) {
			return connectResolvedAgentWithProvisionedHint(waitCtx, previous.Host, addr, false, func() bool { return false })
		}
	}

	return func(waitCtx context.Context) (*grpcclient.AgentConnection, error) {
		return connectToAgent(waitCtx,
			ExcludeProviders("local", "docker", "wendy-lite"),
			ExcludeBluetooth(),
			SuppressUpdateCheck(),
			SuppressProvisioningHint(),
			NonInteractive(),
		)
	}
}

func waitForUpdatedAgentReady(ctx context.Context, reconnect func(context.Context) (*grpcclient.AgentConnection, error), opts agentRestartWaitOptions) (*grpcclient.AgentConnection, error) {
	if opts.InitialDelay <= 0 {
		opts.InitialDelay = defaultAgentRestartInitialDelay
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultAgentRestartTimeout
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultAgentRestartPollInterval
	}

	if err := sleepContext(ctx, opts.InitialDelay); err != nil {
		return nil, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var lastErr error
	for {
		conn, err := reconnect(waitCtx)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		if waitCtx.Err() != nil {
			break
		}
		if err := sleepContext(waitCtx, opts.PollInterval); err != nil {
			break
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("agent did not become reachable after update: %w", lastErr)
	}
	return nil, fmt.Errorf("agent did not become reachable after update: %w", waitCtx.Err())
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
