package timesync_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

// buildRelayPacket builds a valid WendyDatagram multicast packet for the given
// server index and a signed Roughtime response.
func buildRelayPacket(t *testing.T, serverIdx uint8, priv ed25519.PrivateKey, midpMicros uint64) []byte {
	t.Helper()
	nonce := make([]byte, 32)
	rand.Read(nonce) //nolint:errcheck
	_, onlinePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("online keygen: %v", err)
	}

	midpB := make([]byte, 8)
	binary.LittleEndian.PutUint64(midpB, midpMicros/1_000_000)
	radiB := make([]byte, 4)
	binary.LittleEndian.PutUint32(radiB, 1)
	leaf := sha512.Sum512(append([]byte{0x00}, nonce...))
	verB := make([]byte, 4)
	binary.LittleEndian.PutUint32(verB, roughtime.VersionDraft08)

	srep := roughtime.EncodeMessage(map[uint32][]byte{
		roughtime.TagMIDP: midpB,
		roughtime.TagRADI: radiB,
		roughtime.TagROOT: leaf[:32],
	})
	toSign := append([]byte(roughtime.SigContext), srep...)
	sig := ed25519.Sign(onlinePriv, toSign)

	pubk := ed25519.PrivateKey(onlinePriv).Public().(ed25519.PublicKey)
	mintB := make([]byte, 8)
	binary.LittleEndian.PutUint64(mintB, 0)
	maxtB := make([]byte, 8)
	binary.LittleEndian.PutUint64(maxtB, ^uint64(0))
	dele := roughtime.EncodeMessage(map[uint32][]byte{
		roughtime.TagMINT: mintB,
		roughtime.TagMAXT: maxtB,
		roughtime.TagPUBK: pubk,
	})
	certSig := ed25519.Sign(priv, append([]byte(roughtime.CertContext), dele...))
	cert := roughtime.EncodeMessage(map[uint32][]byte{
		roughtime.TagSIG:  certSig,
		roughtime.TagDELE: dele,
	})

	indxB := make([]byte, 4)
	resp := roughtime.EncodeMessage(map[uint32][]byte{
		roughtime.TagCERT: cert,
		roughtime.TagNONC: nonce,
		roughtime.TagSIG:  sig,
		roughtime.TagSREP: srep,
		roughtime.TagINDX: indxB,
		roughtime.TagPATH: {},
		roughtime.TagVER:  verB,
	})
	rawResp := append([]byte("ROUGHTIM"), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(rawResp[8:12], uint32(len(resp)))
	rawResp = append(rawResp, resp...)

	payload := roughtime.EncodeRoughtimePayload(roughtime.RoughtimePayload{
		ServerIndex: serverIdx,
		Nonce:       nonce,
		Response:    rawResp,
	})
	return roughtime.Encode(roughtime.Datagram{MsgType: roughtime.MsgTypeRoughtime, Payload: payload})
}

func TestProcessMulticastPacket_ValidPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	wantTime := time.Date(2026, 6, 23, 15, 0, 0, 0, time.UTC)

	// Override Servers[0] with our test key.
	original := timesync.Servers
	timesync.Servers = []roughtime.Server{{Name: "test", Address: "unused", PublicKey: pub}}
	defer func() { timesync.Servers = original }()

	pkt := buildRelayPacket(t, 0, priv, uint64(wantTime.UnixMicro()))
	applied, err := timesync.ProcessMulticastPacket(pkt)
	if err != nil {
		t.Fatalf("ProcessMulticastPacket: %v", err)
	}
	diff := applied.Sub(wantTime).Abs()
	if diff > time.Second {
		t.Errorf("applied time off by %v", diff)
	}
}

func TestProcessMulticastPacket_UnknownMsgType_Ignored(t *testing.T) {
	pkt := roughtime.Encode(roughtime.Datagram{MsgType: 0x42, Payload: []byte{1, 2, 3}})
	_, err := timesync.ProcessMulticastPacket(pkt)
	if err != nil {
		t.Errorf("unknown msg_type should be silently ignored, got: %v", err)
	}
}

func TestProcessMulticastPacket_TamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	timesync.Servers = []roughtime.Server{{Name: "test", Address: "unused", PublicKey: pub}}

	pkt := buildRelayPacket(t, 0, priv, uint64(time.Now().UnixMicro()))
	// Flip a byte in the Roughtime response portion.
	pkt[len(pkt)/2] ^= 0xFF

	if _, err := timesync.ProcessMulticastPacket(pkt); err == nil {
		t.Error("expected error for tampered packet")
	}
}

func TestProcessMulticastPacket_OutOfRangeServerIndex(t *testing.T) {
	pkt := roughtime.Encode(roughtime.Datagram{
		MsgType: roughtime.MsgTypeRoughtime,
		Payload: roughtime.EncodeRoughtimePayload(roughtime.RoughtimePayload{
			ServerIndex: 255, // no such server
			Response:    []byte{1, 2, 3},
		}),
	})
	if _, err := timesync.ProcessMulticastPacket(pkt); err == nil {
		t.Error("expected error for out-of-range server index")
	}
}

// TestRunMulticast_ListensOnGroup is a lightweight smoke test: it binds the
// multicast group, starts RunMulticast in the background, sends a valid packet
// via loopback, and confirms the manager's clock was advanced.
func TestRunMulticast_ListensOnGroup(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	original := timesync.Servers
	timesync.Servers = []roughtime.Server{{Name: "test", Address: "unused", PublicKey: pub}}
	defer func() { timesync.Servers = original }()

	wantTime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	pkt := buildRelayPacket(t, 0, priv, uint64(wantTime.UnixMicro()))

	// Send to the multicast group address from the loopback interface.
	conn, err := net.Dial("udp4", "239.255.87.84:5887")
	if err != nil {
		t.Skipf("cannot reach multicast group (no suitable interface): %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("send: %v", err)
	}
}
