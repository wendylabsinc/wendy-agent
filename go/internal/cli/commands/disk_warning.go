package commands

import (
	"fmt"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const diskWarningThresholdPercent int64 = 85

type diskUsageAlert struct {
	Mountpoint  string
	UsedPercent int64
}

// highDiskUsage returns the fullest partition above the warning threshold.
// Partition data takes precedence over the legacy root-only fields.
func highDiskUsage(partitions []*agentpb.DiskPartition, legacyUsed, legacyTotal *int64) (diskUsageAlert, bool) {
	var (
		best      diskUsageAlert
		bestRatio float64
	)
	for _, p := range partitions {
		if p == nil || p.GetTotalBytes() <= 0 || p.GetUsedBytes() < 0 || p.GetUsedBytes() > p.GetTotalBytes() {
			continue
		}
		ratio := float64(p.GetUsedBytes()) / float64(p.GetTotalBytes())
		if ratio > float64(diskWarningThresholdPercent)/100 && ratio > bestRatio {
			bestRatio = ratio
			best = diskUsageAlert{Mountpoint: p.GetMountpoint(), UsedPercent: int64(ratio * 100)}
		}
	}
	if bestRatio > 0 {
		return best, true
	}
	if len(partitions) == 0 && legacyUsed != nil && legacyTotal != nil && *legacyTotal > 0 && *legacyUsed >= 0 {
		ratio := float64(*legacyUsed) / float64(*legacyTotal)
		if ratio > float64(diskWarningThresholdPercent)/100 {
			return diskUsageAlert{Mountpoint: "/", UsedPercent: int64(ratio * 100)}, true
		}
	}
	return diskUsageAlert{}, false
}

func diskUsageWarningText(alert diskUsageAlert) string {
	mountpoint := alert.Mountpoint
	if mountpoint == "" {
		mountpoint = "the device disk"
	} else {
		mountpoint = fmt.Sprintf("disk %s", mountpoint)
	}
	return fmt.Sprintf("%s is %d%% full. Free cached container data with 'wendy device cache prune'.", mountpoint, alert.UsedPercent)
}

func printRunDiskUsageWarning(resp *agentpb.GetAgentVersionResponse) {
	if resp == nil {
		return
	}
	if alert, ok := highDiskUsage(resp.GetPartitions(), resp.DiskUsedBytes, resp.DiskTotalBytes); ok {
		cliNotice("Warning: %s", diskUsageWarningText(alert))
	}
}
