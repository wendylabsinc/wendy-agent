# ROS 2 Battery Source — Phase 1: Capture and Decode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a captured DDS payload into a `hoststats.Battery`, with no networking involved.

**Architecture:** A classic-CDR (XCDR1) decoder in `go/internal/rtps/cdr`, two message decoders in `go/internal/agent/hoststats/rosbattery` (`sensor_msgs/msg/BatteryState` preferred, `unitree_go/msg/LowState` fallback), and a staleness-aware cache. Every unit is a pure function of bytes plus an injected clock, so the whole phase is testable without a robot once the fixtures are captured.

**Tech Stack:** Go 1.26.5, module `github.com/wendylabsinc/wendy` (packages live under `go/`), standard library only — no new dependencies.

Implements the decode half of
[`specs/2026-08-08-ros2-battery-source-design.md`](../../2026-08-08-ros2-battery-source-design.md).
Phase 2 (RTPS discovery and reader) and Phase 3 (monitor, config, agent wiring)
get their own plans and depend on this one.

## Global Constraints

- Go 1.26.5; module `github.com/wendylabsinc/wendy`; import paths are `github.com/wendylabsinc/wendy/go/internal/...`
- **No new module dependencies.** Standard library only.
- The agent builds `CGO_ENABLED=0` — nothing here may require cgo.
- `go/internal/agent/hoststats/battery.go` keeps its existing exported surface. `SampleBattery()` is not modified in this phase.
- Existing tests in `go/internal/agent/hoststats/battery_test.go` must continue to pass untouched.
- Run `gofmt -w` on every file before committing; CI enforces it.
- Test command from the repo root: `go test ./go/internal/rtps/... ./go/internal/agent/hoststats/...`

## Ground Truth Warning

The message layouts in Tasks 4 and 5 are **provisional** — reconstructed from
memory, not verified. Task 1 captures the authoritative definitions from
`woof.local` and **the layouts in later tasks must be corrected to match what
Task 1 produces before their decoders are written.** A wrong offset in
`LowState` decodes to a plausible-looking wrong number, not an error, so this
is not a formality.

---

### Task 1: Capture ground-truth fixtures from `woof.local`

The robot is the only source of truth for both the wire bytes and the message
layouts. Everything downstream depends on this, and the robot has to be powered
on, so do it first.

**Files:**
- Create: `go/internal/agent/hoststats/rosbattery/testdata/README.md`
- Create: `go/internal/agent/hoststats/rosbattery/testdata/battery_state.msg`
- Create: `go/internal/agent/hoststats/rosbattery/testdata/lowstate.msg`
- Create: `go/internal/agent/hoststats/rosbattery/testdata/bms_state.msg`
- Create: `go/internal/agent/hoststats/rosbattery/testdata/imu_state.msg`
- Create: `go/internal/agent/hoststats/rosbattery/testdata/motor_state.msg`

**Interfaces:**
- Consumes: nothing
- Produces: the `.msg` definitions that Tasks 4 and 5 encode as decoders, and the
  recorded topic/type names Phase 2 will match on

- [ ] **Step 1: Confirm the robot is reachable**

```bash
nc -z -G 3 192.168.0.107 50052 && echo "agent up"
ssh unitree@192.168.0.107 'echo ok'
```

Expected: `agent up` and `ok`. If SSH refuses, the rest of this task cannot
proceed — stop and report rather than guessing at layouts.

- [ ] **Step 2: Record the exact topic and type names**

```bash
ssh unitree@192.168.0.107 \
  'source /opt/ros/*/setup.bash 2>/dev/null; ros2 topic list -t' \
  | tee /tmp/go2-topics.txt
grep -iE 'batt|lowstate' /tmp/go2-topics.txt
```

Expected: at least one line ending `[sensor_msgs/msg/BatteryState]` and one
ending `[unitree_go/msg/LowState]`. Record both topic names verbatim — Phase 2
matches on the type names and Phase 3's config pins the topic names.

- [ ] **Step 3: Capture the authoritative message definitions**

```bash
D=go/internal/agent/hoststats/rosbattery/testdata
mkdir -p "$D"
for t in sensor_msgs/msg/BatteryState unitree_go/msg/LowState \
         unitree_go/msg/BmsState unitree_go/msg/IMUState \
         unitree_go/msg/MotorState; do
  out="$D/$(echo "$t" | awk -F/ '{print tolower($3)}').msg"
  ssh unitree@192.168.0.107 \
    "source /opt/ros/*/setup.bash 2>/dev/null; ros2 interface show $t" > "$out"
  echo "=== $t"; cat "$out"
done
```

Expected: five non-empty files. **Read each one.** Field order and types here
override the provisional layouts in Tasks 4 and 5.

- [ ] **Step 4: Record a sample of each topic as human-readable YAML**

Substitute the real topic names from Step 2.

```bash
D=go/internal/agent/hoststats/rosbattery/testdata
ssh unitree@192.168.0.107 \
  'source /opt/ros/*/setup.bash 2>/dev/null; timeout 20 ros2 topic echo --once /battery_state' \
  > "$D/battery_state_sample.yaml"
cat "$D/battery_state_sample.yaml"
```

Expected: a YAML document with `percentage`, `current`, `charge`,
`power_supply_status`. Note the observed `percentage` value — it decides whether
the (1, 100] heuristic in Task 6 is actually exercised on this robot.

- [ ] **Step 5: Write the fixture README**

```markdown
# rosbattery test fixtures

Captured from `woof.local` (192.168.0.107), a Unitree Go2 EDU: Jetson Orin,
JetPack 6.2.1, Ubuntu 22.04. DDS runs on `enP8p1s0` (192.168.123.18).

- `*.msg` — `ros2 interface show` output. These are the authoritative field
  orders the decoders in `batterystate.go` and `lowstate.go` encode. If a
  firmware or ROS distro change alters them, the decoders must change too, and
  `TestDecodeLowState_RejectsWrongLength` is the test that should catch it.
- `battery_state_sample.yaml` — one `ros2 topic echo --once`, used to sanity-check
  decoded values against what ROS itself reports.

Re-capture with the commands in
`specs/superpowers/plans/2026-08-08-ros2-battery-decode.md`, Task 1.
```

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/hoststats/rosbattery/testdata
git commit -m "test(rosbattery): capture ground-truth ROS 2 message definitions from a Go2"
```

---

### Task 2: CDR decoder — encapsulation header, alignment, primitives

**Files:**
- Create: `go/internal/rtps/cdr/decoder.go`
- Test: `go/internal/rtps/cdr/decoder_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `func NewDecoder(payload []byte) (*Decoder, error)`
  - `func (*Decoder) Uint8() (uint8, error)`, `Int8() (int8, error)`, `Bool() (bool, error)`
  - `func (*Decoder) Uint16() (uint16, error)`, `Int16() (int16, error)`
  - `func (*Decoder) Uint32() (uint32, error)`, `Int32() (int32, error)`
  - `func (*Decoder) Float32() (float32, error)`
  - `func (*Decoder) Remaining() int`
  - `var ErrShort error`

Background the implementer needs: a ROS 2 user-data payload begins with a
4-byte encapsulation header — two bytes of representation identifier
(`0x0000` CDR_BE, `0x0001` CDR_LE, `0x0002` PL_CDR_BE, `0x0003` PL_CDR_LE),
then two option bytes. The identifier itself is always big-endian. Crucially,
**alignment is measured from the end of that header**, not from the start of
the buffer, so the decoder stores the body separately. Each primitive aligns to
its own width: 1 for `uint8`/`bool`, 2 for `uint16`, 4 for `uint32`/`float32`.

- [ ] **Step 1: Write the failing test**

```go
package cdr

import (
	"errors"
	"testing"
)

// le builds a little-endian CDR payload: encapsulation header + body.
func le(body ...byte) []byte {
	return append([]byte{0x00, 0x01, 0x00, 0x00}, body...)
}

// be builds a big-endian CDR payload.
func be(body ...byte) []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x00}, body...)
}

func TestNewDecoder_RejectsTruncatedHeader(t *testing.T) {
	if _, err := NewDecoder([]byte{0x00, 0x01}); !errors.Is(err, ErrShort) {
		t.Fatalf("err = %v; want ErrShort", err)
	}
}

func TestNewDecoder_RejectsUnknownEncapsulation(t *testing.T) {
	if _, err := NewDecoder([]byte{0x00, 0x7f, 0x00, 0x00}); err == nil {
		t.Fatal("expected an error for an unknown representation identifier")
	}
}

func TestDecoder_LittleEndianPrimitives(t *testing.T) {
	d, err := NewDecoder(le(0x2a, 0x00, 0x34, 0x12, 0x78, 0x56, 0x34, 0x12))
	if err != nil {
		t.Fatal(err)
	}
	// uint8 at 0, then uint16 aligned to 2 (skipping one pad byte), then
	// uint32 aligned to 4.
	if v, err := d.Uint8(); err != nil || v != 0x2a {
		t.Fatalf("Uint8 = %v, %v; want 42, nil", v, err)
	}
	if v, err := d.Uint16(); err != nil || v != 0x1234 {
		t.Fatalf("Uint16 = %#x, %v; want 0x1234, nil", v, err)
	}
	if v, err := d.Uint32(); err != nil || v != 0x12345678 {
		t.Fatalf("Uint32 = %#x, %v; want 0x12345678, nil", v, err)
	}
	if d.Remaining() != 0 {
		t.Errorf("Remaining = %d; want 0", d.Remaining())
	}
}

func TestDecoder_BigEndianPrimitives(t *testing.T) {
	d, err := NewDecoder(be(0x12, 0x34, 0x12, 0x34, 0x56, 0x78))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.Uint16(); err != nil || v != 0x1234 {
		t.Fatalf("Uint16 = %#x, %v; want 0x1234, nil", v, err)
	}
	if v, err := d.Uint32(); err != nil || v != 0x12345678 {
		t.Fatalf("Uint32 = %#x, %v; want 0x12345678, nil", v, err)
	}
}

func TestDecoder_Float32(t *testing.T) {
	// 1.5f == 0x3fc00000
	d, err := NewDecoder(le(0x00, 0x00, 0xc0, 0x3f))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.Float32(); err != nil || v != 1.5 {
		t.Fatalf("Float32 = %v, %v; want 1.5, nil", v, err)
	}
}

func TestDecoder_SignedPrimitives(t *testing.T) {
	d, err := NewDecoder(le(0xff, 0x00, 0xfe, 0xff, 0xff, 0xff, 0xff, 0xff))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.Int8(); err != nil || v != -1 {
		t.Fatalf("Int8 = %v, %v; want -1, nil", v, err)
	}
	if v, err := d.Int16(); err != nil || v != -2 {
		t.Fatalf("Int16 = %v, %v; want -2, nil", v, err)
	}
	if v, err := d.Int32(); err != nil || v != -1 {
		t.Fatalf("Int32 = %v, %v; want -1, nil", v, err)
	}
}

func TestDecoder_Bool(t *testing.T) {
	d, err := NewDecoder(le(0x01, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.Bool(); err != nil || !v {
		t.Fatalf("Bool = %v, %v; want true, nil", v, err)
	}
	if v, err := d.Bool(); err != nil || v {
		t.Fatalf("Bool = %v, %v; want false, nil", v, err)
	}
}

func TestDecoder_ShortReadIsErrShort(t *testing.T) {
	d, err := NewDecoder(le(0x01))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Uint32(); !errors.Is(err, ErrShort) {
		t.Fatalf("err = %v; want ErrShort", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/rtps/cdr/ -run TestDecoder -v`
Expected: FAIL — the package does not compile, `undefined: NewDecoder`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package cdr decodes classic CDR (XCDR1) payloads, the wire format ROS 2
// uses for user data on DDS. It is decode-only and covers exactly the subset
// the battery messages need.
package cdr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ErrShort reports a payload that ended before the requested field.
var ErrShort = errors.New("cdr: payload too short")

// Decoder reads a CDR body. Alignment is measured from the start of the body,
// i.e. excluding the 4-byte encapsulation header, which is what the RTPS spec
// requires and what both CycloneDDS and Fast DDS emit.
type Decoder struct {
	buf   []byte
	pos   int
	order binary.ByteOrder
}

// NewDecoder splits the encapsulation header off payload and selects the byte
// order it names. The identifier itself is always big-endian.
func NewDecoder(payload []byte) (*Decoder, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("reading encapsulation header: %w", ErrShort)
	}
	var order binary.ByteOrder
	switch id := binary.BigEndian.Uint16(payload[0:2]); id {
	case 0x0000, 0x0002: // CDR_BE, PL_CDR_BE
		order = binary.BigEndian
	case 0x0001, 0x0003: // CDR_LE, PL_CDR_LE
		order = binary.LittleEndian
	default:
		return nil, fmt.Errorf("cdr: unsupported representation identifier %#04x", id)
	}
	return &Decoder{buf: payload[4:], order: order}, nil
}

// Remaining reports the unread byte count, used to assert that a decoder
// consumed a payload exactly.
func (d *Decoder) Remaining() int { return len(d.buf) - d.pos }

// align advances pos to the next multiple of n.
func (d *Decoder) align(n int) {
	if r := d.pos % n; r != 0 {
		d.pos += n - r
	}
}

// take aligns to n, then consumes n bytes.
func (d *Decoder) take(n int) ([]byte, error) {
	d.align(n)
	if d.pos+n > len(d.buf) {
		return nil, ErrShort
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *Decoder) Uint8() (uint8, error) {
	b, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *Decoder) Int8() (int8, error) {
	v, err := d.Uint8()
	return int8(v), err
}

func (d *Decoder) Bool() (bool, error) {
	v, err := d.Uint8()
	return v != 0, err
}

func (d *Decoder) Uint16() (uint16, error) {
	b, err := d.take(2)
	if err != nil {
		return 0, err
	}
	return d.order.Uint16(b), nil
}

func (d *Decoder) Int16() (int16, error) {
	v, err := d.Uint16()
	return int16(v), err
}

func (d *Decoder) Uint32() (uint32, error) {
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(b), nil
}

func (d *Decoder) Int32() (int32, error) {
	v, err := d.Uint32()
	return int32(v), err
}

func (d *Decoder) Float32() (float32, error) {
	v, err := d.Uint32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/rtps/cdr/ -v`
Expected: PASS, all eight tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/rtps/cdr/
git add go/internal/rtps/cdr/
git commit -m "feat(cdr): add classic-CDR primitive decoding"
```

---

### Task 3: CDR decoder — strings, sequences, and skips

**Files:**
- Modify: `go/internal/rtps/cdr/decoder.go`
- Test: `go/internal/rtps/cdr/decoder_test.go`

**Interfaces:**
- Consumes: `NewDecoder`, `Uint32`, `take`, `align`, `ErrShort` from Task 2
- Produces:
  - `func (*Decoder) String() (string, error)`
  - `func (*Decoder) SkipFloat32Seq() error`
  - `func (*Decoder) SkipString() error`
  - `func (*Decoder) SkipBytes(align, n int) error`

A CDR string is a `uint32` length **including** its NUL terminator, then that
many bytes. A sequence is a `uint32` element count followed by the elements. A
fixed-size array has no count. `SkipBytes` exists so message decoders can step
over fixed arrays (`float32[3]` is `SkipBytes(4, 12)`) while still paying the
alignment of the first element.

- [ ] **Step 1: Write the failing test**

```go
func TestDecoder_String(t *testing.T) {
	// len=6 ("hello\0"), then the bytes.
	d, err := NewDecoder(le(
		0x06, 0x00, 0x00, 0x00,
		'h', 'e', 'l', 'l', 'o', 0x00,
	))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.String(); err != nil || v != "hello" {
		t.Fatalf("String = %q, %v; want \"hello\", nil", v, err)
	}
	if d.Remaining() != 0 {
		t.Errorf("Remaining = %d; want 0", d.Remaining())
	}
}

func TestDecoder_StringEmpty(t *testing.T) {
	d, err := NewDecoder(le(0x01, 0x00, 0x00, 0x00, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := d.String(); err != nil || v != "" {
		t.Fatalf("String = %q, %v; want \"\", nil", v, err)
	}
}

func TestDecoder_StringTruncated(t *testing.T) {
	d, err := NewDecoder(le(0xff, 0x00, 0x00, 0x00, 'a'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.String(); !errors.Is(err, ErrShort) {
		t.Fatalf("err = %v; want ErrShort", err)
	}
}

func TestDecoder_SkipFloat32Seq(t *testing.T) {
	// count=2, then two float32s, then a trailing uint8 we expect to reach.
	d, err := NewDecoder(le(
		0x02, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x80, 0x3f, // 1.0
		0x00, 0x00, 0x00, 0x40, // 2.0
		0x7b,
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SkipFloat32Seq(); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Uint8(); err != nil || v != 123 {
		t.Fatalf("Uint8 = %v, %v; want 123, nil", v, err)
	}
}

func TestDecoder_SkipFloat32SeqTruncated(t *testing.T) {
	d, err := NewDecoder(le(0x09, 0x00, 0x00, 0x00, 0x00, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SkipFloat32Seq(); !errors.Is(err, ErrShort) {
		t.Fatalf("err = %v; want ErrShort", err)
	}
}

func TestDecoder_SkipBytesPaysAlignment(t *testing.T) {
	// uint8, then a float32[2] that must align to 4 first.
	d, err := NewDecoder(le(
		0x01,
		0x00, 0x00, 0x00, // padding to align 4
		0xde, 0xad, 0xbe, 0xef,
		0xde, 0xad, 0xbe, 0xef,
		0x2a,
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Uint8(); err != nil {
		t.Fatal(err)
	}
	if err := d.SkipBytes(4, 8); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Uint8(); err != nil || v != 42 {
		t.Fatalf("Uint8 = %v, %v; want 42, nil", v, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/rtps/cdr/ -run 'String|Skip' -v`
Expected: FAIL — `d.String undefined`, `d.SkipFloat32Seq undefined`, `d.SkipBytes undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `decoder.go`:

```go
// String reads a CDR string: a uint32 length that includes the NUL
// terminator, then that many bytes.
func (d *Decoder) String() (string, error) {
	n, err := d.Uint32()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if int(n) > len(d.buf)-d.pos {
		return "", ErrShort
	}
	s := d.buf[d.pos : d.pos+int(n)]
	d.pos += int(n)
	if len(s) > 0 && s[len(s)-1] == 0 {
		s = s[:len(s)-1]
	}
	return string(s), nil
}

// SkipString steps over a string without allocating it.
func (d *Decoder) SkipString() error {
	_, err := d.String()
	return err
}

// SkipBytes aligns to `align`, then steps over n bytes. Use it for fixed-size
// arrays, whose alignment is that of their element type.
func (d *Decoder) SkipBytes(align, n int) error {
	d.align(align)
	if d.pos+n > len(d.buf) {
		return ErrShort
	}
	d.pos += n
	return nil
}

// SkipFloat32Seq steps over a sequence<float32>: a uint32 count, then that
// many 4-byte elements.
func (d *Decoder) SkipFloat32Seq() error {
	n, err := d.Uint32()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return d.SkipBytes(4, int(n)*4)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/rtps/cdr/ -v`
Expected: PASS, all fourteen tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/rtps/cdr/
git add go/internal/rtps/cdr/
git commit -m "feat(cdr): add string, sequence, and skip decoding"
```

---

### Task 4: Export the shared time-remaining estimator

`rosbattery` is a sibling package and cannot reach `hoststats`'s unexported
`estimateBatterySeconds`, but it must produce identical estimates — same units,
same "never extrapolate" rule. Export a wrapper rather than duplicating it.

**Files:**
- Modify: `go/internal/agent/hoststats/battery.go` (append after `estimateBatterySeconds`, which ends at line 190)
- Test: `go/internal/agent/hoststats/battery_test.go` (append)

**Interfaces:**
- Consumes: `estimateBatterySeconds`, `BatteryState` from the existing package
- Produces: `func EstimateSecondsRemaining(state BatteryState, now, full, rate float64) int64`

- [ ] **Step 1: Write the failing test**

```go
func TestEstimateSecondsRemaining_MatchesUnexported(t *testing.T) {
	cases := []struct {
		name             string
		state            BatteryState
		now, full, rate  float64
		want             int64
	}{
		{"discharging", BatteryDischarging, 39, 50, 5, 28080},
		{"charging", BatteryCharging, 20, 50, 6, 18000},
		{"full has no countdown", BatteryFull, 50, 50, 5, 0},
		{"zero rate is unknown", BatteryDischarging, 39, 50, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateSecondsRemaining(tc.state, tc.now, tc.full, tc.rate); got != tc.want {
				t.Errorf("EstimateSecondsRemaining = %d; want %d", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/hoststats/ -run TestEstimateSecondsRemaining -v`
Expected: FAIL — `undefined: EstimateSecondsRemaining`.

- [ ] **Step 3: Write minimal implementation**

```go
// EstimateSecondsRemaining is the exported form of estimateBatterySeconds, for
// sibling packages that build a Battery from a source other than sysfs. now,
// full, and rate must share a unit family; the result is seconds, or 0 for
// "unknown" under exactly the same rules the sysfs path uses.
func EstimateSecondsRemaining(state BatteryState, now, full, rate float64) int64 {
	return estimateBatterySeconds(state, now, full, rate)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/hoststats/ -v`
Expected: PASS — the new test plus every pre-existing battery test.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/agent/hoststats/
git add go/internal/agent/hoststats/
git commit -m "refactor(hoststats): export the battery time estimator for sibling sources"
```

---

### Task 5: Decode `sensor_msgs/msg/BatteryState`

**⚠ Before writing any code, open `testdata/batterystate.msg` from Task 1 and
correct the field list below to match it.** The provisional layout is:

```
std_msgs/Header header      # builtin_interfaces/Time stamp {int32 sec; uint32 nanosec}; string frame_id
float32 voltage
float32 temperature
float32 current             # negative when discharging
float32 charge              # Ah
float32 capacity            # Ah
float32 design_capacity     # Ah
float32 percentage          # 0..1
uint8 power_supply_status   # 0 UNKNOWN 1 CHARGING 2 DISCHARGING 3 NOT_CHARGING 4 FULL
uint8 power_supply_health
uint8 power_supply_technology
bool present
float32[] cell_voltage
float32[] cell_temperature
string location
string serial_number
```

**Files:**
- Create: `go/internal/agent/hoststats/rosbattery/batterystate.go`
- Test: `go/internal/agent/hoststats/rosbattery/batterystate_test.go`

**Interfaces:**
- Consumes: `cdr.NewDecoder` and its methods (Tasks 2–3); `hoststats.Battery`,
  `hoststats.BatteryState`, `hoststats.EstimateSecondsRemaining` (Task 4)
- Produces:
  - `const TypeBatteryState = "sensor_msgs::msg::dds_::BatteryState_"`
  - `func DecodeBatteryState(payload []byte) (*hoststats.Battery, error)`

Two mapping rules the tests pin down. `power_supply_status` maps 1:1 onto the
existing enum, so no new states appear. And `percentage` is specified as 0–1 but
misreported as 0–100 often enough that a value in (1, 100] is taken as already
a percent — with NaN falling back to `charge/capacity`.

- [ ] **Step 1: Write the failing test**

```go
package rosbattery

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// batteryStatePayload builds a little-endian BatteryState CDR payload.
func batteryStatePayload(current, charge, capacity, percentage float32, status uint8) []byte {
	var b []byte
	put32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	putf := func(v float32) { put32(math.Float32bits(v)) }
	align := func(n int) {
		for len(b)%n != 0 {
			b = append(b, 0)
		}
	}

	put32(0)            // header.stamp.sec
	put32(0)            // header.stamp.nanosec
	put32(1)            // header.frame_id length ("")
	b = append(b, 0)    // NUL
	align(4)
	putf(24.5)          // voltage
	putf(30.0)          // temperature
	putf(current)
	putf(charge)
	putf(capacity)
	putf(capacity)      // design_capacity
	putf(percentage)
	b = append(b, status, 0, 0, 1) // status, health, technology, present
	put32(0)            // cell_voltage: empty sequence
	put32(0)            // cell_temperature: empty sequence
	put32(1)            // location ""
	b = append(b, 0)
	align(4)
	put32(1)            // serial_number ""
	b = append(b, 0)

	return append([]byte{0x00, 0x01, 0x00, 0x00}, b...)
}

func TestDecodeBatteryState_Discharging(t *testing.T) {
	// 7.8 Ah left of 10 Ah, drawing 5 A, 78%. 7.8/5 h = 5616 s.
	p := batteryStatePayload(-5.0, 7.8, 10.0, 0.78, 2)

	b, err := DecodeBatteryState(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if math.Abs(b.Percent-78) > 0.01 {
		t.Errorf("Percent = %v; want 78", b.Percent)
	}
	if b.SecondsRemaining != 5616 {
		t.Errorf("SecondsRemaining = %d; want 5616", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_ChargingCountsUpToFull(t *testing.T) {
	// 2 Ah of 10 Ah, charging at 4 A: (10-2)/4 h = 7200 s.
	b, err := DecodeBatteryState(batteryStatePayload(4.0, 2.0, 10.0, 0.20, 1))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryCharging {
		t.Errorf("State = %q; want charging", b.State)
	}
	if b.SecondsRemaining != 7200 {
		t.Errorf("SecondsRemaining = %d; want 7200", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_StatusEnumMapsOneToOne(t *testing.T) {
	want := []hoststats.BatteryState{
		hoststats.BatteryUnknown,
		hoststats.BatteryCharging,
		hoststats.BatteryDischarging,
		hoststats.BatteryNotCharging,
		hoststats.BatteryFull,
	}
	for status, exp := range want {
		b, err := DecodeBatteryState(batteryStatePayload(0, 5, 10, 0.5, uint8(status)))
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if b.State != exp {
			t.Errorf("status %d: State = %q; want %q", status, b.State, exp)
		}
	}
}

func TestDecodeBatteryState_PercentageAlreadyAPercent(t *testing.T) {
	// A driver that publishes 0..100 despite the spec saying 0..1.
	b, err := DecodeBatteryState(batteryStatePayload(-1, 5, 10, 64.0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(b.Percent-64) > 0.01 {
		t.Errorf("Percent = %v; want 64", b.Percent)
	}
}

func TestDecodeBatteryState_NaNPercentageFallsBackToCharge(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(-1, 2.5, 10.0, float32(math.NaN()), 2))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(b.Percent-25) > 0.01 {
		t.Errorf("Percent = %v; want 25", b.Percent)
	}
}

func TestDecodeBatteryState_NaNCurrentMeansNoEstimate(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(float32(math.NaN()), 7.8, 10.0, 0.78, 2))
	if err != nil {
		t.Fatal(err)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 (unknown)", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_Truncated(t *testing.T) {
	p := batteryStatePayload(-5.0, 7.8, 10.0, 0.78, 2)
	if _, err := DecodeBatteryState(p[:12]); err == nil {
		t.Fatal("expected an error decoding a truncated payload")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -v`
Expected: FAIL — `undefined: DecodeBatteryState`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package rosbattery decodes ROS 2 battery messages into the agent's existing
// hoststats.Battery shape. It knows message layouts and nothing about
// transport, so every decoder here is a pure function of bytes.
package rosbattery

import (
	"fmt"
	"math"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/rtps/cdr"
)

// TypeBatteryState is the DDS type name a sensor_msgs/msg/BatteryState writer
// advertises over SEDP. Phase 2 matches on this string.
const TypeBatteryState = "sensor_msgs::msg::dds_::BatteryState_"

// batteryStateStatus maps power_supply_status onto the agent's battery states.
// The ROS constants happen to line up exactly with what the sysfs path
// produces, so no new states are introduced.
var batteryStateStatus = map[uint8]hoststats.BatteryState{
	0: hoststats.BatteryUnknown,
	1: hoststats.BatteryCharging,
	2: hoststats.BatteryDischarging,
	3: hoststats.BatteryNotCharging,
	4: hoststats.BatteryFull,
}

// DecodeBatteryState decodes a sensor_msgs/msg/BatteryState CDR payload.
func DecodeBatteryState(payload []byte) (*hoststats.Battery, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, err
	}

	// std_msgs/Header: stamp {sec, nanosec} then frame_id.
	if _, err := d.Int32(); err != nil {
		return nil, fmt.Errorf("header.stamp.sec: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return nil, fmt.Errorf("header.stamp.nanosec: %w", err)
	}
	if err := d.SkipString(); err != nil {
		return nil, fmt.Errorf("header.frame_id: %w", err)
	}

	read := func(name string) (float32, error) {
		v, err := d.Float32()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return v, nil
	}
	if _, err := read("voltage"); err != nil {
		return nil, err
	}
	if _, err := read("temperature"); err != nil {
		return nil, err
	}
	current, err := read("current")
	if err != nil {
		return nil, err
	}
	charge, err := read("charge")
	if err != nil {
		return nil, err
	}
	capacity, err := read("capacity")
	if err != nil {
		return nil, err
	}
	if _, err := read("design_capacity"); err != nil {
		return nil, err
	}
	percentage, err := read("percentage")
	if err != nil {
		return nil, err
	}
	rawStatus, err := d.Uint8()
	if err != nil {
		return nil, fmt.Errorf("power_supply_status: %w", err)
	}

	state, ok := batteryStateStatus[rawStatus]
	if !ok {
		state = hoststats.BatteryUnknown
	}

	b := &hoststats.Battery{
		State:   state,
		Percent: batteryStatePercent(percentage, charge, capacity),
	}
	// charge/capacity are Ah and current is A, so they form one unit family
	// and the shared estimator applies unchanged.
	if !math.IsNaN(float64(current)) && !math.IsNaN(float64(charge)) && !math.IsNaN(float64(capacity)) {
		b.SecondsRemaining = hoststats.EstimateSecondsRemaining(
			state,
			float64(charge),
			float64(capacity),
			math.Abs(float64(current)),
		)
	}
	return b, nil
}

// batteryStatePercent converts the reported level to 0-100. The message spec
// says percentage is 0-1, but drivers publish 0-100 often enough that a value
// above 1 is taken at face value rather than clamped to full. NaN falls back
// to charge/capacity, and yields 0 when that is unusable too.
func batteryStatePercent(percentage, charge, capacity float32) float64 {
	p := float64(percentage)
	switch {
	case !math.IsNaN(p) && p > 1 && p <= 100:
		return p
	case !math.IsNaN(p) && p >= 0 && p <= 1:
		return p * 100
	}
	c, full := float64(charge), float64(capacity)
	if !math.IsNaN(c) && !math.IsNaN(full) && full > 0 {
		return math.Max(0, math.Min(100, c/full*100))
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -v`
Expected: PASS, all seven tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/agent/hoststats/rosbattery/
git add go/internal/agent/hoststats/rosbattery/
git commit -m "feat(rosbattery): decode sensor_msgs/BatteryState"
```

---

### Task 6: Decode `unitree_go/msg/LowState` → `bms_state`

**⚠ Before writing any code, open `testdata/lowstate.msg`, `bms_state.msg`,
`imu_state.msg`, and `motor_state.msg` from Task 1 and correct the layout below
to match them.** A wrong offset here decodes to a plausible wrong number rather
than an error, which is exactly why Step 1 pins the exact-consumption guard.

> **Superseded by the implementation.** `lowStateTrailerBytes = 76` below is
> wrong — walking the trailer with real alignment gives 96. The shipped
> decoder therefore has no such constant: `skipLowStateTrailer` walks every
> field individually so alignment is computed rather than baked in, and the
> exact-consumption check can actually detect drift. Read
> `go/internal/agent/hoststats/rosbattery/lowstate.go` rather than the code
> block below.

Provisional layout:

```
LowState: uint8[2] head; uint8 level_flag; uint8 frame_reserve;
          uint32[2] sn; uint32[2] version; uint16 bandwidth;
          IMUState imu_state; MotorState[20] motor_state; BmsState bms_state; ...
IMUState: float32[4] quaternion; float32[3] gyroscope;
          float32[3] accelerometer; float32[3] rpy; int8 temperature
MotorState: uint8 mode; float32 q, dq, ddq, tau_est, q_raw, dq_raw, ddq_raw;
          int8 temperature; uint32 lost; uint32[2] reserve
BmsState: uint8 version_high, version_low, status, soc; int32 current;
          uint16 cycle; int8[2] bq_ntc; int8[2] mcu_ntc; uint16[15] cell_vol
```

**Files:**
- Create: `go/internal/agent/hoststats/rosbattery/lowstate.go`
- Test: `go/internal/agent/hoststats/rosbattery/lowstate_test.go`

**Interfaces:**
- Consumes: `cdr` (Tasks 2–3), `hoststats.Battery` / `hoststats.BatteryState`
- Produces:
  - `const TypeLowState = "unitree_go::msg::dds_::LowState_"`
  - `func DecodeLowState(payload []byte) (*hoststats.Battery, error)`

`BmsState` has no capacity field, so this path reports a level and a direction
and never a countdown — `SecondsRemaining` stays 0, consistent with the
"never extrapolate" rule.

- [ ] **Step 1: Write the failing test**

```go
package rosbattery

import (
	"encoding/binary"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// lowStateBuilder assembles a little-endian LowState body with CDR alignment.
type lowStateBuilder struct{ b []byte }

func (w *lowStateBuilder) align(n int) {
	for len(w.b)%n != 0 {
		w.b = append(w.b, 0)
	}
}
func (w *lowStateBuilder) u8(v uint8)   { w.b = append(w.b, v) }
func (w *lowStateBuilder) pad(n int)    { w.b = append(w.b, make([]byte, n)...) }
func (w *lowStateBuilder) u16(v uint16) { w.align(2); w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *lowStateBuilder) u32(v uint32) { w.align(4); w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *lowStateBuilder) i32(v int32)  { w.u32(uint32(v)) }

// lowStatePayload builds a LowState whose bms_state carries soc and current,
// with every other field zeroed. trailing controls how many bytes follow
// bms_state, letting a test drive the exact-consumption guard.
func lowStatePayload(soc uint8, current int32, trailing int) []byte {
	w := &lowStateBuilder{}
	w.u8(0)
	w.u8(0)      // head[2]
	w.u8(0)      // level_flag
	w.u8(0)      // frame_reserve
	w.u32(0)
	w.u32(0)     // sn[2]
	w.u32(0)
	w.u32(0)     // version[2]
	w.u16(0)     // bandwidth

	// IMUState: 4+3+3+3 float32s, then int8.
	w.align(4)
	w.pad(13 * 4)
	w.u8(0)

	// MotorState[20]: mode, 7 float32, int8 temperature, uint32 lost,
	// uint32[2] reserve — 48 bytes each once aligned.
	for i := 0; i < 20; i++ {
		w.align(4)
		w.u8(0)      // mode
		w.pad(3)     // pad to 4
		w.pad(7 * 4) // q..ddq_raw
		w.u8(0)      // temperature
		w.pad(3)     // pad to 4
		w.u32(0)     // lost
		w.u32(0)
		w.u32(0)     // reserve[2]
	}

	// BmsState
	w.u8(0)        // version_high
	w.u8(0)        // version_low
	w.u8(0)        // status
	w.u8(soc)      // soc
	w.i32(current) // current, mA
	w.u16(0)       // cycle
	w.pad(2)       // bq_ntc[2]
	w.pad(2)       // mcu_ntc[2]
	w.pad(15 * 2)  // cell_vol[15]

	w.pad(trailing)
	return append([]byte{0x00, 0x01, 0x00, 0x00}, w.b...)
}

// lowStateTrailingBytes is the size of everything after bms_state in LowState.
// Correct this against testdata/lowstate.msg before implementing.
const lowStateTrailingBytes = 76

func TestDecodeLowState_Discharging(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(84, -3200, lowStateTrailingBytes))
	if err != nil {
		t.Fatal(err)
	}
	if b.Percent != 84 {
		t.Errorf("Percent = %v; want 84", b.Percent)
	}
	if b.State != hoststats.BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 — BmsState has no capacity field", b.SecondsRemaining)
	}
}

func TestDecodeLowState_Charging(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(30, 2500, lowStateTrailingBytes))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryCharging {
		t.Errorf("State = %q; want charging", b.State)
	}
}

func TestDecodeLowState_ZeroCurrentIsUnknownDirection(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(55, 0, lowStateTrailingBytes))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryUnknown {
		t.Errorf("State = %q; want unknown", b.State)
	}
	if b.Percent != 55 {
		t.Errorf("Percent = %v; want 55", b.Percent)
	}
}

// The guard that makes this decoder safe: if the layout assumption is wrong,
// the decoder will not land exactly on the end of the payload.
func TestDecodeLowState_RejectsWrongLength(t *testing.T) {
	if _, err := DecodeLowState(lowStatePayload(84, -3200, lowStateTrailingBytes+8)); err == nil {
		t.Fatal("expected an error when the payload is longer than the layout predicts")
	}
	if _, err := DecodeLowState(lowStatePayload(84, -3200, 0)); err == nil {
		t.Fatal("expected an error when the payload is shorter than the layout predicts")
	}
}

func TestDecodeLowState_RejectsImplausibleSoc(t *testing.T) {
	if _, err := DecodeLowState(lowStatePayload(200, -3200, lowStateTrailingBytes)); err == nil {
		t.Fatal("expected an error for soc > 100")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -run LowState -v`
Expected: FAIL — `undefined: DecodeLowState`.

- [ ] **Step 3: Write minimal implementation**

```go
package rosbattery

import (
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/rtps/cdr"
)

// TypeLowState is the DDS type name a unitree_go/msg/LowState writer
// advertises over SEDP.
const TypeLowState = "unitree_go::msg::dds_::LowState_"

// lowStateTrailerBytes is the size of every LowState field after bms_state:
// foot_force[4] and foot_force_est[4] (int16), tick (uint32),
// wireless_remote[40], bit_flag, adc_reel, two int8 NTCs, power_v, power_a,
// fan_frequency[4], reserve, crc. Correct against testdata/lowstate.msg.
const lowStateTrailerBytes = 76

// DecodeLowState extracts bms_state from a unitree_go/msg/LowState payload.
//
// Reaching bms_state means walking the whole preceding layout, including
// motor_state[20], so a firmware revision that shifts any offset would decode
// silently into a plausible wrong number. The decoder therefore asserts it
// consumed the payload exactly, and rejects the sample otherwise.
func DecodeLowState(payload []byte) (*hoststats.Battery, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, err
	}

	// head[2], level_flag, frame_reserve
	if err := d.SkipBytes(1, 4); err != nil {
		return nil, fmt.Errorf("head/level_flag/frame_reserve: %w", err)
	}
	// sn[2], version[2]
	if err := d.SkipBytes(4, 16); err != nil {
		return nil, fmt.Errorf("sn/version: %w", err)
	}
	if _, err := d.Uint16(); err != nil {
		return nil, fmt.Errorf("bandwidth: %w", err)
	}

	// IMUState: quaternion[4] + gyroscope[3] + accelerometer[3] + rpy[3]
	// float32s, then int8 temperature.
	if err := d.SkipBytes(4, 13*4); err != nil {
		return nil, fmt.Errorf("imu_state floats: %w", err)
	}
	if _, err := d.Int8(); err != nil {
		return nil, fmt.Errorf("imu_state.temperature: %w", err)
	}

	// MotorState[20]: mode(u8) pad(3) 7×float32 temperature(i8) pad(3)
	// lost(u32) reserve[2](u32) = 48 bytes each.
	for i := 0; i < 20; i++ {
		if err := d.SkipBytes(4, 48); err != nil {
			return nil, fmt.Errorf("motor_state[%d]: %w", i, err)
		}
	}

	// BmsState
	if err := d.SkipBytes(1, 3); err != nil { // version_high, version_low, status
		return nil, fmt.Errorf("bms_state version/status: %w", err)
	}
	soc, err := d.Uint8()
	if err != nil {
		return nil, fmt.Errorf("bms_state.soc: %w", err)
	}
	current, err := d.Int32()
	if err != nil {
		return nil, fmt.Errorf("bms_state.current: %w", err)
	}
	if _, err := d.Uint16(); err != nil { // cycle
		return nil, fmt.Errorf("bms_state.cycle: %w", err)
	}
	// bq_ntc[2], mcu_ntc[2]
	if err := d.SkipBytes(1, 4); err != nil {
		return nil, fmt.Errorf("bms_state ntc: %w", err)
	}
	// cell_vol[15]
	if err := d.SkipBytes(2, 30); err != nil {
		return nil, fmt.Errorf("bms_state.cell_vol: %w", err)
	}

	if soc > 100 {
		return nil, fmt.Errorf("bms_state.soc = %d, above 100: layout assumption is wrong", soc)
	}
	if err := d.SkipBytes(1, lowStateTrailerBytes); err != nil {
		return nil, fmt.Errorf("lowstate trailer: %w", err)
	}
	if rem := d.Remaining(); rem != 0 {
		return nil, fmt.Errorf("lowstate: %d bytes unconsumed: layout assumption is wrong", rem)
	}

	b := &hoststats.Battery{Percent: float64(soc)}
	switch {
	case current < 0:
		b.State = hoststats.BatteryDischarging
	case current > 0:
		b.State = hoststats.BatteryCharging
	default:
		b.State = hoststats.BatteryUnknown
	}
	// SecondsRemaining stays 0: BmsState carries no capacity, and the estimate
	// is never extrapolated.
	return b, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -v`
Expected: PASS. If `TestDecodeLowState_RejectsWrongLength` fails on the
*shorter* case while the others pass, `lowStateTrailerBytes` disagrees with
`testdata/lowstate.msg` — recompute it from the captured definition rather than
adjusting the constant until the test goes green.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/agent/hoststats/rosbattery/
git add go/internal/agent/hoststats/rosbattery/
git commit -m "feat(rosbattery): decode bms_state from unitree_go/LowState"
```

---

### Task 7: Staleness-aware sample cache

The monitor in Phase 3 writes samples in; the `hoststats` resolver reads them
out. The cache is what makes a dead publisher disappear rather than freeze, and
since the design deliberately hides the reading's source, it is the only defence
against rendering a stale number confidently.

**Files:**
- Create: `go/internal/agent/hoststats/rosbattery/cache.go`
- Test: `go/internal/agent/hoststats/rosbattery/cache_test.go`

**Interfaces:**
- Consumes: `hoststats.Battery`
- Produces:
  - `const StaleAfter = 15 * time.Second`
  - `func NewCache(now func() time.Time) *Cache`
  - `func (*Cache) Put(b *hoststats.Battery)`
  - `func (*Cache) Battery() *hoststats.Battery`

`Cache` must be safe for concurrent use: the monitor goroutine calls `Put` while
gRPC handlers call `Battery`.

- [ ] **Step 1: Write the failing test**

```go
package rosbattery

import (
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestCache_EmptyReturnsNil(t *testing.T) {
	c := NewCache((&fakeClock{t: time.Unix(1000, 0)}).now)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil from an empty cache, got %+v", b)
	}
}

func TestCache_FreshSampleIsReturned(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78, State: hoststats.BatteryDischarging})

	clk.advance(StaleAfter - time.Second)
	b := c.Battery()
	if b == nil {
		t.Fatal("expected a battery just inside the staleness window")
	}
	if b.Percent != 78 {
		t.Errorf("Percent = %v; want 78", b.Percent)
	}
}

func TestCache_StaleSampleIsDropped(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	clk.advance(StaleAfter + time.Second)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil past the staleness window, got %+v", b)
	}
}

func TestCache_PutRefreshesTheWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	clk.advance(StaleAfter - time.Second)
	c.Put(&hoststats.Battery{Percent: 60})
	clk.advance(StaleAfter - time.Second)

	b := c.Battery()
	if b == nil {
		t.Fatal("expected the refreshed sample to still be live")
	}
	if b.Percent != 60 {
		t.Errorf("Percent = %v; want 60", b.Percent)
	}
}

func TestCache_PutNilClears(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})
	c.Put(nil)
	if b := c.Battery(); b != nil {
		t.Errorf("expected nil after Put(nil), got %+v", b)
	}
}

// Battery must hand back a copy: a caller mutating the result must not corrupt
// what the next caller sees.
func TestCache_BatteryReturnsACopy(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)
	c.Put(&hoststats.Battery{Percent: 78})

	first := c.Battery()
	first.Percent = 1

	if second := c.Battery(); second.Percent != 78 {
		t.Errorf("Percent = %v; want 78 — Battery must return a copy", second.Percent)
	}
}

func TestCache_ConcurrentUse(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewCache(clk.now)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); for j := 0; j < 200; j++ { c.Put(&hoststats.Battery{Percent: 50}) } }()
		go func() { defer wg.Done(); for j := 0; j < 200; j++ { _ = c.Battery() } }()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -run TestCache -v`
Expected: FAIL — `undefined: NewCache`.

- [ ] **Step 3: Write minimal implementation**

```go
package rosbattery

import (
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// StaleAfter is how long a sample stays usable. BatteryState republishers
// typically run at 1-10 Hz and LowState far faster, so this is generous for
// both while making a dead publisher vanish from `wendy device top` within a
// refresh or two. It is deliberately tight: the reading's source is not
// exposed anywhere, so staleness is the only thing preventing a stale number
// from rendering as a confident one.
const StaleAfter = 15 * time.Second

// Cache holds the newest decoded sample and expires it. Safe for concurrent
// use: the monitor goroutine calls Put while gRPC handlers call Battery.
type Cache struct {
	now func() time.Time

	mu   sync.RWMutex
	b    *hoststats.Battery
	seen time.Time
}

// NewCache returns an empty cache reading time from now.
func NewCache(now func() time.Time) *Cache {
	return &Cache{now: now}
}

// Put stores a sample and restarts its staleness window. A nil sample clears
// the cache, which is how the monitor reports that its writer went away.
func (c *Cache) Put(b *hoststats.Battery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b == nil {
		c.b, c.seen = nil, time.Time{}
		return
	}
	cp := *b
	c.b, c.seen = &cp, c.now()
}

// Battery returns a copy of the newest sample, or nil when the cache is empty
// or the sample has gone stale.
func (c *Cache) Battery() *hoststats.Battery {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.b == nil || c.now().Sub(c.seen) > StaleAfter {
		return nil
	}
	cp := *c.b
	return &cp
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/hoststats/rosbattery/ -race -v`
Expected: PASS, with no race detector findings.

- [ ] **Step 5: Commit**

```bash
gofmt -w go/internal/agent/hoststats/rosbattery/
git add go/internal/agent/hoststats/rosbattery/
git commit -m "feat(rosbattery): add a staleness-aware battery sample cache"
```

---

### Task 8: Phase gate — full suite and fixture cross-check

**Files:**
- Modify: `go/internal/agent/hoststats/rosbattery/testdata/README.md`

**Interfaces:**
- Consumes: everything above
- Produces: nothing; this is the gate before Phase 2

- [ ] **Step 1: Run the whole affected suite with the race detector**

Run: `go test ./go/internal/rtps/... ./go/internal/agent/hoststats/... -race`
Expected: PASS, including every pre-existing `battery_test.go` test.

- [ ] **Step 2: Confirm the build stays cgo-free**

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./go/...
```

Expected: success. This is the agent's release configuration.

- [ ] **Step 3: Cross-check a decoded value against the robot**

Compare the `percentage` and `power_supply_status` in
`testdata/battery_state_sample.yaml` against what `DecodeBatteryState` produces
for the same inputs, and record the comparison in the fixture README under a
`## Verified` heading — the observed percentage, the state it maps to, and
whether the (1, 100] heuristic was exercised.

- [ ] **Step 4: Commit**

```bash
git add go/internal/agent/hoststats/rosbattery/testdata/README.md
git commit -m "test(rosbattery): record decoder cross-check against the robot"
```

---

## What Phase 1 does not do

No network code, no discovery, and nothing wired into the agent — after this
plan the decoders exist and are tested, but `wendy device info` output is
unchanged. Phase 2 (`go/internal/rtps`: SPDP/SEDP discovery and a best-effort
reader) and Phase 3 (`rosbattery.Monitor`, `/etc/wendy-agent/ros2-battery.json`,
and the `hoststats` source resolution wired into `agent_service.go:148` and
`container_service.go:1207`) follow as separate plans.
