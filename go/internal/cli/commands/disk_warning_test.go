package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestHighDiskUsageThresholdAndFullestPartition(t *testing.T) {
	parts := []*agentpb.DiskPartition{
		{Mountpoint: "/", UsedBytes: 850, TotalBytes: 1000},
		{Mountpoint: "/data", UsedBytes: 960, TotalBytes: 1000},
		{Mountpoint: "/boot", UsedBytes: 9, TotalBytes: 10},
	}
	alert, ok := highDiskUsage(parts, nil, nil)
	if !ok || alert.Mountpoint != "/data" || alert.UsedPercent != 96 {
		t.Fatalf("alert = %+v, ok=%v", alert, ok)
	}
	if !strings.Contains(diskUsageWarningText(alert), "wendy device cache prune") {
		t.Fatal("warning does not include remediation command")
	}
}

func TestHighDiskUsageDoesNotWarnAtExactlyThreshold(t *testing.T) {
	used, total := int64(85), int64(100)
	if alert, ok := highDiskUsage(nil, &used, &total); ok {
		t.Fatalf("unexpected alert: %+v", alert)
	}
}
