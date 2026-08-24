//go:build darwin || linux || windows

package commands

import (
	"encoding/binary"
	"testing"
	"time"

	"go.bug.st/serial"
)

type modemLineChange struct {
	line  string
	value bool
}

type resetTestPort struct {
	changes           []modemLineChange
	inputBufferResets int
	readCalled        bool
}

type scriptedReadPort struct {
	resetTestPort
	input []byte
}

func (p *scriptedReadPort) Read(data []byte) (int, error) {
	if len(p.input) == 0 {
		return 0, nil
	}
	data[0] = p.input[0]
	p.input = p.input[1:]
	return 1, nil
}

func (p *resetTestPort) SetDTR(value bool) error {
	p.changes = append(p.changes, modemLineChange{line: "DTR", value: value})
	return nil
}

func (p *resetTestPort) SetRTS(value bool) error {
	p.changes = append(p.changes, modemLineChange{line: "RTS", value: value})
	return nil
}

func (p *resetTestPort) ResetInputBuffer() error {
	p.inputBufferResets++
	return nil
}

func (p *resetTestPort) Read([]byte) (int, error) {
	p.readCalled = true
	return 0, nil
}

func (p *resetTestPort) SetMode(*serial.Mode) error                           { return nil }
func (p *resetTestPort) Write(data []byte) (int, error)                       { return len(data), nil }
func (p *resetTestPort) Drain() error                                         { return nil }
func (p *resetTestPort) ResetOutputBuffer() error                             { return nil }
func (p *resetTestPort) GetModemStatusBits() (*serial.ModemStatusBits, error) { return nil, nil }
func (p *resetTestPort) SetReadTimeout(time.Duration) error                   { return nil }
func (p *resetTestPort) Close() error                                         { return nil }
func (p *resetTestPort) Break(time.Duration) error                            { return nil }

func TestResetESP32ViaNativeUSBUsesUSBJTAGSequence(t *testing.T) {
	port := &resetTestPort{}
	if err := espResetViaUSBJTAG(port, true); err != nil {
		t.Fatal(err)
	}
	want := []modemLineChange{
		{line: "RTS", value: false},
		{line: "DTR", value: false},
		{line: "DTR", value: true},
		{line: "RTS", value: false},
		{line: "RTS", value: true},
		{line: "DTR", value: false},
		{line: "RTS", value: true},
		{line: "DTR", value: false},
		{line: "RTS", value: false},
	}
	assertModemLineChanges(t, port.changes, want)
}

func TestESPFlasherDrainPurgesInsteadOfReadingUntilQuiet(t *testing.T) {
	port := &resetTestPort{}
	flasher := &espFlasher{port: port}
	if err := flasher.drain(); err != nil {
		t.Fatal(err)
	}
	if port.inputBufferResets != 1 {
		t.Fatalf("input buffer reset count = %d, want 1", port.inputBufferResets)
	}
	if port.readCalled {
		t.Fatal("drain read from the serial stream; continuous application output could hang it")
	}
}

func TestSlipDecodePreservesFrameAfterConsecutiveDelimiters(t *testing.T) {
	port := &scriptedReadPort{input: []byte{
		0x55, slipEnd, slipEnd,
		0x01, espCmdChangeBaud, 0x04, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		slipEnd,
	}}
	flasher := &espFlasher{port: port, readTimeout: time.Second}
	got, err := flasher.slipDecode()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x01, espCmdChangeBaud, 0x04, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	if len(got) != len(want) {
		t.Fatalf("decoded frame = %x, want %x", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded frame = %x, want %x", got, want)
		}
	}
}

func TestChangeBaudCommandDataUsesROMBootloaderLayout(t *testing.T) {
	data := changeBaudCommandData(flashBaudRate)
	if got := binary.LittleEndian.Uint32(data[0:4]); got != flashBaudRate {
		t.Fatalf("new baud = %d, want %d", got, flashBaudRate)
	}
	if got := binary.LittleEndian.Uint32(data[4:8]); got != 0 {
		t.Fatalf("ROM bootloader old-baud argument = %d, want 0", got)
	}
}

func assertModemLineChanges(t *testing.T, got, want []modemLineChange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("line changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line change %d = %+v, want %+v (all got: %+v)", i, got[i], want[i], got)
		}
	}
}
