package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/nv3p"
)

type capTransport struct {
	sent   [][]byte
	ackBuf []byte
	ackOff int
	reads  int
}

func makeACK(seq uint32) []byte {
	ack := make([]byte, 20)
	binary.LittleEndian.PutUint32(ack[0:], 3)
	binary.LittleEndian.PutUint32(ack[4:], 3) // ACKv3
	binary.LittleEndian.PutUint32(ack[8:], seq)
	binary.LittleEndian.PutUint32(ack[12:], 0)
	var sum uint32
	for _, b := range ack[:16] {
		sum += uint32(b)
	}
	binary.LittleEndian.PutUint32(ack[16:], ^sum+1)
	return ack
}

func (c *capTransport) Write(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	c.sent = append(c.sent, cp)
	return nil
}

func (c *capTransport) Read(b []byte) (int, error) {
	c.reads++
	if c.ackOff < len(c.ackBuf) {
		n := copy(b, c.ackBuf[c.ackOff:])
		c.ackOff += n
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func hexDump(data []byte, name string) {
	fmt.Printf("=== %s (%d bytes) ===\n", name, len(data))
	fmt.Println(hex.Dump(data))
}

func main() {
	t := &capTransport{}
	// Provide ACKs for both CMD and DATA sends (seq=0 and seq=1)
	ack0 := makeACK(0)
	ack1 := makeACK(1)
	t.ackBuf = append(ack0, ack1...)

	client, _ := nv3p.NewClientT264(t)
	bctData := make([]byte, 8192)
	err := client.DownloadT264File("bct_br", bctData)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	}

	fmt.Printf("Total packets sent: %d\n\n", len(t.sent))
	for i, pkt := range t.sent {
		hexDump(pkt, fmt.Sprintf("Sent[%d]", i))
	}
}
