package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/cli/clouddefaults"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func newCloudTunnelCmd() *cobra.Command {
	var cloudGRPC string
	var deviceName string
	var brokerURL string

	cmd := &cobra.Command{
		Use:   "tunnel <port-forward>",
		Short: "Forward a local TCP or UDP port to a port on a cloud-enrolled device",
		Long: "Listens on a local port and forwards each connection through the Wendy Cloud tunnel broker. " +
			"Use <port>, <local-port>:<remote-port>, or <local-port>:<remote-host>:<remote-port>. " +
			"Remote hosts are resolved and reached by the target device. Append /udp (or /tcp) to select the protocol; remote-host forwarding is TCP-only.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			localPort, remoteHost, remotePort, udp, err := parseTunnelArg(args[0])
			if err != nil {
				return err
			}
			return cloudTunnelCommand(cmd.Context(), cloudGRPC, effectiveDeviceName(deviceName), brokerURL, localPort, remoteHost, remotePort, udp)
		},
	}

	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is set via 'wendy auth use')")
	cmd.Flags().StringVar(&deviceName, "device", "", "Device name (skips interactive picker)")
	cmd.Flags().StringVar(&brokerURL, "broker-url", os.Getenv("WENDY_BROKER_URL"), "Tunnel broker host:port (default: cloud :443 endpoint, otherwise <cloud-host>:50052)")

	return cmd
}

// parseTunnelArg parses "port", "localPort:remotePort", or
// "localPort:remoteHost:remotePort", with an optional docker-style "/udp"
// (or explicit "/tcp") protocol suffix. IPv6 remote hosts must be bracketed.
func parseTunnelArg(arg string) (localPort uint32, remoteHost string, remotePort uint32, udp bool, err error) {
	if i := strings.LastIndex(arg, "/"); i >= 0 {
		switch strings.ToLower(arg[i+1:]) {
		case "udp":
			udp = true
		case "tcp":
		default:
			return 0, "", 0, false, fmt.Errorf("unknown protocol %q (use tcp or udp)", arg[i+1:])
		}
		arg = arg[:i]
	}
	parse := func(s string) (uint32, error) {
		n, e := strconv.ParseUint(s, 10, 32)
		if e != nil || n == 0 || n > 65535 {
			return 0, fmt.Errorf("invalid port %q", s)
		}
		return uint32(n), nil
	}

	separator := strings.IndexByte(arg, ':')
	if separator < 0 {
		p, e := parse(arg)
		return p, "localhost", p, udp, e
	}

	lp, e := parse(arg[:separator])
	if e != nil {
		return 0, "", 0, false, e
	}
	remote := arg[separator+1:]
	if !strings.Contains(remote, ":") {
		rp, parseErr := parse(remote)
		return lp, "localhost", rp, udp, parseErr
	}

	host, portString, splitErr := net.SplitHostPort(remote)
	if splitErr != nil {
		return 0, "", 0, false, fmt.Errorf("invalid remote target %q: %w", remote, splitErr)
	}
	if host == "" {
		return 0, "", 0, false, fmt.Errorf("invalid remote host %q", host)
	}
	rp, e := parse(portString)
	if e != nil {
		return 0, "", 0, false, e
	}
	if udp && host != "localhost" {
		return 0, "", 0, false, fmt.Errorf("remote host forwarding is only supported for TCP tunnels")
	}
	return lp, host, rp, udp, nil
}

func cloudTunnelCommand(ctx context.Context, cloudGRPC, deviceName, brokerURL string, localPort uint32, remoteHost string, remotePort uint32, udp bool) error {
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}

	cliLogln("Fetching device list from cloud...")
	asset, err := pickCloudDevice(ctx, auth, deviceName, brokerURL)
	if err != nil {
		return err
	}

	brokerConn, err := clouddefaults.DialBroker(auth, brokerURL)
	if err != nil {
		return err
	}
	defer brokerConn.Close()

	if udp {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(localPort)})
		if err != nil {
			return fmt.Errorf("listening on udp 127.0.0.1:%d: %w", localPort, err)
		}
		defer pc.Close()
		session, err := openDatagramSession(ctx, brokerConn, auth, asset.GetId())
		if err != nil {
			return datagramOpenError(err, asset.GetName())
		}
		defer session.close()
		cliSuccess("Forwarding udp 127.0.0.1:%d → %s:%d (via cloud)", localPort, asset.GetName(), remotePort)
		cliLogln("Press Ctrl+C to stop.")
		go func() { <-ctx.Done(); pc.Close() }()
		udpErr := serveUDPForward(ctx, pc, session, remotePort, udpFlowIdleTimeout)
		if ctx.Err() != nil {
			return nil
		}
		return datagramOpenError(udpErr, asset.GetName())
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listenAddr, err)
	}
	defer ln.Close()

	if remoteHost == "localhost" {
		cliSuccess("Forwarding %s → %s:%d (via cloud)", listenAddr, asset.GetName(), remotePort)
	} else {
		cliSuccess("Forwarding %s → %s through %s (via cloud)", listenAddr, net.JoinHostPort(remoteHost, strconv.Itoa(int(remotePort))), asset.GetName())
	}
	cliLogln("Press Ctrl+C to stop.")

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		tcpConn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting connection: %w", err)
		}
		go serveTunnelConn(ctx, tcpConn, brokerConn, auth, asset.GetId(), remoteHost, remotePort)
	}
}

func serveTunnelConn(ctx context.Context, tcpConn net.Conn, brokerConn *grpc.ClientConn, auth *config.AuthConfig, assetID int32, remoteHost string, remotePort uint32) {
	defer tcpConn.Close()

	tunnelConn, err := openBrokerTunnelToHost(ctx, brokerConn, auth, assetID, remoteHost, remotePort)
	if err != nil {
		return
	}
	defer tunnelConn.Close()

	done := make(chan struct{}, 2)
	relay := func(dst io.Writer, src io.Reader) {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(dst, src)
	}
	go relay(tunnelConn, tcpConn)
	go relay(tcpConn, tunnelConn)
	<-done
	<-done // wait for both directions before closing connections
}
