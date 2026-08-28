package rtps

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// dataSubmessage builds a little-endian DATA submessage carrying payload,
// addressed from writerID to readerID. Entity IDs go on the wire as octets.
func dataSubmessage(readerID, writerID uint32, sn uint64, payload []byte) []byte {
	body := make([]byte, 20)
	binary.LittleEndian.PutUint16(body[0:2], 0)  // extraFlags
	binary.LittleEndian.PutUint16(body[2:4], 16) // octetsToInlineQos
	binary.BigEndian.PutUint32(body[4:8], readerID)
	binary.BigEndian.PutUint32(body[8:12], writerID)
	binary.LittleEndian.PutUint32(body[12:16], uint32(sn>>32))
	binary.LittleEndian.PutUint32(body[16:20], uint32(sn))
	body = append(body, payload...)

	out := make([]byte, 4)
	out[0] = subDATA
	out[1] = 0x01 | 0x04 // little-endian, data present
	binary.LittleEndian.PutUint16(out[2:4], uint16(len(body)))
	return append(out, body...)
}

func dataFragSubmessage(readerID, writerID uint32, sn uint64, start uint32, count, fragmentSize uint16, sampleSize uint32, payload []byte) []byte {
	body := make([]byte, 32)
	binary.LittleEndian.PutUint16(body[2:4], 28)
	binary.BigEndian.PutUint32(body[4:8], readerID)
	binary.BigEndian.PutUint32(body[8:12], writerID)
	binary.LittleEndian.PutUint32(body[12:16], uint32(sn>>32))
	binary.LittleEndian.PutUint32(body[16:20], uint32(sn))
	binary.LittleEndian.PutUint32(body[20:24], start)
	binary.LittleEndian.PutUint16(body[24:26], count)
	binary.LittleEndian.PutUint16(body[26:28], fragmentSize)
	binary.LittleEndian.PutUint32(body[28:32], sampleSize)
	body = append(body, payload...)
	return buildSubmessage(subDATAFRAG, 0, body)
}

func rtpsDatagram(prefix GUIDPrefix, subs ...[]byte) []byte {
	out := []byte{'R', 'T', 'P', 'S', 2, 2, 0x01, 0x0f}
	out = append(out, prefix[:]...)
	for _, s := range subs {
		out = append(out, s...)
	}
	return out
}

func TestParseMessage_RejectsNonRTPS(t *testing.T) {
	if _, err := ParseMessage([]byte("NOPEnopenopenopenope")); err == nil {
		t.Fatal("expected an error for a non-RTPS datagram")
	}
}

func TestParseMessage_TooShort(t *testing.T) {
	if _, err := ParseMessage([]byte{'R', 'T', 'P', 'S'}); err != ErrShort {
		t.Fatalf("err = %v; want ErrShort", err)
	}
}

// EntityId_t is four octets, not a byte-order-swapped integer. Reading it with
// the submessage's endianness turns the SPDP writer 0x000100c2 into
// 0xc2000100, which matches no builtin endpoint — so every discovery
// announcement is silently misfiled as user data and discovery finds nothing.
// Observed against a real Go2: 129 RTPS messages parsed, 0 recognised as SPDP.
func TestParseData_EntityIDsAreNotEndianSwapped(t *testing.T) {
	var prefix GUIDPrefix
	payload := []byte{0x00, 0x03, 0x00, 0x00} // PL_CDR_LE, empty
	dg := rtpsDatagram(prefix, dataSubmessage(entitySPDPReader, entitySPDPWriter, 1, payload))

	msg, err := ParseMessage(dg)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Submessages) != 1 {
		t.Fatalf("got %d submessages, want 1", len(msg.Submessages))
	}
	d, err := ParseData(msg.Submessages[0])
	if err != nil {
		t.Fatal(err)
	}
	if d.WriterID != entitySPDPWriter {
		t.Errorf("WriterID = %#08x, want %#08x (entity IDs are octets, not swapped)",
			d.WriterID, entitySPDPWriter)
	}
	if d.ReaderID != entitySPDPReader {
		t.Errorf("ReaderID = %#08x, want %#08x", d.ReaderID, entitySPDPReader)
	}
}

// buildData must put entity IDs on the wire the same way ParseData reads them,
// or we would announce ourselves with a writer nobody recognises.
func TestBuildData_RoundTripsThroughParseData(t *testing.T) {
	payload := []byte{0x00, 0x03, 0x00, 0x00, 0xde, 0xad}
	sub := buildData(entitySEDPSubReader, entitySEDPSubWriter, 42, payload)

	var prefix GUIDPrefix
	msg, err := ParseMessage(rtpsDatagram(prefix, sub))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseData(msg.Submessages[0])
	if err != nil {
		t.Fatal(err)
	}
	if d.WriterID != entitySEDPSubWriter {
		t.Errorf("WriterID = %#08x, want %#08x", d.WriterID, entitySEDPSubWriter)
	}
	if d.ReaderID != entitySEDPSubReader {
		t.Errorf("ReaderID = %#08x, want %#08x", d.ReaderID, entitySEDPSubReader)
	}
	if d.WriterSN != 42 {
		t.Errorf("WriterSN = %d, want 42", d.WriterSN)
	}
	if string(d.Payload) != string(payload) {
		t.Errorf("Payload = % x, want % x", d.Payload, payload)
	}
}

func TestParseData_SequenceNumberIsHighLow(t *testing.T) {
	var prefix GUIDPrefix
	// high=1, low=2 -> 1<<32 | 2
	dg := rtpsDatagram(prefix, dataSubmessage(0, entitySPDPWriter, 1<<32|2, []byte{0, 3, 0, 0}))
	msg, _ := ParseMessage(dg)
	d, err := ParseData(msg.Submessages[0])
	if err != nil {
		t.Fatal(err)
	}
	if d.WriterSN != SequenceNumber(1<<32|2) {
		t.Errorf("WriterSN = %d, want %d", d.WriterSN, int64(1<<32|2))
	}
}

func TestParseDataFrag(t *testing.T) {
	sub := dataFragSubmessage(0, 0x00032102, 1<<32|7, 3, 2, 1024, 5000, []byte("fragment bytes"))
	msg, err := ParseMessage(rtpsDatagram(GUIDPrefix{}, sub))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseDataFrag(msg.Submessages[0])
	if err != nil {
		t.Fatal(err)
	}
	if f.WriterID != 0x00032102 || f.WriterSN != SequenceNumber(1<<32|7) ||
		f.FragmentStartingNum != 3 || f.FragmentsInSubmessage != 2 ||
		f.FragmentSize != 1024 || f.SampleSize != 5000 || string(f.Payload) != "fragment bytes" {
		t.Fatalf("parsed DATA_FRAG = %+v payload=%q", f, f.Payload)
	}
}

func TestParticipant_ReassemblesDataFragOutOfOrder(t *testing.T) {
	writer := GUID{EntityID: 0x00032102}
	p := &Participant{
		subWriters: map[GUID]struct{}{writer: {}},
		fragments:  map[fragmentKey]*fragmentSet{},
		samples:    make(chan Sample, 1),
		unmatched:  map[GUID]int{},
	}
	p.handleUserDataFrag(writer.Prefix, &DataFragSubmessage{
		WriterID: writer.EntityID, WriterSN: 9, FragmentStartingNum: 2,
		FragmentsInSubmessage: 1, FragmentSize: 4, SampleSize: 10, Payload: []byte("efgh"),
	})
	p.handleUserDataFrag(writer.Prefix, &DataFragSubmessage{
		WriterID: writer.EntityID, WriterSN: 9, FragmentStartingNum: 1,
		FragmentsInSubmessage: 1, FragmentSize: 4, SampleSize: 10, Payload: []byte("abcd"),
	})
	p.handleUserDataFrag(writer.Prefix, &DataFragSubmessage{
		WriterID: writer.EntityID, WriterSN: 9, FragmentStartingNum: 3,
		FragmentsInSubmessage: 1, FragmentSize: 4, SampleSize: 10, Payload: []byte("ij"),
	})
	select {
	case sample := <-p.samples:
		if string(sample.Payload) != "abcdefghij" {
			t.Fatalf("payload = %q", sample.Payload)
		}
	default:
		t.Fatal("completed fragmented sample was not delivered")
	}
}

func TestParticipant_ExpiresIncompleteDataFragWithoutMoreTraffic(t *testing.T) {
	key := fragmentKey{writer: GUID{EntityID: 0x00032102}, sn: 9}
	p := &Participant{
		fragments: map[fragmentKey]*fragmentSet{
			key: {buf: make([]byte, 32), updated: time.Now().Add(-fragmentSetTTL - time.Second)},
		},
		fragmentBytes: 32,
	}
	p.expireFragmentSets(time.Now())
	if len(p.fragments) != 0 || p.fragmentBytes != 0 {
		t.Fatalf("expired fragments retained: sets=%d bytes=%d", len(p.fragments), p.fragmentBytes)
	}
}

func TestParticipant_RejectsFragmentedBuiltinSubscriptionWriter(t *testing.T) {
	remote := GUIDPrefix{9}
	p := &Participant{prefix: GUIDPrefix{1}}
	sub := dataFragSubmessage(0, entitySEDPSubWriter, 1, 1, 1, 4, 4, []byte("data"))
	p.handle(rtpsDatagram(remote, sub), nil)
	stats := p.Stats()
	if stats.DataParseErrors != 1 || stats.UserDataMessages != 0 {
		t.Fatalf("stats = %+v; want one parse error and no user data", stats)
	}
}

func TestParticipant_VerifiesNamespaceTargetBeforeEntry(t *testing.T) {
	called := false
	err := verifyNamespaceTarget(42, func() bool {
		called = true
		return false
	})
	if !called || err == nil || !strings.Contains(err.Error(), "process 42 changed") {
		t.Fatalf("called=%v error=%v; want verifier rejection before namespace entry", called, err)
	}
}

func TestParseMessage_ZeroLengthSubmessageRunsToEnd(t *testing.T) {
	var prefix GUIDPrefix
	// A submessage whose octetsToNextHeader is 0 extends to the datagram end.
	sub := []byte{subDATA, 0x01, 0x00, 0x00}
	sub = append(sub, make([]byte, 24)...)
	msg, err := ParseMessage(rtpsDatagram(prefix, sub))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Submessages) != 1 {
		t.Fatalf("got %d submessages, want 1", len(msg.Submessages))
	}
	if got := len(msg.Submessages[0].Body); got != 24 {
		t.Errorf("body = %d bytes, want 24", got)
	}
}

func TestLocator_UDPAddr(t *testing.T) {
	l := udpv4Locator([]byte{192, 168, 123, 18}, 7412)
	addr, ok := l.UDPAddr()
	if !ok {
		t.Fatal("expected a usable locator")
	}
	if addr.String() != "192.168.123.18:7412" {
		t.Errorf("addr = %s, want 192.168.123.18:7412", addr)
	}
}

func TestLocator_RejectsUnusable(t *testing.T) {
	if _, ok := (Locator{Kind: locatorKindUDPv4, Port: 0}).UDPAddr(); ok {
		t.Error("a zero port must not be usable")
	}
	if _, ok := (Locator{Kind: 16, Port: 7400}).UDPAddr(); ok {
		t.Error("a non-UDPv4 locator must not be usable")
	}
}

func TestSPDPMulticastPort(t *testing.T) {
	for _, tc := range []struct {
		domain int
		want   int
	}{{0, 7400}, {1, 7650}, {42, 17900}} {
		if got := spdpMulticastPort(tc.domain); got != tc.want {
			t.Errorf("spdpMulticastPort(%d) = %d, want %d", tc.domain, got, tc.want)
		}
	}
}

func TestParameterList_RoundTrip(t *testing.T) {
	b := newPLBuilder()
	b.addString(pidTopicName, "rt/lf/lowstate")
	b.addString(pidTypeName, "unitree_go::msg::dds_::LowState_")
	b.addGUID(pidEndpointGUID, GUID{EntityID: 0x00000104})
	b.addLocator(pidUnicastLocator, udpv4Locator([]byte{192, 168, 123, 18}, 41612))
	b.addReliability(reliabilityBestEffort)
	payload := b.finish()

	params, order, err := parseParameterList(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint16]parameter{}
	for _, p := range params {
		got[p.id] = p
	}

	if s, ok := paramString(got[pidTopicName].value, order); !ok || s != "rt/lf/lowstate" {
		t.Errorf("topic = %q, %v; want rt/lf/lowstate", s, ok)
	}
	if s, ok := paramString(got[pidTypeName].value, order); !ok || s != "unitree_go::msg::dds_::LowState_" {
		t.Errorf("type = %q, %v", s, ok)
	}
	if g, ok := paramGUID(got[pidEndpointGUID].value, order); !ok || g.EntityID != 0x00000104 {
		t.Errorf("guid = %v, %v; want entity 0x104", g, ok)
	}
	l, ok := paramLocator(got[pidUnicastLocator].value, order)
	if !ok {
		t.Fatal("locator did not round-trip")
	}
	if addr, ok := l.UDPAddr(); !ok || addr.String() != "192.168.123.18:41612" {
		t.Errorf("locator = %v, %v", addr, ok)
	}
}

func TestParameterList_StopsAtSentinel(t *testing.T) {
	b := newPLBuilder()
	b.addString(pidTopicName, "a")
	payload := b.finish()
	payload = append(payload, 0xff, 0xff, 0xff, 0xff) // trailing junk past the sentinel

	params, _, err := parseParameterList(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Errorf("got %d parameters, want 1 — parsing must stop at the sentinel", len(params))
	}
}
