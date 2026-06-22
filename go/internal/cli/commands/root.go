// Package commands defines all Cobra commands for the Wendy CLI.
package commands

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

var (
	jsonOutput bool
	deviceFlag string
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "wendy",
		Short:         "Wendy CLI - Edge Computing Development Tool",
		Long:          "Wendy is a CLI for developing and deploying edge computing applications to WendyOS devices.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip heavy init for commands that don't need device/cloud setup.
			switch cmd.Name() {
			case "__ble-check", "open-browser":
				return nil
			}

			if !cmd.Root().PersistentFlags().Changed("json") && !isInteractiveTerminal() {
				jsonOutput = true
			}

			premark := phaseTimer()
			providers.Initialize(cmd.Context())
			premark("  prerun: providers.Initialize")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			premark("  prerun: config.Load")

			firstRun := analytics.Init(cfg)
			premark("  prerun: analytics.Init")
			if firstRun {
				cmd.PrintErrln("Attention: The Wendy CLI collects anonymous analytics.")
				cmd.PrintErrln("They help us understand which commands are used most, identify common errors, and prioritize improvements.")
				cmd.PrintErrln("Analytics are enabled by default. If you'd like to opt-out, use the following command:")
				cmd.PrintErrln("  wendy analytics disable")
				cmd.PrintErrln("Or, set the following environment variable:")
				cmd.PrintErrln("  WENDY_ANALYTICS=false")

				cfg.Analytics = &config.AnalyticsConfig{Enabled: true}
				if err := config.Save(cfg); err != nil {
					return err
				}
			}

			// Refresh MCP config and skills if the CLI was upgraded since the
			// user last ran `wendy mcp setup`. Runs synchronously here, before
			// the update-check goroutine below also mutates and saves cfg.
			maybeRefreshMCPSetup(cfg)
			premark("  prerun: maybeRefreshMCPSetup")

			if dueCLIUpdateCheck(cfg) {
				scheduleCLIUpdateCheck(cfg)
			}
			premark("  prerun: dueCLIUpdateCheck")

			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			// Load fresh config so we see any value written by the background
			// goroutine (possibly from a previous invocation).
			cfg, err := config.Load()
			if err != nil || cfg.AvailableCLIUpdate == "" {
				return nil
			}
			// Double-check: user may have updated since the goroutine last ran.
			if version.CompareVersions(cfg.AvailableCLIUpdate, version.Version) <= 0 {
				return nil
			}
			newVersion := cfg.AvailableCLIUpdate

			var updateShellCmd string
			switch runtime.GOOS {
			case "windows":
				updateShellCmd = "winget upgrade WendyLabs.Wendy"
			case "darwin":
				updateShellCmd = "brew update && brew install wendy"
			default:
				updateShellCmd = "curl -fsSL https://install.wendy.sh/cli.sh | bash"
			}

			if jsonOutput || !isInteractiveTerminal() {
				msg := "\nA new version of the Wendy CLI is available: %s (you have %s)\nUpdate with: %s\n"
				if runtime.GOOS == "darwin" {
					msg += "  (if the tap is untrusted: brew trust wendylabsinc/tap)\n"
				}
				cmd.PrintErrf(msg, newVersion, version.Version, updateShellCmd)
				return nil
			}

			cmd.PrintErrf("\nA new version of the Wendy CLI is available: %s (you have %s)\n", newVersion, version.Version)
			confirmed, promptErr := tui.ConfirmDefaultYes("Update now?", tea.WithOutput(os.Stderr))

			// Clear the stored version so the prompt doesn't reappear on the next
			// command regardless of the user's choice; it'll re-surface after the
			// next 24 h update check if still relevant.
			cfg.AvailableCLIUpdate = ""
			_ = config.Save(cfg)

			if promptErr != nil || !confirmed {
				cmd.PrintErrf("Run %q to update manually.\n", updateShellCmd)
				return nil
			}

			var runErr error
			switch runtime.GOOS {
			case "windows":
				c := exec.Command("winget", "upgrade", "WendyLabs.Wendy")
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
				runErr = c.Run()
			case "darwin":
				for _, brewArgs := range [][]string{{"update"}, {"install", "wendy"}} {
					c := exec.Command("brew", brewArgs...)
					c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
					if runErr = c.Run(); runErr != nil {
						break
					}
				}
			default:
				// Pipe the installer script directly into bash without shell interpolation.
				curl := exec.Command("curl", "-fsSL", "https://install.wendy.sh/cli.sh")
				bash := exec.Command("bash")
				curl.Stderr = os.Stderr
				bash.Stdout, bash.Stderr = os.Stdout, os.Stderr
				if bash.Stdin, runErr = curl.StdoutPipe(); runErr == nil {
					if runErr = curl.Start(); runErr == nil {
						if runErr = bash.Start(); runErr == nil {
							_ = curl.Wait()
							runErr = bash.Wait()
						}
					}
				}
			}
			if runErr != nil {
				return fmt.Errorf("update failed: %w", runErr)
			}
			return nil
		},
	}

	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	root.PersistentFlags().StringVar(&deviceFlag, "device", "", "Target device hostname")

	root.AddGroup(
		&cobra.Group{ID: "project", Title: "Project Commands:"},
		&cobra.Group{ID: "cloud", Title: "Manage Your Cloud:"},
		&cobra.Group{ID: "devices", Title: "Manage Your Devices:"},
		&cobra.Group{ID: "misc", Title: "Misc.:"},
	)

	// Project Commands
	runCmd := newRunCmd()
	runCmd.GroupID = "project"
	watchCmd := newWatchCmd()
	watchCmd.GroupID = "project"
	buildCmd := newBuildCmd()
	buildCmd.GroupID = "project"
	initCmd := newInitCmd()
	initCmd.GroupID = "project"
	projectCmd := newProjectCmd()
	projectCmd.GroupID = "project"
	jsonCmd := newJSONCmd()
	jsonCmd.GroupID = "project"

	// Cloud Commands
	authCmd := newAuthCmd()
	authCmd.GroupID = "cloud"
	cloudCmd := newCloudCmd()
	cloudCmd.GroupID = "cloud"

	// Device Commands
	deviceCmd := newDeviceCmd()
	deviceCmd.GroupID = "devices"
	discoverCmd := newDiscoverCmd()
	discoverCmd.GroupID = "devices"
	osCmd := newOSCmd()
	osCmd.GroupID = "devices"
	// Misc Commands
	cacheCmd := newCacheCmd()
	cacheCmd.GroupID = "misc"
	infoCmd := newInfoCmd()
	infoCmd.GroupID = "misc"
	analyticsCmd := newAnalyticsCmd()
	analyticsCmd.GroupID = "misc"
	utilsCmd := newUtilsCmd()
	utilsCmd.GroupID = "misc"
	tourCmd := newTourCmd()
	tourCmd.GroupID = "misc"
	mcpCmd := newMCPCmd()
	mcpCmd.GroupID = "misc"
	completionCmd := newCompletionCmd()
	completionCmd.GroupID = "misc"

	// Hidden command used by a subprocess to test CoreBluetooth access.
	// The main process spawns a child process that runs this command so
	// the child gets a fresh Obj-C runtime and can safely probe
	// CoreBluetooth without risking SIGABRT in the long-lived parent.
	bleCheckCmd := &cobra.Command{
		Use:    "__ble-check",
		Hidden: true,
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(discovery.RunBLECheck())
		},
	}

	var bmapDevice, bmapFile, bmapSource string
	var bmapWriters int
	bmapWriteCmd := &cobra.Command{
		Use:    "__bmap-write",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bmapSource != "" {
				return runBmapWriteSeekable(bmapDevice, bmapFile, bmapSource, bmapWriters, cmd.OutOrStdout())
			}
			return runBmapWrite(bmapDevice, bmapFile, cmd.InOrStdin())
		},
	}
	bmapWriteCmd.Flags().StringVar(&bmapDevice, "device", "", "Raw device path to write")
	bmapWriteCmd.Flags().StringVar(&bmapFile, "bmap", "", "Path to the .bmap file")
	bmapWriteCmd.Flags().StringVar(&bmapSource, "source", "", "Path to the seekable .img.zst source")
	bmapWriteCmd.Flags().IntVar(&bmapWriters, "writers", 0, "Concurrent writer goroutines (0 = sequential default)")

	root.AddCommand(
		bleCheckCmd,
		bmapWriteCmd,
		runCmd,
		watchCmd,
		buildCmd,
		initCmd,
		projectCmd,
		jsonCmd,
		authCmd,
		cloudCmd,
		deviceCmd,
		discoverCmd,
		osCmd,
		cacheCmd,
		infoCmd,
		analyticsCmd,
		utilsCmd,
		tourCmd,
		mcpCmd,
		completionCmd,
	)

	root.SetHelpCommandGroupID("misc")
	root.SetCompletionCommandGroupID("misc")

	root.Version = version.Version
	return root
}
