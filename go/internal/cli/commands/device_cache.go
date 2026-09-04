package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type deviceCachePruneClient interface {
	PruneCache(context.Context, *agentpbv2.PruneCacheRequest, ...grpc.CallOption) (*agentpbv2.PruneCacheResponse, error)
}

func newDeviceCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage cached container data on the target device",
	}
	cmd.AddCommand(newDeviceCachePruneCmd())
	return cmd
}

func newDeviceCachePruneCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Release unused container layer and snapshot caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := connectToAgent(cmd.Context(), SuppressUpdateCheck())
			if err != nil {
				return err
			}
			defer conn.Close()
			return runDeviceCachePrune(cmd.Context(), conn, cmd.OutOrStdout(), dryRun, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report eligible cache data without releasing it")
	return cmd
}

func runDeviceCachePrune(ctx context.Context, conn *grpcclient.AgentConnection, out io.Writer, dryRun, jsonOut bool) error {
	client := agentpbv2.NewWendyContainerServiceClient(conn.Conn)
	return runDeviceCachePruneRPC(ctx, client, out, dryRun, jsonOut)
}

func runDeviceCachePruneRPC(ctx context.Context, client deviceCachePruneClient, out io.Writer, dryRun, jsonOut bool) error {
	resp, err := client.PruneCache(ctx, &agentpbv2.PruneCacheRequest{DryRun: dryRun})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return fmt.Errorf("this device agent does not support cache pruning; update it with 'wendy device update'")
		}
		return fmt.Errorf("pruning device cache: %w", err)
	}
	if jsonOut {
		data, err := json.MarshalIndent(map[string]any{
			"dryRun":            dryRun,
			"contentBlobs":      resp.GetContentBlobs(),
			"contentBytes":      resp.GetContentBytes(),
			"snapshots":         resp.GetSnapshots(),
			"snapshotBytes":     resp.GetSnapshotBytes(),
			"minimumAgeSeconds": resp.GetMinimumAgeSeconds(),
		}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}

	totalObjects := resp.GetContentBlobs() + resp.GetSnapshots()
	totalBytes := saturatingAdd(resp.GetContentBytes(), resp.GetSnapshotBytes())
	if totalObjects == 0 {
		_, err = fmt.Fprintf(out, "No cache entries older than %s are eligible for pruning.\n", formatCacheAge(resp.GetMinimumAgeSeconds()))
		return err
	}
	action := "Released"
	if dryRun {
		action = "Eligible"
	}
	if _, err = fmt.Fprintf(out, "%s: %s across %d layer blobs and %d snapshots.\n",
		action, formatBytes(int64(min(totalBytes, uint64(math.MaxInt64)))), resp.GetContentBlobs(), resp.GetSnapshots()); err != nil {
		return err
	}
	if !dryRun {
		_, err = fmt.Fprintln(out, "Containerd will reclaim unreachable data in the background; current images and active apps are preserved.")
	}
	return err
}

func saturatingAdd(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

func formatCacheAge(seconds uint64) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
