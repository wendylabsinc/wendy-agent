package dtb_test

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/dtb"
)

// testDTS is a device-tree source with nested nodes and varied property types
// used to exercise the FDT reader end-to-end via dtc.
const testDTS = `/dts-v1/;
/ {
	compatible = "test";
	count = <0x2a>;
	triple = <1 2 3>;
	name = "hello";
	child@0 { reg = <0x10>; };
	child@1 { reg = <0x20>; };
};
`

func TestParseFDT_WithDTC(t *testing.T) {
	dtcPath, err := exec.LookPath("dtc")
	if err != nil {
		t.Skip("dtc not installed: skipping dtc-based FDT test")
	}

	dir := t.TempDir()
	dtsFile := filepath.Join(dir, "test.dts")
	dtbFile := filepath.Join(dir, "test.dtb")

	if err := os.WriteFile(dtsFile, []byte(testDTS), 0o644); err != nil {
		t.Fatalf("write DTS: %v", err)
	}

	// -f forces dtc to produce output even when properties like "name" trigger
	// reserved-property warnings (which are non-fatal in real device trees).
	cmd := exec.Command(dtcPath, "-I", "dts", "-O", "dtb", "-o", dtbFile, "-qqq", "-f", dtsFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dtc: %v\n%s", err, out)
	}

	data, err := os.ReadFile(dtbFile)
	if err != nil {
		t.Fatalf("read dtb: %v", err)
	}

	fdt, err := dtb.ParseFDT(data)
	if err != nil {
		t.Fatalf("ParseFDT: %v", err)
	}

	// PropertyString: NUL-terminated string.
	if got, ok := fdt.PropertyString("/", "compatible"); !ok || got != "test" {
		t.Errorf("PropertyString(\"/\", \"compatible\") = %q, %v; want \"test\", true", got, ok)
	}

	// PropertyU32: single big-endian cell.
	if got, ok := fdt.PropertyU32("/", "count"); !ok || got != 0x2a {
		t.Errorf("PropertyU32(\"/\", \"count\") = %d, %v; want 0x2a, true", got, ok)
	}

	// PropertyU32Array: three-cell property.
	if got, ok := fdt.PropertyU32Array("/", "triple"); !ok || len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("PropertyU32Array(\"/\", \"triple\") = %v, %v; want [1 2 3], true", got, ok)
	}

	// PropertyString: "name" property (dtc accepts it with -f).
	if got, ok := fdt.PropertyString("/", "name"); !ok || got != "hello" {
		t.Errorf("PropertyString(\"/\", \"name\") = %q, %v; want \"hello\", true", got, ok)
	}

	// Children: both child nodes must be present.
	children, err := fdt.Children("/")
	if err != nil {
		t.Fatalf("Children(\"/\"): %v", err)
	}
	childSet := map[string]bool{}
	for _, c := range children {
		childSet[c] = true
	}
	for _, want := range []string{"child@0", "child@1"} {
		if !childSet[want] {
			t.Errorf("Children(\"/\") missing %q; got %v", want, children)
		}
	}

	// PropertyU32 on a child node.
	if got, ok := fdt.PropertyU32("/child@1", "reg"); !ok || got != 0x20 {
		t.Errorf("PropertyU32(\"/child@1\", \"reg\") = 0x%x, %v; want 0x20, true", got, ok)
	}

	// HasNode: present and absent paths.
	if !fdt.HasNode("/child@0") {
		t.Error("HasNode(\"/child@0\") = false; want true")
	}
	if fdt.HasNode("/nope") {
		t.Error("HasNode(\"/nope\") = true; want false")
	}
}

// TestParseFDT_Minimal exercises ParseFDT with a hand-built minimal DTB so
// the core parsing logic is covered without requiring dtc.
//
// The blob encodes:
//
//	/ {
//	    answer = <0x42>;
//	    label = "ok";
//	    nested { value = <0x7>; };
//	};
func TestParseFDT_Minimal(t *testing.T) {
	data := buildMinimalDTB(t)

	fdt, err := dtb.ParseFDT(data)
	if err != nil {
		t.Fatalf("ParseFDT: %v", err)
	}

	if v, ok := fdt.PropertyU32("/", "answer"); !ok || v != 0x42 {
		t.Errorf("PropertyU32(\"/\", \"answer\") = %d, %v; want 0x42, true", v, ok)
	}

	if s, ok := fdt.PropertyString("/", "label"); !ok || s != "ok" {
		t.Errorf("PropertyString(\"/\", \"label\") = %q, %v; want \"ok\", true", s, ok)
	}

	if !fdt.HasNode("/nested") {
		t.Error("HasNode(\"/nested\") = false; want true")
	}
	if v, ok := fdt.PropertyU32("/nested", "value"); !ok || v != 0x7 {
		t.Errorf("PropertyU32(\"/nested\", \"value\") = %d, %v; want 0x7, true", v, ok)
	}

	children, err := fdt.Children("/")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 || children[0] != "nested" {
		t.Errorf("Children(\"/\") = %v; want [nested]", children)
	}

	if fdt.HasNode("/missing") {
		t.Error("HasNode(\"/missing\") = true; want false")
	}
}

// TestParseFDT_EmptyStrings verifies that a minimal DTB whose strings block is
// empty (no property names) parses. Such a blob has off_dt_strings == totalsize,
// which an over-strict bounds check (>= instead of >) wrongly rejects. Real
// inputs hit this: e.g. the bundle's ~72-byte tegra264-mb1-bct-cprod DTB.
func TestParseFDT_EmptyStrings(t *testing.T) {
	be := binary.BigEndian
	var st []byte
	app := func(v uint32) {
		var b [4]byte
		be.PutUint32(b[:], v)
		st = append(st, b[:]...)
	}
	app(fdtBeginNode)
	st = append(st, 0, 0, 0, 0) // empty (root) name, 4-byte aligned
	app(fdtEndNode)
	app(fdtEnd)

	const headerSize = 40
	offStruct := uint32(headerSize)
	offStrings := offStruct + uint32(len(st)) // strings block is empty -> == totalsize
	totalSize := offStrings

	var hdr [40]byte
	be.PutUint32(hdr[0:], 0xd00dfeed)
	be.PutUint32(hdr[4:], totalSize)
	be.PutUint32(hdr[8:], offStruct)
	be.PutUint32(hdr[12:], offStrings)
	be.PutUint32(hdr[16:], headerSize)
	be.PutUint32(hdr[20:], 17)
	be.PutUint32(hdr[24:], 16)
	be.PutUint32(hdr[32:], 0) // size_dt_strings = 0
	be.PutUint32(hdr[36:], uint32(len(st)))

	blob := append(append([]byte{}, hdr[:]...), st...)
	fdt, err := dtb.ParseFDT(blob)
	if err != nil {
		t.Fatalf("ParseFDT(empty-strings DTB): %v", err)
	}
	if !fdt.HasNode("/") {
		t.Error("root node not found in empty DTB")
	}
	if _, ok := fdt.PropertyU32("/", "nope"); ok {
		t.Error("unexpected property in empty DTB")
	}
}

// TestParseFDT_BadMagic verifies that a blob with a wrong magic number is
// rejected with an appropriate error.
func TestParseFDT_BadMagic(t *testing.T) {
	data := make([]byte, 40)
	binary.BigEndian.PutUint32(data[0:], 0xdeadbeef) // wrong magic
	if _, err := dtb.ParseFDT(data); err == nil {
		t.Error("ParseFDT with bad magic: want error, got nil")
	}
}

// buildMinimalDTB constructs a valid v17 DTB by hand.
//
// Struct block encodes:
//
//	FDT_BEGIN_NODE ""           (root)
//	  FDT_PROP answer <0x42>
//	  FDT_PROP label  "ok\0"
//	  FDT_BEGIN_NODE "nested"
//	    FDT_PROP value <0x7>
//	  FDT_END_NODE
//	FDT_END_NODE
//	FDT_END
func buildMinimalDTB(t *testing.T) []byte {
	t.Helper()

	be := binary.BigEndian

	// Strings block: "answer\0label\0value\0"
	// Offsets: answer=0, label=7, value=13
	strings_ := []byte("answer\x00label\x00value\x00")
	const (
		offAnswer = 0
		offLabel  = 7
		offValue  = 13
	)

	// Build the struct block token by token.
	var st []byte

	appendU32 := func(v uint32) {
		var buf [4]byte
		be.PutUint32(buf[:], v)
		st = append(st, buf[:]...)
	}
	// Append a NUL-terminated, 4-byte-aligned node name.
	appendName := func(name string) {
		raw := append([]byte(name), 0)
		for len(raw)%4 != 0 {
			raw = append(raw, 0)
		}
		st = append(st, raw...)
	}
	// Append a property: token, len, nameoff, value (padded).
	appendProp := func(nameOff uint32, value []byte) {
		appendU32(fdtProp)
		appendU32(uint32(len(value)))
		appendU32(nameOff)
		padded := make([]byte, (len(value)+3)&^3)
		copy(padded, value)
		st = append(st, padded...)
	}

	// Root begin.
	appendU32(fdtBeginNode)
	appendName("") // root has empty name

	// answer = <0x42>
	var answerVal [4]byte
	be.PutUint32(answerVal[:], 0x42)
	appendProp(offAnswer, answerVal[:])

	// label = "ok\0"
	appendProp(offLabel, []byte("ok\x00"))

	// nested begin.
	appendU32(fdtBeginNode)
	appendName("nested")

	// value = <0x7>
	var valueVal [4]byte
	be.PutUint32(valueVal[:], 0x7)
	appendProp(offValue, valueVal[:])

	// nested end.
	appendU32(fdtEndNode)

	// Root end.
	appendU32(fdtEndNode)

	// FDT_END.
	appendU32(fdtEnd)

	// DTB header (10 × u32 = 40 bytes).
	const headerSize = 40
	offStruct := uint32(headerSize)
	offStrings := offStruct + uint32(len(st))
	totalSize := offStrings + uint32(len(strings_))

	var hdr [40]byte
	be.PutUint32(hdr[0:], 0xd00dfeed)          // magic
	be.PutUint32(hdr[4:], totalSize)            // totalsize
	be.PutUint32(hdr[8:], offStruct)            // off_dt_struct
	be.PutUint32(hdr[12:], offStrings)          // off_dt_strings
	be.PutUint32(hdr[16:], uint32(headerSize))  // off_mem_rsvmap (point at header end; empty)
	be.PutUint32(hdr[20:], 17)                  // version
	be.PutUint32(hdr[24:], 16)                  // last_comp_version
	be.PutUint32(hdr[28:], 0)                   // boot_cpuid_phys
	be.PutUint32(hdr[32:], uint32(len(strings_))) // size_dt_strings
	be.PutUint32(hdr[36:], uint32(len(st)))     // size_dt_struct

	blob := make([]byte, 0, int(totalSize))
	blob = append(blob, hdr[:]...)
	blob = append(blob, st...)
	blob = append(blob, strings_...)
	return blob
}

// fdtProp mirrors the constant from fdt.go; we redefine it here so the test
// file can construct struct blocks without importing unexported symbols.
const fdtProp = 0x00000003

// fdtBeginNode and fdtEndNode are used by buildMinimalDTB.
const (
	fdtBeginNode = 0x00000001
	fdtEndNode   = 0x00000002
	fdtEnd       = 0x00000009
)
