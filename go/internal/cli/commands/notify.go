package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage Wendy Cloud push notifications",
	}

	cmd.AddCommand(
		newNotifyStartCmd(),
		newNotifyStopCmd(),
		newNotifyStatusCmd(),
		newNotifyDaemonCmd(),
	)
	return cmd
}

func newNotifyStartCmd() *cobra.Command {
	var cloudGRPC string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Install and start the Wendy notification daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := pickAuthEntry(cloudGRPC)
			if err != nil {
				return err
			}

			if err := installNotifyService(auth.CloudGRPC); err != nil {
				return fmt.Errorf("installing notification daemon: %w", err)
			}

			logPath, err := notifyLogPath()
			if err != nil {
				logPath = "(log path unavailable)"
			}
			cmd.Printf("Wendy notification daemon started.\nLogs: %s\n", logPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (required when multiple auth sessions exist)")
	return cmd
}

func newNotifyStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop and uninstall the Wendy notification daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := uninstallNotifyService(); err != nil {
				return fmt.Errorf("uninstalling notification daemon: %w", err)
			}
			cmd.Println("Wendy notification daemon stopped.")
			return nil
		},
	}
}

func newNotifyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the status of the Wendy notification daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := notifyServiceStatus()
			if err != nil {
				return err
			}
			logPath, err := notifyLogPath()
			if err != nil {
				logPath = "(log path unavailable)"
			}
			cmd.Printf("Notification daemon: %s\nLogs: %s\n", status, logPath)
			return nil
		},
	}
}

func newNotifyDaemonCmd() *cobra.Command {
	var cloudGRPC string

	cmd := &cobra.Command{
		Use:    "__daemon",
		Short:  "Run the notification daemon (internal use by OS service manager)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			auth, err := pickAuthEntry(cloudGRPC)
			if err != nil {
				return err
			}
			return runNotifyDaemon(cmd.Context(), auth, defaultNotifyDaemonDeps())
		},
	}
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (required when multiple auth sessions exist)")
	return cmd
}
