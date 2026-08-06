package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newDevicePingCmd() *cobra.Command {
	var count int
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping the selected LAN device through its agent connection",
		Long:  "Sends echo requests over the device's DatagramTunnel session. A reply proves the device's agent is up and measures true end-to-end round-trip time. No ICMP sockets or privileges are involved.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return devicePingCommand(ctx, count, interval)
		},
	}
	cmd.Flags().IntVarP(&count, "count", "c", 0, "Stop after this many echoes (0 = until Ctrl+C)")
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "Time between echoes")
	return cmd
}

func devicePingCommand(ctx context.Context, count int, interval time.Duration) error {
	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return err
	}
	defer target.Close()
	if target.Agent == nil || target.Agent.TunnelService == nil {
		return fmt.Errorf("device ping requires a WendyOS LAN agent")
	}

	session, err := openDeviceDatagramSession(ctx, target.Agent.TunnelService)
	if err != nil {
		return datagramOpenError(err, target.Agent.Host)
	}
	defer session.close()

	cliLogln("PING %s", target.Agent.Host)
	stats := runPingLoop(ctx, session, target.Agent.Host, count, interval, os.Stdout)

	loss := 0.0
	if stats.Sent > 0 {
		loss = 100 * float64(stats.Sent-stats.Received) / float64(stats.Sent)
	}
	cliLogln("--- %s ping statistics ---", target.Agent.Host)
	cliLogln("%d sent, %d received, %.0f%% loss", stats.Sent, stats.Received, loss)
	if stats.Received > 0 {
		cliLogln("rtt min/avg/max = %s/%s/%s", stats.Min.Round(time.Microsecond),
			stats.Avg.Round(time.Microsecond), stats.Max.Round(time.Microsecond))
	}
	if stats.Received == 0 && stats.Sent > 0 {
		if stats.Err != nil {
			// A genuine transport error (PermissionDenied, Unauthenticated,
			// mesh-disabled, ...) ended the recv loop — surface it instead of
			// the generic hint. datagramOpenError still folds
			// DeadlineExceeded/Unavailable/Unimplemented into that same hint.
			return datagramOpenError(stats.Err, target.Agent.Host)
		}
		return fmt.Errorf("no replies from %s: the device may be offline or need a WendyOS update for ping support", target.Agent.Host)
	}
	return nil
}
