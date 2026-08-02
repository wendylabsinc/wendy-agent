package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

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

	// Every subcommand here drives launchd and ~/Library paths, so none of them
	// can work off-darwin. root.go also hides the group elsewhere, but Hidden
	// only suppresses help/completion — this is what actually stops execution
	// before it fails confusingly on a missing launchctl.
	//
	// Deliberately PreRunE on each subcommand rather than PersistentPreRunE on
	// the group: cobra runs only the *nearest* PersistentPreRunE in the chain, so
	// a group-level one would shadow the root command's (config load, analytics
	// init, provider setup). PreRunE is a separate chain and composes cleanly.
	// None of these subcommands define their own PreRunE.
	for _, sub := range cmd.Commands() {
		sub.PreRunE = func(cmd *cobra.Command, args []string) error {
			return sandboxPlatformGuard(runtime.GOOS)
		}
	}
	return cmd
}

func sandboxPlatformGuard(goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("wendy sandbox is only supported on macOS")
	}
	return nil
}

func runSandboxInstall(ctx context.Context, cmd *cobra.Command) error {
	// Keep the resolved path: the plist invokes node by absolute path so launchd
	// doesn't have to find it on a PATH that never includes nvm/fnm/asdf/volta.
	nodePath, err := exec.LookPath("node")
	if err != nil {
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

	// Unload before touching the install directory (ignore errors — it may not be
	// loaded yet). `npm ci` wipes node_modules wholesale, so leaving an old
	// KeepAlive-managed agent running through the download would crash-loop it for
	// the whole install.
	_ = unloadSandboxLaunchAgent(ctx)

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
		Label: sandboxLaunchAgentLabel, NodePath: nodePath, WorkDir: installDir, LogPath: logPath,
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
	// 0600, not 0644: this plist embeds the admin password in cleartext. launchd
	// reads it fine at 0600 since it runs as the same user.
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", plistPath, err)
	}

	if err := loadSandboxLaunchAgent(ctx, plistPath); err != nil {
		return err
	}

	if sandboxWaitForPort(ctx, "8787", 3*time.Second) {
		cmd.Println("control-plane installed and running on http://localhost:8787")
	} else {
		cmd.Println("control-plane installed and starting on http://localhost:8787 (not answering yet)")
	}
	cmd.Println("Check status any time with: wendy sandbox status")
	return nil
}

// sandboxWaitForPort polls the port until it answers or timeout elapses.
func sandboxWaitForPort(ctx context.Context, port string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if sandboxPortIsListening(ctx, port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
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
	// Check status first: bootout on an already-unloaded job exits 3 ("No such
	// process"), so this keeps a repeated `stop` idempotent, mirroring `start`.
	running, err := sandboxLaunchAgentStatus(ctx)
	if err != nil {
		return err
	}
	if !running {
		cmd.Println("control-plane already stopped.")
		return nil
	}
	if err := unloadSandboxLaunchAgent(ctx); err != nil {
		return err
	}
	cmd.Println("control-plane stopped.")
	return nil
}

// runSandboxStatus distinguishes three states, because "not loaded in launchd"
// alone means neither "not installed" (the plist may be on disk, just booted
// out by `stop`) nor "healthy" (KeepAlive keeps a crash-looping job loaded).
func runSandboxStatus(ctx context.Context, cmd *cobra.Command) error {
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		cmd.Println("control-plane is not installed; run: wendy sandbox install")
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", plistPath, err)
	}

	loaded, err := sandboxLaunchAgentStatus(ctx)
	if err != nil {
		return err
	}
	if !loaded {
		cmd.Println("control-plane is installed but stopped; run: wendy sandbox start")
		return nil
	}

	if sandboxPortIsListening(ctx, "8787") {
		cmd.Printf("control-plane is installed and running (%s)\n", sandboxLaunchdTarget())
	} else {
		cmd.Println("control-plane is loaded but not responding on port 8787 — it may be crash-looping; check ~/Library/Logs/wendy-sandbox-control-plane.log")
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
