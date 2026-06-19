package bct

import (
	"encoding/binary"
	"fmt"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/dtb"
)

// mb1BctSize is the fixed MB1 BCT buffer size in bytes. The reverse-engineering
// doc reports NvTegraT264Mb1BctGetSize returning 0x9618 (38424); the golden
// fixture is 0x9620 (38432). We follow the golden, which carries an 8-byte tail
// past the highest field (idx 89 ends at 0x956c). The size word at offset 0x0c
// of the header is itself 0x9620, confirming the golden size.
const mb1BctSize = 0x9620

// MB1 BCT header layout (NvTegraT264BctInitStaticFields). The 8-byte ASCII
// magic sits at offset 0, an 8-byte version word at offset 8, the fixed size at
// offset 0x0c, and a static field-count-like word at offset 0x10.
const (
	mb1Magic       = "MB1B0264"
	mb1VersionWord = 0x00020000 // bytes 00 00 02 00: +0x8 = 0 (u16), +0xa = 2 (u16)
	mb1SizeOffset  = 0x0c
	mb1StaticWord  = 0x10 // golden holds 0x00000004 here
)

// MB1 BCT destination offsets for the per-input regions, taken from the
// tegrabct_v2 DeInit handlers (dtb-field-mapping section 5.2). Only the regions
// this increment populates are named; the rest are characterized as deferred
// gaps in mb1bct_test.go.
const (
	mb1ProdBase   = 0x7450 // --prod /prod@N/: count u64 then 16-byte triples at +8
	mb1ProdStride = 0x648  // per-prod@N stride
	mb1UPhyBase   = 0x8868 // --uphy /uphy-lane@N/: owner byte at +lane (+8*sel)
)

// MB1BCTInputs carries the 12 compiled DTB blobs that feed the MB1 BCT, named
// after their conventional tegrabct_v2 argument. Each blob may carry one or
// more top-level nodes; routing is by node name, not by argument (see
// dtb-field-mapping section 1.4). A nil/empty blob contributes nothing.
type MB1BCTInputs struct {
	SDRAM      []byte // --sdram     /sdram/        (parsed + packed, deferred)
	WB0SDRAM   []byte // --wb0sdram  /sdram/        (second set, deferred)
	Device     []byte // --device    /mb1_bct/boot_device/ (deferred)
	UPhy       []byte // --uphy      /uphy-lane@N/  (implemented)
	Pinmux     []byte // --pinmux    /mb1_bct/padctl@N/ (deferred)
	PMIC       []byte // --pmic      /mb1_bct/pmic_config@N/ (deferred)
	PMC        []byte // --pmc       /mb1_bct/pmc@N/ (deferred)
	Misc       []byte // --misc      /mb1_bct/, /misc/ (deferred)
	Prod       []byte // --prod      /prod@N/       (implemented)
	GPIOInt    []byte // --gpioint   /mb1_bct/gpio-intmap@N/ (deferred)
	DeviceProd []byte // --deviceprod /deviceprod/  (deferred)
	MinRatchet []byte // --minratchet /ratchet@N/   (deferred)
}

// BuildMB1BCT assembles the MB1 BCT image (MB1B0264 magic) from the supplied DTB
// inputs, pre-signing. It writes the static header and the input regions that
// are fully decoded in this increment (the --prod register triples and the
// --uphy lane-owner map). The heavy regions (SDRAM pack, pinmux, pmic, pmc,
// gpioint, deviceprod, device, and the 61-handler MISC sub-block) are validated
// to parse but intentionally left zero rather than emit incorrect bytes; each
// is enumerated and characterized by TestMB1BCTGaps so the remaining work is
// precisely scoped. The 64-byte integrity digest is injected by the signing
// step (Task 9), not here.
func BuildMB1BCT(in MB1BCTInputs) ([]byte, error) {
	out := make([]byte, mb1BctSize)

	// Static header.
	copy(out[0:8], []byte(mb1Magic))
	binary.LittleEndian.PutUint32(out[8:], mb1VersionWord)
	binary.LittleEndian.PutUint32(out[mb1SizeOffset:], mb1BctSize)
	binary.LittleEndian.PutUint32(out[mb1StaticWord:], 0x4)

	// Tractable inputs (fully decoded, byte-exact against the golden).
	if err := parseProd(out, in.Prod); err != nil {
		return nil, fmt.Errorf("bct: mb1 prod: %w", err)
	}
	if err := parseUPhy(out, in.UPhy); err != nil {
		return nil, fmt.Errorf("bct: mb1 uphy: %w", err)
	}

	// Deferred inputs: validate they parse (so a malformed blob is reported
	// here rather than silently dropped) but emit no bytes. These are the
	// heavy regions enumerated in TestMB1BCTGaps.
	if err := validateDeferredInputs(in); err != nil {
		return nil, err
	}

	return out, nil
}

// parseProd reproduces CommonProdT264 (dtb-field-mapping section 4.6). For each
// /prod@N/ node it reads the addr-mask-data property as a flat list of 3-cell
// {addr, mask, data} entries, writes the entry count as a u64 at
// mb1ProdBase + N*mb1ProdStride, then writes each entry as little-endian
// {addr, mask, data, 0} on a 16-byte stride starting at +8. The DTB cells are
// big-endian; PropertyU32Array converts them to host order, and we re-emit them
// little-endian (the byte-swap the reference tool performs).
func parseProd(out []byte, blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	fdt, err := dtb.ParseFDT(blob)
	if err != nil {
		return fmt.Errorf("parse fdt: %w", err)
	}
	for n := 0; n < 2; n++ {
		node := fmt.Sprintf("/prod@%d", n)
		if !fdt.HasNode(node) {
			continue
		}
		cells, ok := fdt.PropertyU32Array(node, "addr-mask-data")
		if !ok || len(cells) == 0 {
			continue
		}
		entries := len(cells) / 3
		if entries > 100 { // reference caps at 100 entries
			entries = 100
		}
		base := mb1ProdBase + n*mb1ProdStride
		if base+8+entries*16 > len(out) {
			return fmt.Errorf("prod@%d region out of bounds", n)
		}
		binary.LittleEndian.PutUint64(out[base:], uint64(entries))
		for i := 0; i < entries; i++ {
			e := base + 8 + i*16
			binary.LittleEndian.PutUint32(out[e:], cells[i*3+0])   // addr
			binary.LittleEndian.PutUint32(out[e+4:], cells[i*3+1]) // mask
			binary.LittleEndian.PutUint32(out[e+8:], cells[i*3+2]) // data
			// e+12 padding stays zero.
		}
	}
	return nil
}

// parseUPhy reproduces UphyT264 (dtb-field-mapping section 4.11). For each
// /uphy-lane@N/ node it reads lane-owner-map as groups of 3 cells
// {select, lane, owner} and writes the owner's low byte at
// mb1UPhyBase + lane + 8*select. (The doc's "8*(lane+3*sel)" stride is a
// misread; the golden places owners at +lane, sel*8, which this matches
// byte-exact.)
func parseUPhy(out []byte, blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	fdt, err := dtb.ParseFDT(blob)
	if err != nil {
		return fmt.Errorf("parse fdt: %w", err)
	}
	for n := 0; n < 2; n++ {
		node := fmt.Sprintf("/uphy-lane@%d", n)
		if !fdt.HasNode(node) {
			continue
		}
		cells, ok := fdt.PropertyU32Array(node, "lane-owner-map")
		if !ok {
			continue
		}
		for i := 0; i+2 < len(cells); i += 3 {
			sel := cells[i]
			lane := cells[i+1]
			owner := cells[i+2]
			off := mb1UPhyBase + int(lane) + 8*int(sel)
			if off < 0 || off >= len(out) {
				return fmt.Errorf("uphy-lane@%d entry %d offset 0x%x out of bounds", n, i/3, off)
			}
			out[off] = byte(owner)
		}
	}
	return nil
}

// validateDeferredInputs best-effort parses every deferred DTB input. These
// inputs own the documented, deliberate gaps for this increment (enumerated and
// characterized by TestMB1BCTGaps) and emit no bytes, so a parse failure is not
// fatal: some real fixtures (for example an empty deviceprod cprod DTB with a
// zero-length string block) carry no relevant nodes at all. Parsing here only
// exercises the blobs; once a region is implemented its parser will validate
// strictly, as parseProd and parseUPhy already do.
func validateDeferredInputs(in MB1BCTInputs) error {
	for _, e := range []struct {
		name string
		blob []byte
	}{
		{"sdram", in.SDRAM}, {"wb0sdram", in.WB0SDRAM}, {"device", in.Device},
		{"pinmux", in.Pinmux}, {"pmic", in.PMIC}, {"pmc", in.PMC},
		{"misc", in.Misc}, {"gpioint", in.GPIOInt}, {"deviceprod", in.DeviceProd},
		{"minratchet", in.MinRatchet},
	} {
		if len(e.blob) == 0 {
			continue
		}
		if _, err := dtb.ParseFDT(e.blob); err != nil {
			return fmt.Errorf("parse %s dtb: %w", e.name, err)
		}
	}
	return nil
}
