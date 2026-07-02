package t234

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RCMBootBlobDir is where tegraflash's sign step leaves the RCM boot chain
// (inside the bundle root).
const RCMBootBlobDir = "rcmboot_blob"

// RCMBoot describes the stage-1 RCM boot: the bootROM image filenames in send
// order, plus the mem BCT and blob that follow once mb1 is up. All names are
// relative to Dir. It is parsed from rcmbootcmd.txt — the two tegrarcm_v2
// command lines tegraflash records for a pre-signed boot — so a BSP that
// reorders or renames the chain needs no wendy code change.
type RCMBoot struct {
	Dir       string   // absolute rcmboot_blob directory
	SendOrder []string // bootROM images (--download args of the first command)
	MemBCT    string   // --download bct_mem argument of the second command
	Blob      string   // --download blob argument of the second command
}

// ParseRCMBootCmd reads <bundleDir>/rcmboot_blob/rcmbootcmd.txt. The expected
// shape (tegraflash_impl_t234.tegraflash_rcmboot, --securedev branch):
//
//	./tegrarcm_v2 --new_session --chip 0x23 0 --uid --download bct_br <f> --download mb1 <f> --download psc_bl1 <f> --download bct_mb1 <f>
//	./tegrarcm_v2 --chip 0x23 0 --pollbl --download bct_mem <f> --download blob <f>
func ParseRCMBootCmd(bundleDir string) (*RCMBoot, error) {
	dir := filepath.Join(bundleDir, RCMBootBlobDir)
	data, err := os.ReadFile(filepath.Join(dir, "rcmbootcmd.txt"))
	if err != nil {
		return nil, fmt.Errorf("reading rcmbootcmd.txt: %w", err)
	}

	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		return nil, fmt.Errorf("rcmbootcmd.txt: expected 2 tegrarcm_v2 commands, got %d", len(lines))
	}

	boot := &RCMBoot{Dir: dir}
	first := parseDownloads(lines[0])
	if len(first) == 0 {
		return nil, fmt.Errorf("rcmbootcmd.txt: no --download arguments in the bootROM command")
	}
	for _, d := range first {
		boot.SendOrder = append(boot.SendOrder, d.file)
	}
	for _, d := range parseDownloads(lines[1]) {
		switch d.kind {
		case "bct_mem":
			boot.MemBCT = d.file
		case "blob":
			boot.Blob = d.file
		}
	}
	if boot.MemBCT == "" || boot.Blob == "" {
		return nil, fmt.Errorf("rcmbootcmd.txt: bootloader command is missing bct_mem or blob download")
	}
	for _, f := range append(append([]string{}, boot.SendOrder...), boot.MemBCT, boot.Blob) {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, fmt.Errorf("rcmboot_blob is missing %s: %w", f, err)
		}
	}
	return boot, nil
}

type download struct{ kind, file string }

// parseDownloads extracts the (kind, file) pairs of every `--download k f` in
// a tegrarcm_v2 command line.
func parseDownloads(line string) []download {
	fields := strings.Fields(line)
	var out []download
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] == "--download" {
			out = append(out, download{kind: fields[i+1], file: fields[i+2]})
			i += 2
		}
	}
	return out
}
