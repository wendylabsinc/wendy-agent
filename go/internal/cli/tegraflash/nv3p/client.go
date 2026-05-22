// nv3p protocol client, translated from NVIDIA tegrarcm nv3p.c
// (BSD 3-Clause License, Copyright (c) 2011 NVIDIA CORPORATION)
//
// Packet format (all little-endian, all uint32):
//
// v1 (T234 applet, 12-byte header):
//
//	CMD:  [ver=1][type][seq][args_len][command][args...][checksum]
//	ACK:  [ver=1][type=4][seq][checksum]
//	NACK: [ver=1][type=5][seq][code][checksum]
//
// v3 (T264 bootROM and MB1, 16-byte header, ACK=3, NACK=4):
//
//	CMD:  [ver=3][type=1][seq][rsvd=0][args_len][command][args...][checksum]
//	ACK:  [ver=3][type=3][seq][rsvd=0][checksum]
//	NACK: [ver=3][type=4][seq][rsvd=0][code][checksum]
//
// Checksum: ~(sum of all preceding bytes) + 1  (two's complement of byte sum)
package nv3p

import (
	"encoding/binary"
	"fmt"
	"io"
)

// transport is satisfied by rcm.Device after the applet is running.
type transport interface {
	Read([]byte) (int, error)
	Write([]byte) error
}

// Client implements the nv3p protocol over a bulk USB transport.
type Client struct {
	t          transport
	sequence   uint32
	recvSeq    uint32
	version    uint32
	basicHdrSz int    // 12 for v1, 16 for v3
	ackType    uint32 // PacketTypeACK (v1=4) or PacketTypeACKv3 (v3=3)
	nackType   uint32 // PacketTypeNACK (v1=5) or PacketTypeNACKv3 (v3=4)

	// buffered reader state (device pads reads with extra bytes)
	buf    [4096]byte
	bufOff int
	bufLen int
}

// NewClient opens a v1 nv3p session (T234 applet).
func NewClient(t transport) (*Client, error) {
	return &Client{t: t, version: Version, basicHdrSz: sizeBasic, ackType: PacketTypeACK, nackType: PacketTypeNACK}, nil
}

// NewClientT264 opens a v3 nv3p session (T264 bootROM and MB1).
// T264 uses protocol version 3 with a 16-byte basic header (extra reserved DWORD
// after sequence) and shifted packet type encoding: ACK=3, NACK=4.
func NewClientT264(t transport) (*Client, error) {
	return &Client{t: t, version: VersionT264, basicHdrSz: sizeBasicV3, ackType: PacketTypeACKv3, nackType: PacketTypeNACKv3}, nil
}

// GetPlatformInfo sends NV3P_CMD_GET_PLATFORM_INFO and returns device info.
func (c *Client) GetPlatformInfo() (*PlatformInfo, error) {
	if err := c.sendCmd(CmdGetPlatformInfo, nil); err != nil {
		return nil, err
	}

	var info PlatformInfo
	raw := make([]byte, 6*8+4*4+2*4+4+4*4+4*4) // match nv3p_platform_info_t
	if err := c.recvData(raw); err != nil {
		return nil, err
	}

	r := raw
	for i := range info.UID {
		info.UID[i] = binary.LittleEndian.Uint64(r[:8])
		r = r[8:]
	}
	info.ChipID.ID = binary.LittleEndian.Uint16(r[:2])
	info.ChipID.Major = r[2]
	info.ChipID.Minor = r[3]
	r = r[4:]
	info.SKU = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.Version = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.BootDevice = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.OpMode = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.DevConfStrap = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.DevConfFuse = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.SDRAMConfStrap = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	r = r[8:] // reserved[2]
	info.BoardID.BoardNo = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.BoardID.Fab = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.BoardID.MemType = binary.LittleEndian.Uint32(r[:4])
	r = r[4:]
	info.BoardID.Freq = binary.LittleEndian.Uint32(r[:4])

	return &info, nil
}

// DownloadBL downloads a bootloader binary to the device, then executes it.
// addr is the SDRAM load address; entry is the execution entry point.
// After this call the device begins executing the payload — no further nv3p
// communication is expected.
func (c *Client) DownloadBL(payload []byte, addr, entry uint32) error {
	args := DLBLArgs{
		Length:  uint64(len(payload)),
		Address: addr,
		Entry:   entry,
	}
	argBytes := make([]byte, 16) // 8+4+4
	binary.LittleEndian.PutUint64(argBytes[0:], args.Length)
	binary.LittleEndian.PutUint32(argBytes[8:], args.Address)
	binary.LittleEndian.PutUint32(argBytes[12:], args.Entry)

	if err := c.sendCmd(CmdDLBL, argBytes); err != nil {
		return err
	}
	return c.sendData(payload)
}

// DlBCT downloads a Boot Configuration Table binary to the device.
// The BCT must be loaded before any partition writes.
func (c *Client) DlBCT(bct []byte) error {
	argBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(argBytes, uint32(len(bct)))
	if err := c.sendCmd(CmdDLBCT, argBytes); err != nil {
		return err
	}
	return c.sendData(bct)
}

// WritePartition downloads a partition image to the device and writes it to flash.
// id is the partition ID from the partition layout XML.
// partType: use 0x01 for generic partition data.
// TODO: verify DLPartitionArgs layout and CmdDLPartition opcode against T234 hardware.
func (c *Client) WritePartition(id, partType uint32, data []byte) error {
	args := make([]byte, 16)
	binary.LittleEndian.PutUint64(args[0:], uint64(len(data)))
	binary.LittleEndian.PutUint32(args[8:], id)
	binary.LittleEndian.PutUint32(args[12:], partType)
	if err := c.sendCmd(CmdDLPartition, args); err != nil {
		return err
	}
	return c.sendData(data)
}

// Reset sends a reset command to restart the device after flashing.
func (c *Client) Reset() error {
	return c.sendCmd(CmdReset, nil)
}

// IsAppletT264 performs the GetPlatformInfo handshake with the T264 MB1 applet.
// The exchange (from NvTegraRcmIsApplet disassembly) is:
//   host→device: CMD(cmd=1, size=4)
//   device→host: RESPONSE_CMD(type=9, data_size=16)   ← consumed by sendCmd's waitACK
//   device→host: DATA(16 bytes of platform info)
//   device→host: STATUS_CMD(type=8)
// Returns true when the device is in applet mode (status byte == 4).
func (c *Client) IsAppletT264() (bool, error) {
	if err := c.sendCmd(CmdGetPlatformInfo, nil); err != nil {
		return false, err
	}
	// RESPONSE_CMD was already consumed by waitACK inside sendCmd.
	data := make([]byte, 16)
	if err := c.recvData(data); err != nil {
		return false, fmt.Errorf("IsAppletT264 data: %w", err)
	}
	if _, _, err := c.recvDevicePkt(); err != nil {
		return false, fmt.Errorf("IsAppletT264 status cmd: %w", err)
	}
	status := binary.LittleEndian.Uint32(data[0:4])
	return status == 4, nil
}

// DownloadT264File sends a T264-style file download (nv3p command 0x2).
// typeName is the download type identifier used by tegrarcm_v2 ("bct_mem", "blob", etc.).
//
// For T264 (v3 protocol), the exchange (from NvTegraRcmAppletDownload disassembly) is:
//   host→device: CMD(2, args, size=60)
//   device→host: RESPONSE_CMD(type=9, data_size=0)   ← consumed by sendCmd's waitACK
//   host→device: DATA(file bytes)
//   device→host: STATUS_CMD(type=8)
func (c *Client) DownloadT264File(typeName string, data []byte) error {
	// Struct layout (56 bytes, matching tegrarcm_v2 NvTegraRcmAppletDownload):
	//   [0x00..0x07] file_size uint64
	//   [0x08..0x0b] address   uint32 (0 for bct_mem/blob)
	//   [0x0c..0x0f] entry     uint32 (0 for bct_mem/blob)
	//   [0x10..0x37] type_name [40]byte null-terminated
	args := make([]byte, 56)
	binary.LittleEndian.PutUint64(args[0:], uint64(len(data)))
	copy(args[16:], typeName)
	if err := c.sendCmd(CmdDownloadT264, args); err != nil {
		return err
	}
	// RESPONSE_CMD consumed by waitACK inside sendCmd.
	if err := c.sendData(data); err != nil {
		return err
	}
	if c.basicHdrSz == sizeBasicV3 {
		if _, _, err := c.recvDevicePkt(); err != nil {
			return fmt.Errorf("T264 download %s status: %w", typeName, err)
		}
	}
	return nil
}

// PollMB1 sends GetPlatformInfo and reads the minimal 16-byte T264 MB1
// response. Returns true when the device is ready for downloads.
func (c *Client) PollMB1() (bool, error) {
	if err := c.sendCmd(CmdGetPlatformInfo, nil); err != nil {
		return false, err
	}
	// T264 MB1 responds with exactly 16 bytes of platform info.
	resp := make([]byte, 16)
	if err := c.recvData(resp); err != nil {
		return false, err
	}
	// Check that byte 12 (4th uint32) has value 0x4 (applet mode).
	marker := binary.LittleEndian.Uint32(resp[12:])
	return marker == 0x4, nil
}

// sendCmd serialises and sends a CMD packet, then waits for ACK/NACK.
func (c *Client) sendCmd(cmd uint32, args []byte) error {
	argsLen := uint32(len(args))

	// v3 (T264): the size field is total body bytes = cmd_id (4) + args.
	// Confirmed from NvTegra3pSend disassembly: it accumulates scatter sizes
	// starting with the 4-byte cmd_id, so the size field sent over the wire is
	// always 4 + len(args).
	// v1 (T234): size field is args only (cmd_id not included).
	sizeField := argsLen
	if c.basicHdrSz == sizeBasicV3 {
		sizeField += 4
	}

	// Build packet: basic(12 or 16) + command(8) + args + footer(4)
	pktLen := c.basicHdrSz + sizeCommand + int(argsLen) + sizeFooter
	pkt := make([]byte, pktLen)
	p := pkt

	c.writeHeader(PacketTypeCmd, c.sequence, p)
	p = p[c.basicHdrSz:]

	binary.LittleEndian.PutUint32(p[:4], sizeField)
	binary.LittleEndian.PutUint32(p[4:], cmd)
	p = p[sizeCommand:]

	copy(p, args)
	p = p[argsLen:]

	// v3: checksum covers header + cmd_id + args but NOT the 4-byte sizeField.
	// v1: checksum covers the entire packet body (header + sizeField + cmd_id + args).
	// Confirmed by NvTegra3pSend disassembly: NvTegraComputeChecksum is called on
	// the header (16 B) and then on each scatter entry, never on the size-field dword.
	var checksum uint32
	if c.basicHdrSz == sizeBasicV3 {
		checksum = cksum(pkt[:c.basicHdrSz]) + cksum(pkt[c.basicHdrSz+4:pktLen-sizeFooter])
	} else {
		checksum = cksum(pkt[:pktLen-sizeFooter])
	}
	binary.LittleEndian.PutUint32(p[:4], twosComplement(checksum))

	// tegrarcm_v2 (NvTegra3pSend) sends each nv3p packet as 4 separate USB bulk
	// transfers. The T264 bootROM USB stack processes each transfer discretely, so
	// combining them into one write causes the device to never respond.
	if err := c.t.Write(pkt[:c.basicHdrSz]); err != nil {
		return fmt.Errorf("nv3p send cmd header: %w", err)
	}
	if err := c.t.Write(pkt[c.basicHdrSz : c.basicHdrSz+4]); err != nil {
		return fmt.Errorf("nv3p send cmd size: %w", err)
	}
	if err := c.t.Write(pkt[c.basicHdrSz+4 : pktLen-sizeFooter]); err != nil {
		return fmt.Errorf("nv3p send cmd 0x%02x body: %w", cmd, err)
	}
	if err := c.t.Write(pkt[pktLen-sizeFooter:]); err != nil {
		return fmt.Errorf("nv3p send cmd checksum: %w", err)
	}
	c.sequence++

	return c.waitACK()
}

// sendData serialises and sends a DATA packet, then waits for ACK/NACK.
func (c *Client) sendData(data []byte) error {
	hdrLen := c.basicHdrSz + sizeData
	pkt := make([]byte, hdrLen+sizeFooter)
	c.writeHeader(PacketTypeData, c.sequence, pkt)
	binary.LittleEndian.PutUint32(pkt[c.basicHdrSz:], uint32(len(data)))

	// v3: checksum covers header + data but NOT the 4-byte data_len field.
	// v1: checksum covers header + data_len + data.
	var sum uint32
	if c.basicHdrSz == sizeBasicV3 {
		sum = cksum(pkt[:c.basicHdrSz])
	} else {
		sum = cksum(pkt[:hdrLen])
	}
	sum += cksum(data)
	binary.LittleEndian.PutUint32(pkt[hdrLen:], twosComplement(sum))

	// Same 4-transfer scatter-gather as sendCmd (matching tegrarcm_v2 NvTegra3pSend).
	if err := c.t.Write(pkt[:c.basicHdrSz]); err != nil {
		return fmt.Errorf("nv3p send data header: %w", err)
	}
	if err := c.t.Write(pkt[c.basicHdrSz:hdrLen]); err != nil {
		return fmt.Errorf("nv3p send data size: %w", err)
	}
	if err := c.t.Write(data); err != nil {
		return fmt.Errorf("nv3p send data body: %w", err)
	}
	if err := c.t.Write(pkt[hdrLen:]); err != nil {
		return fmt.Errorf("nv3p send data footer: %w", err)
	}
	c.sequence++

	return c.waitACK()
}

// recvData reads a DATA packet from the device and copies the payload into dst.
func (c *Client) recvData(dst []byte) error {
	hdr, accumSum, err := c.recvHeader()
	if err != nil {
		return err
	}
	if hdr.pktType != PacketTypeData {
		return fmt.Errorf("nv3p: expected DATA packet, got type %d", hdr.pktType)
	}
	c.recvSeq = hdr.sequence

	var dataLenBuf [4]byte
	if err := c.readExact(dataLenBuf[:]); err != nil {
		return fmt.Errorf("nv3p recv data length: %w", err)
	}
	dataLen := binary.LittleEndian.Uint32(dataLenBuf[:])
	// v3: data_len field not included in checksum (matches NvTegra3pSend behaviour).
	if c.basicHdrSz != sizeBasicV3 {
		accumSum += cksum(dataLenBuf[:])
	}

	buf := make([]byte, dataLen)
	if err := c.readExact(buf); err != nil {
		return fmt.Errorf("nv3p recv data body: %w", err)
	}
	accumSum += cksum(buf)

	var footerBuf [4]byte
	if err := c.readExact(footerBuf[:]); err != nil {
		return fmt.Errorf("nv3p recv data footer: %w", err)
	}
	footer := binary.LittleEndian.Uint32(footerBuf[:])
	if accumSum+footer != 0 {
		return fmt.Errorf("nv3p recv data: checksum mismatch")
	}

	copy(dst, buf[:min(len(dst), len(buf))])
	c.sendACK()
	return nil
}

// recvDevicePkt reads a device-initiated packet (RESPONSE_CMD type=9 or STATUS_CMD
// type=8), validates its checksum, sends ACK, and returns the pktType and payload.
//
// After the host sends a CMD and receives an ACK, the T264 bootROM follows with
// a RESPONSE_CMD (type=9) carrying the data_size of the pending DATA transfer.
// After the host sends DATA and receives an ACK, the device sends STATUS_CMD (type=8).
func (c *Client) recvDevicePkt() (pktType uint32, payload []byte, err error) {
	hdr, accumSum, err := c.recvHeader()
	if err != nil {
		return 0, nil, err
	}
	c.recvSeq = hdr.sequence

	var sizeBuf [4]byte
	if err := c.readExact(sizeBuf[:]); err != nil {
		return 0, nil, fmt.Errorf("nv3p recv device pkt size: %w", err)
	}
	size := binary.LittleEndian.Uint32(sizeBuf[:])
	// v3: size field not included in checksum.
	if c.basicHdrSz != sizeBasicV3 {
		accumSum += cksum(sizeBuf[:])
	}

	payload = make([]byte, size)
	if err := c.readExact(payload); err != nil {
		return 0, nil, fmt.Errorf("nv3p recv device pkt payload: %w", err)
	}
	accumSum += cksum(payload)

	var footerBuf [4]byte
	if err := c.readExact(footerBuf[:]); err != nil {
		return 0, nil, fmt.Errorf("nv3p recv device pkt footer: %w", err)
	}
	footer := binary.LittleEndian.Uint32(footerBuf[:])
	if accumSum+footer != 0 {
		return 0, nil, fmt.Errorf("nv3p recv device pkt type=%d: checksum mismatch", hdr.pktType)
	}

	c.sendACK()
	return hdr.pktType, payload, nil
}

// waitACK reads the ACK or NACK response after sending a command/data packet.
func (c *Client) waitACK() error {
	hdr, accumSum, err := c.recvHeader()
	if err != nil {
		return err
	}

	switch {
	case hdr.pktType == c.ackType:
		// nothing extra to read

	case hdr.pktType == c.nackType:
		var codeBuf [4]byte
		if err := c.readExact(codeBuf[:]); err != nil {
			return err
		}
		code := binary.LittleEndian.Uint32(codeBuf[:])
		accumSum += cksum(codeBuf[:])
		var footerBuf [4]byte
		if err := c.readExact(footerBuf[:]); err != nil {
			return err
		}
		footer := binary.LittleEndian.Uint32(footerBuf[:])
		if accumSum+footer != 0 {
			return fmt.Errorf("nv3p nack: checksum mismatch")
		}
		return fmt.Errorf("nv3p NACK code 0x%x", code)

	default:
		if c.basicHdrSz != sizeBasicV3 {
			return fmt.Errorf("nv3p: unexpected packet type %d waiting for ACK", hdr.pktType)
		}
		// v3: device sends RESPONSE_CMD (type=9) in place of ACK after CMD.
		// Format: [header(16B)][size_field(4B)][checksum(4B)].
		// size_field is informational (data size for next DATA transfer), not payload.
		// Matches NvTegra3pWaitForAck behaviour: accept any type != NACK.
		c.recvSeq = hdr.sequence
		var sizeBuf [4]byte
		if err := c.readExact(sizeBuf[:]); err != nil {
			return fmt.Errorf("nv3p response type=%d size: %w", hdr.pktType, err)
		}
		var footerBuf [4]byte
		if err := c.readExact(footerBuf[:]); err != nil {
			return fmt.Errorf("nv3p response type=%d footer: %w", hdr.pktType, err)
		}
		footer := binary.LittleEndian.Uint32(footerBuf[:])
		if accumSum+footer != 0 {
			return fmt.Errorf("nv3p response type=%d: checksum mismatch", hdr.pktType)
		}
		c.sendACK()
		return nil
	}

	var footerBuf [4]byte
	if err := c.readExact(footerBuf[:]); err != nil {
		return err
	}
	footer := binary.LittleEndian.Uint32(footerBuf[:])
	if accumSum+footer != 0 {
		return fmt.Errorf("nv3p ack: checksum mismatch")
	}
	if hdr.sequence != c.sequence-1 {
		return fmt.Errorf("nv3p ack: sequence mismatch (got %d, want %d)", hdr.sequence, c.sequence-1)
	}
	return nil
}

// sendACK sends an ACK packet for the last received sequence number.
func (c *Client) sendACK() {
	pkt := make([]byte, c.basicHdrSz+sizeFooter)
	c.writeHeader(c.ackType, c.recvSeq, pkt)
	sum := cksum(pkt[:c.basicHdrSz])
	binary.LittleEndian.PutUint32(pkt[c.basicHdrSz:], twosComplement(sum))
	_ = c.t.Write(pkt)
}

type header struct {
	version  uint32
	pktType  uint32
	sequence uint32
}

// recvHeader reads the basic header (12 or 16 bytes depending on version) and
// returns it with the running checksum.
func (c *Client) recvHeader() (header, uint32, error) {
	buf := make([]byte, c.basicHdrSz)
	if err := c.readExact(buf); err != nil {
		return header{}, 0, fmt.Errorf("nv3p recv header: %w", err)
	}
	hdr := header{
		version:  binary.LittleEndian.Uint32(buf[0:4]),
		pktType:  binary.LittleEndian.Uint32(buf[4:8]),
		sequence: binary.LittleEndian.Uint32(buf[8:12]),
		// buf[12:16] (v3 reserved field) is consumed but not stored
	}
	if hdr.version != c.version {
		return header{}, 0, fmt.Errorf("nv3p: protocol version mismatch (got %d, want %d)", hdr.version, c.version)
	}
	return hdr, cksum(buf), nil
}

// readExact fills buf completely, buffering excess bytes for the next call.
// The device sometimes pads responses; this matches the buffering in tegrarcm nv3p_read().
func (c *Client) readExact(buf []byte) error {
	total := 0
	for total < len(buf) {
		if c.bufLen == 0 {
			n, err := c.t.Read(c.buf[:])
			if err != nil && err != io.EOF {
				return err
			}
			c.bufOff = 0
			c.bufLen = n
		}
		avail := c.bufLen
		need := len(buf) - total
		if avail > need {
			avail = need
		}
		copy(buf[total:], c.buf[c.bufOff:c.bufOff+avail])
		c.bufOff += avail
		c.bufLen -= avail
		total += avail
	}
	return nil
}

// writeHeader writes the basic header into pkt.
// v1: 12 bytes {version, type, seq}
// v3: 16 bytes {version, type, seq, reserved=0}
func (c *Client) writeHeader(pktType, sequence uint32, pkt []byte) {
	binary.LittleEndian.PutUint32(pkt[0:], c.version)
	binary.LittleEndian.PutUint32(pkt[4:], pktType)
	binary.LittleEndian.PutUint32(pkt[8:], sequence)
	if c.basicHdrSz >= sizeBasicV3 {
		binary.LittleEndian.PutUint32(pkt[12:], 0)
	}
}

// cksum is the nv3p checksum: sum of all bytes (uint32 wrapping).
func cksum(data []byte) uint32 {
	var s uint32
	for _, b := range data {
		s += uint32(b)
	}
	return s
}

// twosComplement returns ^sum + 1 so that sum + result == 0 in uint32.
func twosComplement(sum uint32) uint32 {
	return ^sum + 1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
