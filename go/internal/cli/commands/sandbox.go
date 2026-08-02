package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage the local Wendy Sandbox control plane",
		Long: "Install and run the local control-plane service that Wendy Sandbox\n" +
			"(the native macOS app) uses for session containers, terminal, and the sim viewer.",
	}
	var purge bool
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Unload and remove the local control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSandboxUninstall(context.Background(), cmd, purge)
		},
	}
	uninstallCmd.Flags().BoolVar(&purge, "purge", false, "also remove the cached install directory")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install and start the local control plane (safe to re-run)",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxInstall(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "start",
			Short: "Start the local control plane",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStart(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the local control plane",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStop(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the local control plane is running",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStatus(context.Background(), cmd)
			},
		},
		uninstallCmd,
	)
	return cmd
}

func runSandboxInstall(ctx context.Context, cmd *cobra.Command) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("wendy sandbox is only supported on macOS")
	}
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node is required but not found on PATH; run: brew install node")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm is required but not found on PATH; run: brew install node")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	installDir := filepath.Join(home, ".wendy", "sandbox", "control-plane")

	cmd.Println("Fetching latest control-plane release…")
	rel, err := fetchControlPlaneRelease(ctx)
	if err != nil {
		return err
	}
	assetURL, err := findControlPlaneAsset(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", installDir, err)
	}
	if err := downloadAndExtractControlPlaneRelease(ctx, assetURL, installDir); err != nil {
		return err
	}

	cmd.Println("Installing dependencies…")
	npmCmd := exec.CommandContext(ctx, "npm", "ci", "--omit=dev")
	npmCmd.Dir = installDir
	npmCmd.Stdout, npmCmd.Stderr = os.Stdout, os.Stderr
	if err := npmCmd.Run(); err != nil {
		return fmt.Errorf("npm ci --omit=dev in %s: %w", installDir, err)
	}

	creds, err := readOrGenerateSandboxCredentials()
	if err != nil {
		return err
	}

	dataDir := filepath.Join(home, "Library", "Application Support", "WendySandboxNative", "control-plane-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dataDir, err)
	}
	logPath := filepath.Join(home, "Library", "Logs", "wendy-sandbox-control-plane.log")

	plist, err := renderSandboxPlist(sandboxPlistParams{
		Label: sandboxLaunchAgentLabel, WorkDir: installDir, LogPath: logPath,
		Port: "8787", AdminUser: creds.User, AdminPassword: creds.Password, DataDir: dataDir,
	})
	if err != nil {
		return err
	}
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(plistPath), err)
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", plistPath, err)
	}

	// Unload first (ignore errors — it may not be loaded yet) so a re-run of
	// install picks up a new plist/version instead of launchd keeping the old one.
	_ = unloadSandboxLaunchAgent(ctx)
	if err := loadSandboxLaunchAgent(ctx, plistPath); err != nil {
		return err
	}

	cmd.Println("control-plane installed and running on http://localhost:8787")
	cmd.Println("Check status any time with: wendy sandbox status")
	return nil
}

func runSandboxStart(ctx context.Context, cmd *cobra.Command) error {
	// The plist has KeepAlive: true, so the only correct way to (re)start a
	// stopped agent is to bootstrap it back into launchd — `kickstart` only
	// affects an already-loaded job and does nothing for one that was
	// bootout'd. Check status first so re-running start on an already-running
	// service is a friendly no-op instead of a "already bootstrapped" error
	// from launchctl.
	running, err := sandboxLaunchAgentStatus(ctx)
	if err != nil {
		return err
	}
	if running {
		cmd.Println("control-plane already running.")
		return nil
	}
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if err := loadSandboxLaunchAgent(ctx, plistPath); err != nil {
		return fmt.Errorf("%w (not installed yet? run: wendy sandbox install)", err)
	}
	cmd.Println("control-plane started.")
	return nil
}

func runSandboxStop(ctx context.Context, cmd *cobra.Command) error {
	// With KeepAlive: true in the plist, `launchctl kill` just gets the job
	// respawned by launchd — the only way to actually stop it is to take it
	// out of launchd's control entirely via bootout (unloadSandboxLaunchAgent).
	if err := unloadSandboxLaunchAgent(ctx); err != nil {
		return err
	}
	cmd.Println("control-plane stopped.")
	return nil
}

func runSandboxStatus(ctx context.Context, cmd *cobra.Command) error {
	running, err := sandboxLaunchAgentStatus(ctx)
	if err != nil {
		return err
	}
	if running {
		cmd.Printf("control-plane is installed and loaded (%s)\n", sandboxLaunchdTarget())
	} else {
		cmd.Println("control-plane is not installed; run: wendy sandbox install")
	}
	return nil
}

func runSandboxUninstall(ctx context.Context, cmd *cobra.Command, purge bool) error {
	if err := unloadSandboxLaunchAgent(ctx); err != nil {
		cmd.PrintErrln("warning:", err)
	}
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", plistPath, err)
	}
	if purge {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(home, ".wendy", "sandbox")); err != nil {
			return fmt.Errorf("removing install directory: %w", err)
		}
	}
	cmd.Println("control-plane uninstalled.")
	return nil
}
