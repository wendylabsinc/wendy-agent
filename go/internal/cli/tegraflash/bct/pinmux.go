package bct

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/dtb"
)

// Pinmux MB1 BCT region constants. tegrabct_v2's PinmuxT264CfgDeInitHandler
// copies a 0x1f48-byte (0x7d2-dword) staging buffer into the MB1 BCT at
// mb1PinmuxBase + sku*0x1f48. The staging buffer holds a u64 register-pair
// count followed by {u32 addr, u32 value} pairs. Only sku 0 is populated by
// the golden fixture (no per-sku platform data node), so we emit at the base.
const (
	mb1PinmuxBase   = 0x35c0
	mb1PinmuxStride = 0x1f48
)

// pinmuxRegPair is one {register-address, register-value} entry, the unit the
// pinmux region stores after the leading count.
type pinmuxRegPair struct {
	addr uint32
	val  uint32
}

// parsePinmux reproduces the PinmuxT264 Init/Property/DeInit handlers
// (dtb-field-mapping "Pinmux encoding"). For node /mb1_bct/padctl@N/ it builds
// an ordered register-pair list from the GPIO sub-nodes (gpio@ADDR/default/
// gpio-{input,output-low,output-high}) followed by the per-pin pinmux config
// words (pinmux@ADDR/{common,unused_lowpower}/<pin>), then writes the pair
// count as a u64 at mb1PinmuxBase + sku*mb1PinmuxStride and the {addr,val}
// pairs on an 8-byte stride at +8.
func parsePinmux(out []byte, blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	fdt, err := dtb.ParseFDT(blob)
	if err != nil {
		return fmt.Errorf("parse fdt: %w", err)
	}
	for sku := 0; sku < 2; sku++ {
		node := fmt.Sprintf("/mb1_bct/padctl@%d", sku)
		if !fdt.HasNode(node) {
			continue
		}
		pairs, err := buildPinmuxPairs(fdt, node)
		if err != nil {
			return fmt.Errorf("padctl@%d: %w", sku, err)
		}
		base := mb1PinmuxBase + sku*mb1PinmuxStride
		if base+8+len(pairs)*8 > len(out) {
			return fmt.Errorf("padctl@%d region out of bounds", sku)
		}
		binary.LittleEndian.PutUint64(out[base:], uint64(len(pairs)))
		for i, p := range pairs {
			e := base + 8 + i*8
			binary.LittleEndian.PutUint32(out[e:], p.addr)
			binary.LittleEndian.PutUint32(out[e+4:], p.val)
		}
	}
	return nil
}

// buildPinmuxPairs assembles the ordered register-pair list for one padctl
// node: GPIO controller writes first (in DTB child order), then the pinmux pin
// config words (each pin group, in DTB child order).
func buildPinmuxPairs(fdt *dtb.FDT, padctl string) ([]pinmuxRegPair, error) {
	var pairs []pinmuxRegPair

	children, err := fdt.Children(padctl)
	if err != nil {
		return nil, err
	}
	// GPIO controllers come first, in DTB order, then the pinmux node.
	var pinmuxNode string
	for _, ch := range children {
		switch {
		case strings.HasPrefix(ch, "gpio@"):
			gp, err := buildGPIOPairs(fdt, padctl+"/"+ch)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", ch, err)
			}
			pairs = append(pairs, gp...)
		case strings.HasPrefix(ch, "pinmux@"):
			pinmuxNode = padctl + "/" + ch
		}
	}
	if pinmuxNode != "" {
		pp, err := buildPinPairs(fdt, pinmuxNode)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, pp...)
	}
	return pairs, nil
}

// buildGPIOPairs reproduces AddPinmuxRegValues for one gpio@ controller. The
// gpio@ADDR base selects a group table (pinMuxGPIOTables); the default child's
// gpio-input / gpio-output-low / gpio-output-high lists each carry GPIO pin
// indices. For index i the per-pin register base is table[i>>3] + (i&7)<<5.
// gpio-input emits one pair {base, 1}; gpio-output-{low,high} each emit three
// pairs {base, 3}, {base+0xc, 0}, {base+0x10, low?0:1}. Lists are processed in
// input, output-low, output-high order, matching the reference emission order.
func buildGPIOPairs(fdt *dtb.FDT, gpioNode string) ([]pinmuxRegPair, error) {
	at := strings.LastIndex(gpioNode, "@")
	if at < 0 {
		return nil, fmt.Errorf("gpio node %q has no address", gpioNode)
	}
	ctrl, err := parseHex32(gpioNode[at+1:])
	if err != nil {
		return nil, fmt.Errorf("gpio node %q address: %w", gpioNode, err)
	}
	table, ok := pinMuxGPIOTables[ctrl]
	if !ok {
		return nil, fmt.Errorf("unknown gpio controller 0x%08x", ctrl)
	}
	regBase := func(idx uint32) (uint32, error) {
		grp := idx >> 3
		if int(grp) >= len(table) {
			return 0, fmt.Errorf("gpio index %d out of range for controller 0x%08x (%d groups)", idx, ctrl, len(table))
		}
		return table[grp] + ((idx & 7) << 5), nil
	}

	def := gpioNode + "/default"
	var pairs []pinmuxRegPair
	if cells, ok := fdt.PropertyU32Array(def, "gpio-input"); ok {
		for _, idx := range cells {
			b, err := regBase(idx)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, pinmuxRegPair{b, 1})
		}
	}
	if cells, ok := fdt.PropertyU32Array(def, "gpio-output-low"); ok {
		for _, idx := range cells {
			b, err := regBase(idx)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, pinmuxRegPair{b, 3}, pinmuxRegPair{b + 0xc, 0}, pinmuxRegPair{b + 0x10, 0})
		}
	}
	if cells, ok := fdt.PropertyU32Array(def, "gpio-output-high"); ok {
		for _, idx := range cells {
			b, err := regBase(idx)
			if err != nil {
				return nil, err
			}
			pairs = append(pairs, pinmuxRegPair{b, 3}, pinmuxRegPair{b + 0xc, 0}, pinmuxRegPair{b + 0x10, 1})
		}
	}
	return pairs, nil
}

// parseHex32 parses a bare hex node-address suffix (no 0x prefix) into a u32.
func parseHex32(s string) (uint32, error) {
	var v uint64
	if _, err := fmt.Sscanf(s, "%x", &v); err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// buildPinPairs walks the pin groups under a pinmux@ node (common, then
// unused_lowpower, in DTB order) and emits one register pair per pin.
func buildPinPairs(fdt *dtb.FDT, pinmuxNode string) ([]pinmuxRegPair, error) {
	groups, err := fdt.Children(pinmuxNode)
	if err != nil {
		return nil, err
	}
	var pairs []pinmuxRegPair
	for _, grp := range groups {
		if grp == "drive" { // empty drive node carries no pin config
			continue
		}
		gpath := pinmuxNode + "/" + grp
		pins, err := fdt.Children(gpath)
		if err != nil {
			return nil, err
		}
		// The reference splits the "common" group into a leading SFIO section
		// (keeps the init 0x400 LPDR bit) and a trailing GPIO section (clears
		// it). The source DTSI marks the boundary with a comment that the
		// compiled DTB drops, but the boundary is the first reserved-function
		// pin: every pin from there to the end of the group is in the GPIO
		// section. We track it as a sticky flag in DTB child order.
		inGPIOSection := false
		for _, pin := range pins {
			path := gpath + "/" + pin
			if grp == "common" && !inGPIOSection {
				if fn, _ := fdt.PropertyString(path, "nvidia,function"); strings.HasPrefix(fn, "rsvd") {
					inGPIOSection = true
				}
			}
			p, ok, err := pinPair(fdt, path, pin, grp, inGPIOSection)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			pairs = append(pairs, p)
		}
	}
	return pairs, nil
}

// pinPair computes the {register-address, value} pair for a single pin node,
// reproducing the PinmuxT264 Init default + Property bit-field inserts.
// gpioSection reports whether the pin sits in the common group's trailing GPIO
// section (which force-clears the init 0x400 bit).
func pinPair(fdt *dtb.FDT, path, pin, group string, gpioSection bool) (pinmuxRegPair, bool, error) {
	e, ok := pinMuxEntries[pin]
	if !ok {
		// Pin not in the chip pin-address table: not a configurable pad, skip.
		return pinmuxRegPair{}, false, nil
	}
	addr := pinMuxRegBase[e.f1c] + e.f18
	// A pin present in the DTB must have a reset-default; a miss means the
	// defaults table is incomplete for this board (silently using 0 would emit a
	// wrong register value), so fail loudly rather than miscompile.
	val, ok := pinMuxDefaults[addr]
	if !ok {
		return pinmuxRegPair{}, false, fmt.Errorf("pin %q: no reset-default for register 0x%08x", pin, addr)
	}

	fn, _ := fdt.PropertyString(path, "nvidia,function")

	// f1c==5 pins (the dp_aux_ch* shared-AUX registers) take the gpio-init
	// style function path, not the normal bit-field inserts.
	if e.f1c == 5 {
		val = applyAuxFunction(val, e, fn)
		return pinmuxRegPair{addr, val}, true, nil
	}

	// Init: clear bit 0x400, then re-set it when the pin flag is 0. The
	// reference then force-clears it again for the common group's GPIO section.
	val &^= 0x400
	if e.f20 == 0 && !(group == "common" && gpioSection) {
		val |= 0x400
	}

	// nvidia,function -> 2-bit field (bits 0-1): index within funcs[1..4].
	fidx := 0
	for k := 1; k < 5; k++ {
		if e.funcs[k] == fn {
			fidx = k - 1
			break
		}
	}
	val = (val &^ 0x3) | uint32(fidx&0x3)

	val = applyPinBit(fdt, path, "nvidia,pull", val, 2, 0xc)
	val = applyPinBit(fdt, path, "nvidia,tristate", val, 4, 0x10)
	val = applyPinBit(fdt, path, "nvidia,enable-input", val, 6, 0x40)
	val = applyPinBit(fdt, path, "nvidia,e-io-od", val, 5, 0x20)
	val = applyPinBit(fdt, path, "nvidia,drv-type", val, 8, 0x100)
	val = applyPinBit(fdt, path, "nvidia,e-lpbk", val, 7, 0x80)

	return pinmuxRegPair{addr, val}, true, nil
}

// applyPinBit clears mask in val then ORs (prop<<shift)&mask using the named
// nvidia,* property. An absent property leaves the default bit untouched: the
// reference only performs the read-modify-write when the property is present,
// so a missing property keeps whatever the per-register default carried.
func applyPinBit(fdt *dtb.FDT, path, prop string, val uint32, shift, mask uint32) uint32 {
	v, ok := fdt.PropertyU32(path, prop)
	if !ok {
		return val
	}
	val &^= mask
	val |= (v << shift) & mask
	return val
}

// applyAuxFunction reproduces the f1c==5 gpio-init style function handler:
// clears the function bits then, on a function-name match in funcs[1..4],
// inserts (index<<11)|0x80000000. A self-named match (the gpio function in
// slot 1) leaves the value unchanged.
func applyAuxFunction(val uint32, e pinMuxEntry, fn string) uint32 {
	for k := 1; k < 5; k++ {
		if e.funcs[k] == fn {
			if k == 1 {
				return val
			}
			val &= 0x7ffff7ff
			val |= (uint32(k-1) << 11) | 0x80000000
			return val
		}
	}
	return val
}
