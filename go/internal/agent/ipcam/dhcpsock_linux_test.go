//go:build linux

package ipcam

import (
	"net"
	"testing"
)

// buildIPv4UDP assembles a raw IPv4 datagram the way AF_PACKET SOCK_DGRAM
// delivers one: no ethernet header, starting at the IP header.
func buildIPv4UDP(t *testing.T, srcPort, dstPort uint16, payload []byte) []byte {
	t.Helper()
	total := 20 + 8 + len(payload)
	frame := make([]byte, total)
	frame[0] = 0x45 // version 4, header length 5 words
	frame[2] = byte(total >> 8)
	frame[3] = byte(total)
	frame[8] = 64 // time to live
	frame[9] = 17 // UDP
	copy(frame[12:16], net.IPv4zero.To4())
	copy(frame[16:20], net.IPv4bcast.To4())
	udp := frame[20:]
	udp[0], udp[1] = byte(srcPort>>8), byte(srcPort)
	udp[2], udp[3] = byte(dstPort>>8), byte(dstPort)
	length := 8 + len(payload)
	udp[4], udp[5] = byte(length>>8), byte(length)
	copy(udp[8:], payload)
	return frame
}

func TestHtons(t *testing.T) {
	// ETH_P_IP is 0x0800; AF_PACKET needs it byte-swapped, and passing host order
	// silently matches no packets at all.
	if got := htons(0x0800); got != 0x0008 {
		t.Fatalf("htons(0x0800) = %#04x, want 0x0008", got)
	}
	if got := htons(67); got != 0x4300 {
		t.Fatalf("htons(67) = %#04x", got)
	}
}

// A DISCOVER from a camera with no address is the packet this whole path exists
// to see.
func TestUDPPayloadExtractsDiscover(t *testing.T) {
	mac, err := net.ParseMAC("ec:71:db:2a:ae:7e")
	if err != nil {
		t.Fatalf("parse mac: %v", err)
	}
	discover := BuildDiscover(1, mac, "RLC-520A")
	frame := buildIPv4UDP(t, dhcpClientPort, dhcpServerPort, discover)

	payload, ok := udpPayload(frame)
	if !ok {
		t.Fatal("DHCP DISCOVER was not recognised")
	}
	p, err := ParsePacket(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Type != Discover || p.Hostname != "RLC-520A" {
		t.Fatalf("packet = %+v", p)
	}
}

// A competing server's OFFER is addressed to port 68. Missing it would defeat the
// guard, so it must be recognised too.
func TestUDPPayloadAcceptsServerToClientTraffic(t *testing.T) {
	mac := net.HardwareAddr{1, 2, 3, 4, 5, 6}
	req, err := ParsePacket(BuildDiscover(1, mac, "cam"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	offer := BuildReply(req, Offer, ReplyConfig{
		ServerIP: net.IPv4(192, 168, 0, 1),
		ClientIP: net.IPv4(192, 168, 0, 50),
		Mask:     net.CIDRMask(24, 32),
	})
	frame := buildIPv4UDP(t, dhcpServerPort, dhcpClientPort, offer)

	payload, ok := udpPayload(frame)
	if !ok {
		t.Fatal("an OFFER to the client port was not recognised")
	}
	p, err := ParsePacket(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Type != Offer {
		t.Fatalf("type = %v, want OFFER", p.Type)
	}
}

// The watcher reads from an untrusted link, so every malformed shape must be
// rejected without panicking.
func TestUDPPayloadRejectsNonDHCP(t *testing.T) {
	payload := []byte("payload")
	cases := map[string][]byte{
		"empty":            {},
		"truncated header": make([]byte, 8),
		"not ipv4":         append([]byte{0x60}, make([]byte, 40)...),
		"not udp": func() []byte {
			f := buildIPv4UDP(t, 67, 68, payload)
			f[9] = 6 // TCP
			return f
		}(),
		"wrong ports":        buildIPv4UDP(t, 12345, 80, payload),
		"one dhcp port only": buildIPv4UDP(t, 67, 80, payload),
		"bad header length": func() []byte {
			f := buildIPv4UDP(t, 67, 68, payload)
			f[0] = 0x41 // header length 1 word, below the minimum
			return f
		}(),
		"header longer than frame": func() []byte {
			f := buildIPv4UDP(t, 67, 68, payload)
			f[0] = 0x4f // 60-byte header, more than this frame holds
			return f
		}(),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := udpPayload(frame); ok {
				t.Fatalf("%s was accepted as DHCP", name)
			}
		})
	}
}

func TestIsDHCPPort(t *testing.T) {
	if !isDHCPPort(67) || !isDHCPPort(68) {
		t.Fatal("the DHCP ports must be recognised")
	}
	if isDHCPPort(69) || isDHCPPort(0) {
		t.Fatal("a non-DHCP port was recognised")
	}
}
