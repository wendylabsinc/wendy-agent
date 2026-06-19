package dtb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// FDT token constants as defined in the Devicetree specification.
const (
	fdtMagic     = 0xd00dfeed
	fdtBeginNode = 0x00000001
	fdtEndNode   = 0x00000002
	fdtProp      = 0x00000003
	fdtNop       = 0x00000004
	fdtEnd       = 0x00000009
)

// fdtHeader mirrors the 10-field fixed header at the start of every DTB blob.
// All fields are big-endian u32 per the Devicetree spec v0.3 §5.2.
type fdtHeader struct {
	Magic           uint32
	TotalSize       uint32
	OffDtStruct     uint32
	OffDtStrings    uint32
	OffMemRsvmap    uint32
	Version         uint32
	LastCompVersion uint32
	BootCPUIDPhys   uint32
	SizeDtStrings   uint32
	SizeDtStruct    uint32
}

// fdtNode holds the pre-parsed in-memory representation of a single device
// tree node.
type fdtNode struct {
	name       string            // bare node name (without parent path)
	properties map[string][]byte // property name -> raw bytes
	children   []*fdtNode        // ordered list of immediate children
}

// FDT is a parsed Flattened Device Tree blob. It exposes a read-only API for
// querying node properties and structure.
type FDT struct {
	root *fdtNode
}

// ParseFDT parses a compiled DTB blob produced by dtc and returns an FDT
// ready for property queries. It accepts any blob version >= 16 (the last
// compatible version) and validates the magic number and basic bounds.
func ParseFDT(data []byte) (*FDT, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("fdt: blob too short (%d bytes)", len(data))
	}

	var hdr fdtHeader
	if err := binary.Read(bytes.NewReader(data[:40]), binary.BigEndian, &hdr); err != nil {
		return nil, fmt.Errorf("fdt: header read: %w", err)
	}

	if hdr.Magic != fdtMagic {
		return nil, fmt.Errorf("fdt: bad magic 0x%08x (want 0x%08x)", hdr.Magic, fdtMagic)
	}
	if hdr.LastCompVersion > 17 || hdr.LastCompVersion < 16 {
		// Accept blobs that are compatible with version 16 (the oldest
		// version that current dtc still emits v17 blobs for).
		// The spec says last_comp_version must be <= current version;
		// we require at least 16 since that is the first version with a
		// well-defined struct layout.
		if hdr.LastCompVersion < 16 {
			return nil, fmt.Errorf("fdt: last compatible version %d < 16", hdr.LastCompVersion)
		}
	}

	if int(hdr.OffDtStruct) >= len(data) || int(hdr.OffDtStrings) >= len(data) {
		return nil, fmt.Errorf("fdt: offsets out of range (struct=%d, strings=%d, total=%d)",
			hdr.OffDtStruct, hdr.OffDtStrings, len(data))
	}

	structBlock := data[hdr.OffDtStruct:]
	stringsBlock := data[hdr.OffDtStrings:]

	// Apply size bounds if the blob is version 17+ (SizeDtStruct is defined
	// starting at version 17).
	if hdr.Version >= 17 && hdr.SizeDtStruct > 0 {
		end := hdr.OffDtStruct + hdr.SizeDtStruct
		if int(end) > len(data) {
			return nil, fmt.Errorf("fdt: struct block exceeds blob (end=%d, total=%d)", end, len(data))
		}
		structBlock = structBlock[:hdr.SizeDtStruct]
	}
	if hdr.SizeDtStrings > 0 {
		end := hdr.OffDtStrings + hdr.SizeDtStrings
		if int(end) > len(data) {
			return nil, fmt.Errorf("fdt: strings block exceeds blob (end=%d, total=%d)", end, len(data))
		}
		stringsBlock = stringsBlock[:hdr.SizeDtStrings]
	}

	p := &fdtParser{
		struct_:  structBlock,
		strings_: stringsBlock,
		pos:      0,
	}

	root, err := p.parseRoot()
	if err != nil {
		return nil, fmt.Errorf("fdt: parse: %w", err)
	}

	return &FDT{root: root}, nil
}

// fdtParser holds the mutable parse state.
type fdtParser struct {
	struct_  []byte
	strings_ []byte
	pos      int
}

// u32 reads the next big-endian u32 from the struct block and advances pos.
func (p *fdtParser) u32() (uint32, error) {
	if p.pos+4 > len(p.struct_) {
		return 0, fmt.Errorf("unexpected end of struct block at offset %d", p.pos)
	}
	v := binary.BigEndian.Uint32(p.struct_[p.pos:])
	p.pos += 4
	return v, nil
}

// skipNOPs skips any FDT_NOP tokens at the current position.
func (p *fdtParser) skipNOPs() error {
	for p.pos+4 <= len(p.struct_) {
		tok := binary.BigEndian.Uint32(p.struct_[p.pos:])
		if tok != fdtNop {
			return nil
		}
		p.pos += 4
	}
	return nil
}

// readName reads a NUL-terminated node name from the struct block starting at
// pos and advances pos to the next 4-byte-aligned position after the NUL.
func (p *fdtParser) readName() (string, error) {
	start := p.pos
	end := bytes.IndexByte(p.struct_[start:], 0)
	if end < 0 {
		return "", fmt.Errorf("unterminated node name at offset %d", start)
	}
	name := string(p.struct_[start : start+end])
	// Advance past the NUL and pad to 4-byte alignment.
	raw := end + 1
	p.pos = start + align4(raw)
	return name, nil
}

// stringAt returns the NUL-terminated string at the given offset into the
// strings block.
func (p *fdtParser) stringAt(off uint32) (string, error) {
	if int(off) >= len(p.strings_) {
		return "", fmt.Errorf("string offset %d out of range (%d)", off, len(p.strings_))
	}
	end := bytes.IndexByte(p.strings_[off:], 0)
	if end < 0 {
		return "", fmt.Errorf("unterminated string at strings+%d", off)
	}
	return string(p.strings_[off : int(off)+end]), nil
}

// parseRoot locates and parses the single root FDT_BEGIN_NODE at the top of
// the struct block, consuming NOPs and verifying FDT_END at the end.
func (p *fdtParser) parseRoot() (*fdtNode, error) {
	if err := p.skipNOPs(); err != nil {
		return nil, err
	}
	tok, err := p.u32()
	if err != nil {
		return nil, err
	}
	if tok != fdtBeginNode {
		return nil, fmt.Errorf("expected FDT_BEGIN_NODE (0x1) at start, got 0x%x", tok)
	}
	return p.parseNode()
}

// parseNode parses a single node. It is called after FDT_BEGIN_NODE has
// already been consumed. It reads the node name, then iterates tokens until
// the matching FDT_END_NODE.
func (p *fdtParser) parseNode() (*fdtNode, error) {
	name, err := p.readName()
	if err != nil {
		return nil, err
	}

	node := &fdtNode{
		name:       name,
		properties: make(map[string][]byte),
	}

	for {
		if err := p.skipNOPs(); err != nil {
			return nil, err
		}
		tok, err := p.u32()
		if err != nil {
			return nil, err
		}

		switch tok {
		case fdtBeginNode:
			child, err := p.parseNode()
			if err != nil {
				return nil, err
			}
			node.children = append(node.children, child)

		case fdtEndNode:
			return node, nil

		case fdtProp:
			// FDT_PROP is followed by: u32 len, u32 nameoff, then len bytes
			// of value padded to 4 bytes.
			propLen, err := p.u32()
			if err != nil {
				return nil, fmt.Errorf("prop len: %w", err)
			}
			nameOff, err := p.u32()
			if err != nil {
				return nil, fmt.Errorf("prop nameoff: %w", err)
			}
			propName, err := p.stringAt(nameOff)
			if err != nil {
				return nil, fmt.Errorf("prop name: %w", err)
			}

			end := p.pos + int(propLen)
			if end > len(p.struct_) {
				return nil, fmt.Errorf("prop %q value exceeds struct block", propName)
			}
			value := make([]byte, propLen)
			copy(value, p.struct_[p.pos:end])
			p.pos = p.pos + align4(int(propLen))
			node.properties[propName] = value

		case fdtEnd:
			return nil, fmt.Errorf("unexpected FDT_END inside node")

		default:
			return nil, fmt.Errorf("unknown token 0x%08x at offset %d", tok, p.pos-4)
		}
	}
}

// align4 returns n rounded up to the next multiple of 4.
func align4(n int) int {
	return (n + 3) &^ 3
}

// resolve walks the parsed tree along the given path and returns the matching
// node, or (nil, false) if not found. The root is addressed as "/".
// Paths are slash-separated: "/child@0/grandchild".
func (f *FDT) resolve(nodePath string) (*fdtNode, bool) {
	// Normalise: strip a trailing slash unless the path is exactly "/".
	path := nodePath
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	if path == "/" {
		return f.root, true
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false
	}

	parts := strings.Split(path[1:], "/") // skip leading "/"
	cur := f.root
	for _, part := range parts {
		var found *fdtNode
		for _, ch := range cur.children {
			if ch.name == part {
				found = ch
				break
			}
		}
		if found == nil {
			return nil, false
		}
		cur = found
	}
	return cur, true
}

// Property returns the raw bytes of a property within the node at nodePath.
// It returns (nil, false) if the node or property does not exist.
func (f *FDT) Property(nodePath, propName string) ([]byte, bool) {
	node, ok := f.resolve(nodePath)
	if !ok {
		return nil, false
	}
	v, found := node.properties[propName]
	return v, found
}

// PropertyU32 returns the property value interpreted as a big-endian u32.
// It returns (0, false) if the node or property does not exist or the value
// is not exactly 4 bytes.
func (f *FDT) PropertyU32(nodePath, propName string) (uint32, bool) {
	v, ok := f.Property(nodePath, propName)
	if !ok || len(v) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(v), true
}

// PropertyU32Array returns the property value as a slice of big-endian u32
// cells. It returns (nil, false) if the node or property does not exist or
// the value length is not a multiple of 4.
func (f *FDT) PropertyU32Array(nodePath, propName string) ([]uint32, bool) {
	v, ok := f.Property(nodePath, propName)
	if !ok || len(v)%4 != 0 {
		return nil, false
	}
	out := make([]uint32, len(v)/4)
	for i := range out {
		out[i] = binary.BigEndian.Uint32(v[i*4:])
	}
	return out, true
}

// PropertyString returns the property value as a Go string, stripping a
// trailing NUL if present (DTB string properties are NUL-terminated).
// It returns ("", false) if the node or property does not exist.
func (f *FDT) PropertyString(nodePath, propName string) (string, bool) {
	v, ok := f.Property(nodePath, propName)
	if !ok {
		return "", false
	}
	s := string(v)
	s = strings.TrimRight(s, "\x00")
	return s, true
}

// Children returns the names of the immediate child nodes of nodePath.
// It returns an error if nodePath does not exist.
func (f *FDT) Children(nodePath string) ([]string, error) {
	node, ok := f.resolve(nodePath)
	if !ok {
		return nil, fmt.Errorf("fdt: node %q not found", nodePath)
	}
	names := make([]string, len(node.children))
	for i, ch := range node.children {
		names[i] = ch.name
	}
	return names, nil
}

// HasNode reports whether the node at nodePath exists in the tree.
func (f *FDT) HasNode(nodePath string) bool {
	_, ok := f.resolve(nodePath)
	return ok
}
