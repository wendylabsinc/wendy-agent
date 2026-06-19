package bct

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// loadGoldenMB1Inputs reads the 12 compiled MB1 DTB inputs that produce the
// golden MB1 BCT. The DTB file for each tegrabct argument is resolved from the
// golden rcmboot_bct_cfg.xml (the <misc>, <pinmux>, ... entries) compiled into
// testdata/golden/dtb. Inputs absent from the fixture set are left nil; the
// test skips if a present-expected file is missing.
func loadGoldenMB1Inputs(t *testing.T) MB1BCTInputs {
	t.Helper()
	dtbDir := filepath.Join(goldenDir, "dtb")
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dtbDir, name))
		if err != nil {
			t.Skipf("golden DTB %q not present: %v", name, err)
		}
		return b
	}
	return MB1BCTInputs{
		SDRAM:      read("tegra264-p3834-0008-sdram-bct-l4t_cpp.dtb"),
		WB0SDRAM:   read("tegra264-p3834-0008-sdram-bct-warmboot-l4t_cpp.dtb"),
		Device:     read("tegra264-mb1-bct-device-p3834-xxxx_cpp.dtb"),
		UPhy:       read("tegra264-mb1-bct-uphy-lanes-p4071-0000_cpp.dtb"),
		Pinmux:     read("tegra264-mb1-bct-pinmux-p3834-xxxx-p4071-0000_cpp.dtb"),
		PMIC:       read("tegra264-mb1-bct-pmic-p3834-0008-p4071-0000_cpp.dtb"),
		PMC:        read("tegra264-mb1-bct-padvoltage-p3834-xxxx-p4071-0000_cpp.dtb"),
		Misc:       read("tegra264-mb1-bct-misc-p3834-0008-p4071-0000_cpp.dtb"),
		Prod:       read("tegra264-mb1-bct-prod-p3834-xxxx-p4071-0000_cpp.dtb"),
		GPIOInt:    read("tegra264-mb1-bct-gpioint-p3834-xxxx-p4071-0000_cpp.dtb"),
		DeviceProd: read("tegra264-mb1-bct-cprod-p3834-xxxx-p4071-0000_cpp.dtb"),
		// MinRatchet: the golden cfg leaves <minratchet> empty.
	}
}

// loadGoldenMB1BCT reads the expected 38432-byte MB1 BCT output.
func loadGoldenMB1BCT(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, "mb1_bct_MB1.bct"))
	if err != nil {
		t.Skip("golden MB1 BCT not present")
	}
	return b
}

// deferredRegion is one contiguous byte range that this increment does NOT yet
// reproduce, attributed to the DTB input / handler that owns it. The bounds are
// derived from the s_Mb1BctFields field offsets (mb1BctFields) and the per-input
// DeInit destination offsets in docs/tegraflash-re/tegrabct_v2-dtb-field-mapping.md
// section 5.2, so each range maps to a concrete, deliberately-deferred handler.
type deferredRegion struct {
	start, end int
	owner      string
	note       string
}

// mb1DeferredRegions enumerates every region of the golden MB1 BCT that the
// current BuildMB1BCT leaves zero. The implemented regions (static header,
// --prod triples at 0x7450, --uphy lane owners at 0x8868) are deliberately NOT
// listed: they must match the golden byte-for-byte, which TestMB1BCTMatchesGolden
// enforces by comparing every byte outside this set. The set is verified
// honest-and-minimal by TestMB1BCTGaps, which asserts that no diff falls outside
// it and that every listed region actually differs.
var mb1DeferredRegions = []deferredRegion{
	{0x02b8, 0x0458, "misc", "MISC scalar/array cluster: idx 40 (0x2b8), 55/56 (0x2c0/0x2c8), 41-50 (0x2d8-0x2fc), 82 (0x3a0), 81 (0x3e8), 57-59 (0x438-0x440), 6 (0x450, 8 B)"},
	{0x05e0, 0x0ca0, "sdram", "SDRAM parameter index table (idx 0-4, 27 sets x 64B) from --sdram NvTegraT264PackSdramParams"},
	{0x0ca0, 0x1620, "misc", "MISC idx 13 (0xca0), 14 (0xce0 84x28 block), 26,52,78, and idx 15/16 embedded-MISC header"},
	{0x1638, 0x1670, "misc", "MISC idx 53 (0x1638), 79 (0x1650), 80 (0x1664)"},
	{0x19b0, 0x19f4, "misc", "MISC idx 54 (0x19b0)"},
	{0x1aac, 0x1e74, "misc", "MISC array block (per-entry 53-byte records at 0x1aac/0x1b9c/0x1c8c/0x1d7c + 0x1e70)"},
	{0x2528, 0x2558, "misc", "MISC idx 62-71 (0x2528-0x254b)"},
	{0x3470, 0x3488, "pmc", "--pmc /mb1_bct/pmc@N/ pad-voltage register block (count + 2 reg pairs); bit encoding not yet derived"},
	{0x80e0, 0x8240, "gpioint", "--gpioint /mb1_bct/gpio-intmap@N/ packed interrupt-routing bitmap (0x160/port DeInit transform)"},
	{0x83a0, 0x83a8, "deviceprod", "--deviceprod /deviceprod/ packed controller-prod list (302 dwords, '$'-delimited names)"},
	{0x8898, 0x8b88, "pmic", "--pmic /mb1_bct/pmic_config@N/ bit-encoded command serializer (0x4b8/rail)"},
	{0x9210, 0x9290, "device", "--device /mb1_bct/boot_device/ qspi(0x50)/ufs(0x48) records incl. unexplained 0x570dc09f header word"},
	{0x9568, 0x956c, "misc", "MISC idx 89 (0x9568)"},
	{0x9608, 0x960d, "misc", "MISC trailing field near 0x9608"},
}

// inMB1Deferred reports whether off lies in any declared deferred region.
func inMB1Deferred(off int) bool {
	for _, r := range mb1DeferredRegions {
		if off >= r.start && off < r.end {
			return true
		}
	}
	return false
}

// TestMB1BCTHeader checks the static header: the MB1B0264 magic at 0, the
// 0x00020000 version word at offset 8, the 0x9620 size word at offset 0x0c, and
// the overall 38432-byte length.
func TestMB1BCTHeader(t *testing.T) {
	out, err := BuildMB1BCT(loadGoldenMB1Inputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 38432 {
		t.Fatalf("MB1 BCT size = %d, want 38432", len(out))
	}
	if !bytes.Equal(out[0:8], []byte("MB1B0264")) {
		t.Errorf("magic = %q, want MB1B0264", out[0:8])
	}
	// version word 0x00020000 -> little-endian bytes 00 00 02 00.
	if !bytes.Equal(out[8:12], []byte{0x00, 0x00, 0x02, 0x00}) {
		t.Errorf("version word @0x8 = % x, want 00 00 02 00", out[8:12])
	}
	// size word 0x9620 at offset 0x0c.
	if !bytes.Equal(out[0x0c:0x10], []byte{0x20, 0x96, 0x00, 0x00}) {
		t.Errorf("size word @0xc = % x, want 20 96 00 00", out[0x0c:0x10])
	}
}

// TestMB1BCTMatchesGolden compares BuildMB1BCT(golden inputs) to the golden MB1
// BCT byte-for-byte, EXCLUDING the documented deferred regions (mb1DeferredRegions).
// Everything outside those regions, the static header and the fully-decoded
// --prod, --uphy, and --pinmux outputs, must match exactly. The excluded regions
// are the heavy not-yet-ported handlers (SDRAM pack, pmic, pmc, gpioint,
// deviceprod, device, and the 61-handler MISC sub-block).
func TestMB1BCTMatchesGolden(t *testing.T) {
	out, err := BuildMB1BCT(loadGoldenMB1Inputs(t))
	if err != nil {
		t.Fatal(err)
	}
	want := loadGoldenMB1BCT(t)
	if len(out) != len(want) {
		t.Fatalf("length %d != golden %d", len(out), len(want))
	}

	mismatch := -1
	for off := 0; off < len(out); off++ {
		if inMB1Deferred(off) {
			continue
		}
		if out[off] != want[off] {
			mismatch = off
			break
		}
	}
	if mismatch >= 0 {
		t.Fatalf("MB1 BCT mismatch outside deferred regions at +0x%x: got 0x%02x want 0x%02x",
			mismatch, out[mismatch], want[mismatch])
	}
}

// TestMB1BCTGaps documents, honestly and explicitly, what this increment does
// NOT yet reproduce. It is not a silent skip: it verifies that every byte where
// our output differs from the golden falls inside a declared deferred region
// (no hidden gaps, no regressed implemented region), then logs each region with
// its byte count and owning DTB input so the remaining MB1 work is precisely
// scoped. It also reports the match coverage (golden non-zero bytes matched vs
// deferred).
func TestMB1BCTGaps(t *testing.T) {
	out, err := BuildMB1BCT(loadGoldenMB1Inputs(t))
	if err != nil {
		t.Fatal(err)
	}
	want := loadGoldenMB1BCT(t)

	// Every differing byte must be inside a declared deferred region.
	for off := 0; off < len(out); off++ {
		if out[off] != want[off] && !inMB1Deferred(off) {
			t.Errorf("unexpected diff outside declared deferred region at +0x%x: got 0x%02x want 0x%02x",
				off, out[off], want[off])
		}
	}

	// Each declared region must actually carry a difference (otherwise it is a
	// stale exclusion that should be removed).
	totalGoldenNZ := 0
	for _, b := range want {
		if b != 0 {
			totalGoldenNZ++
		}
	}

	// Per-owner aggregation.
	type agg struct{ span, diffBytes, goldenNZ int }
	owners := map[string]*agg{}
	deferredGoldenNZ := 0
	for _, r := range mb1DeferredRegions {
		a := owners[r.owner]
		if a == nil {
			a = &agg{}
			owners[r.owner] = a
		}
		anyDiff := false
		for off := r.start; off < r.end; off++ {
			a.span++
			if want[off] != 0 {
				a.goldenNZ++
				deferredGoldenNZ++
			}
			if out[off] != want[off] {
				a.diffBytes++
				anyDiff = true
			}
		}
		if !anyDiff {
			t.Errorf("deferred region [0x%x,0x%x) %s shows no diff (stale exclusion?)", r.start, r.end, r.owner)
		}
	}

	matchedNZ := totalGoldenNZ - deferredGoldenNZ
	t.Logf("golden non-zero bytes: %d; matched byte-exact: %d; in deferred regions: %d",
		totalGoldenNZ, matchedNZ, deferredGoldenNZ)
	t.Logf("implemented: static header (0x0-0x18), --prod triples (0x7450), --uphy lane owners (0x8868), --pinmux register pairs (0x35c0)")
	t.Logf("deferred regions (offset range / owning DTB input / golden non-zero bytes):")
	for _, r := range mb1DeferredRegions {
		t.Logf("  [0x%05x,0x%05x) len=%-5d %-11s %s", r.start, r.end, r.end-r.start, r.owner, r.note)
	}
	for owner, a := range owners {
		t.Logf("  owner %-11s span=%d golden-non-zero=%d", owner, a.span, a.goldenNZ)
	}
}
