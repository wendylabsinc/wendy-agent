//go:build darwin

package t234

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// listUMSDisks finds USB mass-storage whole disks and their SCSI inquiry
// strings by walking `ioreg -rc IOSCSILogicalUnitNub`: the logical-unit nub
// carries "Vendor Identification"/"Product Identification" (the INQUIRY
// response) and its subtree holds the IOMedia with the whole-disk "BSD Name".
// (The properties live on the nub itself — rooting the query any deeper,
// e.g. at IOSCSIPeripheralDeviceType00, loses them; verified on the real
// flashing gadget.)
func listUMSDisks() ([]UMSDisk, error) {
	out, err := exec.Command("ioreg", "-rc", "IOSCSILogicalUnitNub", "-l", "-w0").Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	var disks []UMSDisk
	for _, chunk := range splitIoregSubtrees(string(out)) {
		vendor := ioregString(chunk, "Vendor Identification")
		if vendor == "" {
			continue
		}
		bsd := ioregString(chunk, "BSD Name")
		if !wholeDiskRe.MatchString(bsd) {
			continue // no media yet, or a partition slice matched first
		}
		d := UMSDisk{
			DevPath: "/dev/" + bsd,
			RawPath: "/dev/r" + bsd,
			Vendor:  strings.TrimSpace(vendor),
			Serial:  strings.TrimSpace(ioregString(chunk, "Product Identification")),
		}
		if size := ioregInt(chunk, "Size"); size > 0 {
			d.SizeBytes = size
		}
		disks = append(disks, d)
	}
	return disks, nil
}

// splitIoregSubtrees splits `ioreg -r` output into one chunk per matched
// root object (each starts with "+-o " at column 0).
func splitIoregSubtrees(out string) []string {
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "+-o ") && cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// wholeDiskRe matches a whole-disk BSD name (diskN, not a diskNsM slice).
var wholeDiskRe = regexp.MustCompile(`^disk\d+$`)

var (
	ioregStrRe = map[string]*regexp.Regexp{}
	ioregIntRe = map[string]*regexp.Regexp{}
)

// ioregString extracts the first `"key" = "value"` in an ioreg chunk.
func ioregString(chunk, key string) string {
	re, ok := ioregStrRe[key]
	if !ok {
		re = regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `" = "([^"]*)"`)
		ioregStrRe[key] = re
	}
	if m := re.FindStringSubmatch(chunk); m != nil {
		return m[1]
	}
	return ""
}

// ioregInt extracts the first `"key" = <number>` in an ioreg chunk.
func ioregInt(chunk, key string) int64 {
	re, ok := ioregIntRe[key]
	if !ok {
		re = regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `" = (\d+)`)
		ioregIntRe[key] = re
	}
	if m := re.FindStringSubmatch(chunk); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// unmountUMSDisk unmounts anything macOS auto-mounted from the LUN (e.g. the
// FAT config partition after the GPT lands). Best-effort: the LUNs usually
// carry no mountable filesystem.
func unmountUMSDisk(d UMSDisk) {
	exec.Command("diskutil", "unmountDisk", "force", d.DevPath).Run() //nolint:errcheck
}
