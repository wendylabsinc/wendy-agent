package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// pingSession is the slice of datagramSession that runPingLoop needs.
type pingSession interface {
	sendEcho(req *cloudpb.IcmpEchoRequest) error
	recv() (*cloudpb.TunnelData, error)
}

type pingStats struct {
	Sent, Received int
	Min, Avg, Max  time.Duration
}

func newCloudPingCmd() *cobra.Command {
	var cloudGRPC, deviceName, brokerURL string
	var count int
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "ping [--device <name>]",
		Short: "Ping a cloud-enrolled device through the tunnel broker",
		Long:  "Sends echo requests over a Wendy Cloud datagram tunnel session. A reply proves the device's agent is up and measures true end-to-end round-trip time. No ICMP sockets or privileges are involved.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudPingCommand(cmd.Context(), cloudGRPC, deviceName, brokerURL, count, interval)
		},
	}
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is set via 'wendy auth use')")
	cmd.Flags().StringVar(&deviceName, "device", "", "Device name (skips interactive picker)")
	cmd.Flags().StringVar(&brokerURL, "broker-url", os.Getenv("WENDY_BROKER_URL"), "Tunnel broker host:port")
	cmd.Flags().IntVarP(&count, "count", "c", 0, "Stop after this many echoes (0 = until Ctrl+C)")
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "Time between echoes")
	return cmd
}

func cloudPingCommand(ctx context.Context, cloudGRPC, deviceName, brokerURL string, count int, interval time.Duration) error {
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}
	asset, err := pickCloudDevice(ctx, auth, deviceName, brokerURL)
	if err != nil {
		return err
	}
	brokerConn, err := dialCloudBroker(auth, brokerURL)
	if err != nil {
		return err
	}
	defer brokerConn.Close()

	session, err := openDatagramSession(ctx, brokerConn, auth, asset.GetId())
	if err != nil {
		return datagramOpenError(err, asset.GetName())
	}
	defer session.close()

	cliLogln("PING %s (asset %d) via Wendy Cloud", asset.GetName(), asset.GetId())
	stats := runPingLoop(ctx, session, asset.GetName(), count, interval, os.Stdout)

	loss := 0.0
	if stats.Sent > 0 {
		loss = 100 * float64(stats.Sent-stats.Received) / float64(stats.Sent)
	}
	cliLogln("--- %s ping statistics ---", asset.GetName())
	cliLogln("%d sent, %d received, %.0f%% loss", stats.Sent, stats.Received, loss)
	if stats.Received > 0 {
		cliLogln("rtt min/avg/max = %s/%s/%s", stats.Min.Round(time.Microsecond),
			stats.Avg.Round(time.Microsecond), stats.Max.Round(time.Microsecond))
	}
	if stats.Received == 0 && stats.Sent > 0 {
		return fmt.Errorf("no replies from %s: the device may be offline or need a WendyOS update for ping support", asset.GetName())
	}
	return nil
}

// runPingLoop sends one echo per interval (count of 0 = unbounded), prints
// each matched reply to out, and returns aggregate stats once every echo has
// either been answered or the ctx ends. Replies are matched to a pending
// request by sequence number; only replies carrying this process's identifier
// are considered (so two concurrent `wendy cloud ping` runs against the same
// device don't cross-count each other's replies).
func runPingLoop(ctx context.Context, session pingSession, target string, count int, interval time.Duration, out io.Writer) pingStats {
	identifier := uint32(os.Getpid() & 0xFFFF)
	type sentEcho struct{ originate time.Time }
	var (
		stats   pingStats
		pending = map[uint32]sentEcho{} // keyed by sequence
		total   time.Duration
		done    = make(chan struct{})
		replies = make(chan *cloudpb.IcmpEchoReply, 16)
	)

	go func() {
		defer close(done)
		for {
			msg, err := session.recv()
			if err != nil {
				return
			}
			if r := msg.GetIcmpReply(); r != nil && r.GetIdentifier() == identifier {
				select {
				case replies <- r:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	seq := uint32(0)
	sendOne := func() bool {
		if count > 0 && int(seq) >= count {
			return false
		}
		seq++
		now := time.Now()
		pending[seq] = sentEcho{originate: now}
		stats.Sent++
		if err := session.sendEcho(&cloudpb.IcmpEchoRequest{
			Identifier:      identifier,
			Sequence:        seq,
			Payload:         []byte("wendy-ping"),
			OriginateUnixNs: uint64(now.UnixNano()),
		}); err != nil {
			return false
		}
		return true
	}
	if !sendOne() {
		return stats
	}

	// Grace window after the last send so the final reply(ies) can still
	// arrive; started once all echoes have been dispatched.
	graceTimer := time.NewTimer(24 * time.Hour)
	defer graceTimer.Stop()

	finish := func() pingStats {
		stats.Avg = pingAvg(total, stats.Received)
		return stats
	}

	for {
		select {
		case r := <-replies:
			if s, ok := pending[r.GetSequence()]; ok {
				delete(pending, r.GetSequence())
				rtt := time.Since(s.originate)
				stats.Received++
				total += rtt
				if stats.Min == 0 || rtt < stats.Min {
					stats.Min = rtt
				}
				if rtt > stats.Max {
					stats.Max = rtt
				}
				fmt.Fprintf(out, "reply from %s: seq=%d time=%s\n", target, r.GetSequence(), rtt.Round(time.Microsecond))
			}
			if count > 0 && len(pending) == 0 && stats.Sent >= count {
				return finish()
			}
		case <-ticker.C:
			if !sendOne() {
				// All echoes sent (or the session died); give stragglers one
				// interval to answer, with a 500ms floor for short intervals.
				grace := interval
				if grace < 500*time.Millisecond {
					grace = 500 * time.Millisecond
				}
				graceTimer.Reset(grace)
			}
		case <-graceTimer.C:
			return finish()
		case <-ctx.Done():
			return finish()
		case <-done:
			return finish()
		}
	}
}

func pingAvg(total time.Duration, n int) time.Duration {
	if n == 0 {
		return 0
	}
	return total / time.Duration(n)
}
