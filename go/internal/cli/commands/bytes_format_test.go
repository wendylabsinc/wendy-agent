package commands

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// When the agent reports the raw block-device size, the table gains a SIZE
// column (the provisioned partition size) and renames the filesystem-usable
// column to USABLE, so the ext4-overhead gap between them is legible.
func TestFormatPartitionTableWithSize(t *testing.T) {
	parts := []*agentpb.DiskPartition{
		{
			Mountpoint: "/",
			Filesystem: "ext4",
			Device:     "/dev/mmcblk0p3",
			UsedBytes:  912101376,
			TotalBytes: 8216080384,
			SizeBytes:  8589934592,
		},
	}

	got := formatPartitionTable(parts)

	if !strings.Contains(got, "SIZE") {
		t.Errorf("expected a SIZE column header, got:\n%s", got)
	}
	if !strings.Contains(got, "USABLE") {
		t.Errorf("expected the usable column to be labeled USABLE, got:\n%s", got)
	}
	if strings.Contains(got, "TOTAL") {
		t.Errorf("USABLE should replace TOTAL when a size is present, got:\n%s", got)
	}
	// 8589934592 / 1e9 = 8.59 (raw partition), 8216080384 / 1e9 = 8.22 (usable).
	if !strings.Contains(got, "8.59 GB") {
		t.Errorf("expected raw partition size 8.59 GB in the SIZE column, got:\n%s", got)
	}
	if !strings.Contains(got, "8.22 GB") {
		t.Errorf("expected usable size 8.22 GB, got:\n%s", got)
	}
	// USE% stays used/usable: 912101376 / 8216080384 = 11%.
	if !strings.Contains(got, "11%") {
		t.Errorf("USE%% should remain used/usable (11%%), got:\n%s", got)
	}
}

// When the table shows the SIZE column (because some partition reports a size)
// but a given partition's size is unknown, that cell renders "-" rather than 0.
func TestFormatPartitionTableMixedSize(t *testing.T) {
	parts := []*agentpb.DiskPartition{
		{Mountpoint: "/", Filesystem: "ext4", Device: "/dev/mmcblk0p3", UsedBytes: 1, TotalBytes: 8216080384, SizeBytes: 8589934592},
		{Mountpoint: "/config", Filesystem: "vfat", Device: "/dev/mmcblk0p2", UsedBytes: 1, TotalBytes: 268152832}, // size unknown
	}

	got := formatPartitionTable(parts)

	if !strings.Contains(got, "SIZE") {
		t.Fatalf("expected SIZE column when any partition has a size, got:\n%s", got)
	}
	if !strings.Contains(got, "\t-\t") && !strings.Contains(got, " - ") {
		t.Errorf("partition with unknown size should render '-' in the SIZE column, got:\n%s", got)
	}
}

// Agents that predate the size_bytes field report 0; the table keeps its
// original TOTAL layout with no SIZE column so old devices are unchanged.
func TestFormatPartitionTableWithoutSize(t *testing.T) {
	parts := []*agentpb.DiskPartition{
		{
			Mountpoint: "/",
			Filesystem: "ext4",
			Device:     "/dev/mmcblk0p3",
			UsedBytes:  912101376,
			TotalBytes: 8216080384,
			// SizeBytes left 0 (unknown).
		},
	}

	got := formatPartitionTable(parts)

	if strings.Contains(got, "SIZE") {
		t.Errorf("no SIZE column should appear when no partition reports a size, got:\n%s", got)
	}
	if !strings.Contains(got, "TOTAL") {
		t.Errorf("original TOTAL header should be preserved for old agents, got:\n%s", got)
	}
	if !strings.Contains(got, "8.22 GB") {
		t.Errorf("expected usable size 8.22 GB, got:\n%s", got)
	}
}

func TestPartitionsJSON(t *testing.T) {
	parts := []*agentpb.DiskPartition{
		{Mountpoint: "/", Filesystem: "ext4", Device: "/dev/mmcblk0p3", UsedBytes: 912101376, TotalBytes: 8216080384, SizeBytes: 8589934592},
		{Mountpoint: "/boot", Filesystem: "vfat", Device: "/dev/mmcblk0p1", UsedBytes: 52137984, TotalBytes: 174280704},
	}

	got := partitionsJSON(parts)

	if len(got) != 2 {
		t.Fatalf("expected 2 partition entries, got %d", len(got))
	}
	root := got[0]
	if root["totalBytes"] != int64(8216080384) {
		t.Errorf("totalBytes = %v, want 8216080384", root["totalBytes"])
	}
	if root["sizeBytes"] != int64(8589934592) {
		t.Errorf("sizeBytes = %v, want 8589934592", root["sizeBytes"])
	}
	if _, ok := got[1]["sizeBytes"]; ok {
		t.Errorf("sizeBytes should be omitted when the size is unknown (0), got %v", got[1]["sizeBytes"])
	}
}
