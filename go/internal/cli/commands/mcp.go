package commands

import (
	"fmt"
	"net"
	"os"

	"github.com/spf13/cobra"
	wendymcp "github.com/wendylabsinc/wendy/go/internal/cli/mcp"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server for AI assistant access to wendy devices",
	}
	cmd.AddCommand(newMCPServeCmd())
	cmd.AddCommand(newMCPSetupCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	var deviceFlag string
	var httpMode bool
	var httpAddr string
	var httpToken string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server (stdio by default, or --http for streamable HTTP)",
		Long:  "Start a Model Context Protocol server that exposes wendy device tools over stdio.\nConfigure your AI tool to run: wendy mcp serve\nOr run 'wendy mcp setup' to configure automatically.\nUse --http to serve over streamable HTTP instead of stdio.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			srv := wendymcp.New(cfg, connectWithAutoTLS)
			address := deviceFlag
			if address == "" {
				address = cfg.DefaultDevice
			}
			if address != "" {
				if _, _, err := net.SplitHostPort(address); err != nil {
					address = hostPort(address, defaultAgentPort)
				}
				if err := srv.ConnectTo(ctx, address); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not connect to %s: %v\n", address, err)
				}
			}
			if httpMode {
				fmt.Fprintf(os.Stderr, "wendy mcp: serving over HTTP at %s (endpoint /mcp)\n", httpAddr)
				return srv.StartHTTP(ctx, httpAddr, httpToken)
			}
			return srv.Start(ctx)
		},
	}
	cmd.Flags().StringVarP(&deviceFlag, "device", "d", "", "Device name or IP:port to connect on startup")
	cmd.Flags().BoolVar(&httpMode, "http", false, "Serve over streamable HTTP instead of stdio")
	cmd.Flags().StringVar(&httpAddr, "addr", "127.0.0.1:7777", "HTTP listen address (with --http)")
	cmd.Flags().StringVar(&httpToken, "token", "", "Require this bearer token for HTTP requests (with --http)")
	return cmd
}
