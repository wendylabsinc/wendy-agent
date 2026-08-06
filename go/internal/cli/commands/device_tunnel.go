package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func newDeviceTunnelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel <local-port>:<remote-port>",
		Short: "Forward a local TCP port through the selected LAN agent",
		Long:  "Listens on local loopback and forwards each connection through the selected LAN agent to a TCP port on the device's loopback interface.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, cloud := cloudDeviceConfigFromContext(cmd.Context()); cloud {
				return fmt.Errorf("use 'wendy cloud tunnel' for cloud-connected devices")
			}
			localPort, remotePort, _, err := parseTunnelArg(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return deviceTunnelCommand(ctx, localPort, remotePort)
		},
	}
}

func deviceTunnelCommand(ctx context.Context, localPort, remotePort uint32) error {
	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return err
	}
	defer target.Close()
	if target.Agent == nil || target.Agent.TunnelService == nil {
		return fmt.Errorf("device tunnel requires a WendyOS LAN agent")
	}

	listener, err := listenDeviceTunnel(localPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	return serveDeviceTunnel(ctx, target.Agent, listener, remotePort)
}

func listenDeviceTunnel(localPort uint32) (net.Listener, error) {
	listenAddr := net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(localPort), 10))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", listenAddr, err)
	}
	return listener, nil
}

func serveDeviceTunnel(ctx context.Context, agent *grpcclient.AgentConnection, listener net.Listener, remotePort uint32) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	cliSuccess("Forwarding %s → %s:%d (via LAN agent)", listener.Addr(), agent.Host, remotePort)
	cliLogln("Press Ctrl+C to stop.")
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting connection: %w", err)
		}
		go serveDeviceTunnelConn(ctx, conn, agent.TunnelService, remotePort)
	}
}

func serveDeviceTunnelConn(ctx context.Context, tcpConn net.Conn, client agentpbv2.WendyTunnelServiceClient, remotePort uint32) {
	defer tcpConn.Close()

	tunnelConn, err := openDeviceTunnel(ctx, client, remotePort)
	if err != nil {
		cliLogln("Tunnel connection failed: %v", err)
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
	// Closing both sides after the first direction finishes unblocks the other
	// io.Copy even when the peer does not perform a graceful half-close.
	_ = tunnelConn.Close()
	_ = tcpConn.Close()
	<-done
}

func openDeviceTunnel(ctx context.Context, client agentpbv2.WendyTunnelServiceClient, remotePort uint32) (net.Conn, error) {
	stream, err := client.Tunnel(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening device tunnel stream: %w", err)
	}
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{
		Content: &agentpbv2.DeviceTunnelRequest_Open{
			Open: &agentpbv2.DeviceTunnelOpen{Port: remotePort},
		},
	}); err != nil {
		return nil, fmt.Errorf("sending device tunnel open: %w", err)
	}

	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if len(msg.Payload) > 0 {
				if _, err := remote.Write(msg.Payload); err != nil {
					return
				}
			}
			if msg.HalfClose {
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 256*1024)
		for {
			n, readErr := remote.Read(buf)
			if n > 0 {
				payload := append([]byte(nil), buf[:n]...)
				if err := stream.Send(&agentpbv2.DeviceTunnelRequest{
					Content: &agentpbv2.DeviceTunnelRequest_Data{
						Data: &agentpbv2.DeviceTunnelData{Payload: payload},
					},
				}); err != nil {
					return
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					_ = stream.Send(&agentpbv2.DeviceTunnelRequest{
						Content: &agentpbv2.DeviceTunnelRequest_Data{
							Data: &agentpbv2.DeviceTunnelData{HalfClose: true},
						},
					})
				}
				_ = stream.CloseSend()
				return
			}
		}
	}()

	return local, nil
}
