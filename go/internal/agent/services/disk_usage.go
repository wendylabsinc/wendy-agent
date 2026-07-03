package services

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type diskUsage struct {
	usedBytes  int64
	totalBytes int64
}

// partitionUsage describes a single mounted filesystem and its space usage.
type partitionUsage struct {
	mountpoint string
	filesystem string
	device     string
	usedBytes  int64
	totalBytes int64
	// sizeBytes is the raw backing block-device size (the provisioned
	// partition size). It is larger than totalBytes by the filesystem's own
	// metadata overhead. Zero when the size cannot be determined.
	sizeBytes int64
}

// mountEntry is one parsed line from /proc/mounts.
type mountEntry struct {
	device     string
	mountpoint string
	filesystem string
}

// parseProcMounts parses the contents of /proc/mounts and returns the real,
// disk-backed filesystems. A filesystem is considered disk-backed when its
// source is a path under /dev/, which excludes pseudo-filesystems such as
// tmpfs, proc, sysfs, cgroup and overlay. Entries are deduplicated by backing
// device (the first mount of a device wins) so a disk that is bind-mounted in
// several places is only reported once.
func parseProcMounts(content string) []mountEntry {
	var entries []mountEntry
	seen := make(map[string]struct{})

	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		device := unescapeMountField(fields[0])
		if !strings.HasPrefix(device, "/dev/") {
			continue
		}
		if _, dup := seen[device]; dup {
			continue
		}
		seen[device] = struct{}{}
		entries = append(entries, mountEntry{
			device:     device,
			mountpoint: unescapeMountField(fields[1]),
			filesystem: fields[2],
		})
	}

	return entries
}

// unescapeMountField decodes the octal escape sequences (\040 space, \011 tab,
// \012 newline, \134 backslash) that the kernel writes into /proc/mounts for
// characters that would otherwise be ambiguous.
func unescapeMountField(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(field)
}

// blockSizeFromSysfs reads the raw size of a block device from
// classBlockDir/<name>/size, which the kernel always reports in 512-byte
// sectors regardless of the device's logical block size. Returns the size in
// bytes, or (0, false) when the entry is missing or unreadable. classBlockDir
// is a parameter so the reader is testable against a fixture directory; the
// production caller passes "/sys/class/block".
func blockSizeFromSysfs(classBlockDir, name string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(classBlockDir, name, "size"))
	if err != nil {
		return 0, false
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || sectors <= 0 || sectors > math.MaxInt64/512 {
		return 0, false
	}
	return sectors * 512, true
}

func calculateDiskUsage(blocks, freeBlocks, blockSize uint64) (diskUsage, bool) {
	if blockSize == 0 || blocks == 0 || freeBlocks > blocks {
		return diskUsage{}, false
	}

	usedBlocks := blocks - freeBlocks
	if blocks > math.MaxInt64/blockSize || usedBlocks > math.MaxInt64/blockSize {
		return diskUsage{}, false
	}

	return diskUsage{
		usedBytes:  int64(usedBlocks * blockSize),
		totalBytes: int64(blocks * blockSize),
	}, true
}
