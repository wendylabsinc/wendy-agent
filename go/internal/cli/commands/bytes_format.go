package commands

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// formatBytes converts a byte count to a human-readable string using SI units
// (powers of 1000: kB, MB, GB). This is the package-level helper used by
// both the apps dashboard and volumes commands.
func formatBytes(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1f kB", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatDiskUsage(usedBytes, totalBytes int64) string {
	return fmt.Sprintf("%s / %s", formatGigabytes(usedBytes), formatGigabytes(totalBytes))
}

func formatGigabytes(n int64) string {
	s := fmt.Sprintf("%.2f", float64(n)/1_000_000_000)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s + " GB"
}

// formatPartitionTable renders an aligned, human-readable table of per-partition
// disk usage suitable for printing under the device info output. The returned
// string ends with a trailing newline.
//
// When the agent reports the raw block-device size (size_bytes), the table adds
// a SIZE column (the provisioned partition size) and labels the filesystem
// capacity USABLE, so the ext4-metadata gap between the two is legible — a
// filesystem's usable capacity is always smaller than its partition. Agents
// predating the field report size 0 for every partition; the table then keeps
// its original TOTAL-only layout unchanged.
func formatPartitionTable(partitions []*agentpb.DiskPartition) string {
	showSize := false
	for _, p := range partitions {
		if p.GetSizeBytes() > 0 {
			showSize = true
			break
		}
	}

	var b strings.Builder
	b.WriteString("Disk Usage:\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	if showSize {
		fmt.Fprintln(tw, "  MOUNTPOINT\tFILESYSTEM\tUSED\tSIZE\tUSABLE\tUSE%")
	} else {
		fmt.Fprintln(tw, "  MOUNTPOINT\tFILESYSTEM\tUSED\tTOTAL\tUSE%")
	}
	for _, p := range partitions {
		if showSize {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				p.GetMountpoint(),
				p.GetFilesystem(),
				formatGigabytes(p.GetUsedBytes()),
				formatPartitionSize(p.GetSizeBytes()),
				formatGigabytes(p.GetTotalBytes()),
				formatUsePercent(p.GetUsedBytes(), p.GetTotalBytes()),
			)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			p.GetMountpoint(),
			p.GetFilesystem(),
			formatGigabytes(p.GetUsedBytes()),
			formatGigabytes(p.GetTotalBytes()),
			formatUsePercent(p.GetUsedBytes(), p.GetTotalBytes()),
		)
	}
	tw.Flush()

	return b.String()
}

// partitionsJSON converts the agent's per-partition disk usage into the
// map slice used for `--json` output. size_bytes is included only when known
// (> 0) so old agents and non-Linux hosts don't emit a misleading zero.
func partitionsJSON(partitions []*agentpb.DiskPartition) []map[string]any {
	parts := make([]map[string]any, len(partitions))
	for i, p := range partitions {
		entry := map[string]any{
			"mountpoint": p.GetMountpoint(),
			"filesystem": p.GetFilesystem(),
			"device":     p.GetDevice(),
			"usedBytes":  p.GetUsedBytes(),
			"totalBytes": p.GetTotalBytes(),
		}
		if p.GetSizeBytes() > 0 {
			entry["sizeBytes"] = p.GetSizeBytes()
		}
		parts[i] = entry
	}
	return parts
}

// formatPartitionSize renders a raw block-device size, or "-" when the agent
// could not determine it (a single partition on a device whose size is unknown
// while others are known).
func formatPartitionSize(sizeBytes int64) string {
	if sizeBytes <= 0 {
		return "-"
	}
	return formatGigabytes(sizeBytes)
}

// formatUsePercent returns the used fraction as a whole-number percent string
// (e.g. "16%"), or "-" when the total is unknown.
func formatUsePercent(used, total int64) string {
	if total <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", used*100/total)
}
