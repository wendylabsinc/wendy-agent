# Foxglove native-CDR ROS 2 bridge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-topic rclpy subprocess (subscribe) and YAML `ros2 topic pub` (publish) with a single long-lived compiled C++ ROS 2 bridge per graph that streams native CDR both ways, multiplexed over one process.

**Architecture:** A C++ `rclcpp` node (`wendy-ros2-bridge`) is exec'd inside the existing ROS 2 CLI sidecar and driven by a length-framed binary control protocol on stdin/stdout. A Go `ros2Bridge` manager in the agent owns the process lifecycle and fans MESSAGE frames out to per-subscription gRPC streams. The bridge is strictly additive: if its binary is missing or fails to start, `SubscribeRaw`/`Publish` fall back to today's rclpy/YAML paths, so no device regresses.

**Tech Stack:** C++17 + `rclcpp` (`GenericSubscription`/`GenericPublisher`), `ament_cmake`/`colcon`; Go (agent, containerd, gRPC); protoc-generated `agentpbv2`; `go:embed`.

## Global Constraints

- **Target distros:** Humble and Jazzy only. Subscribe + publish are native on both.
- **Device arch = agent `GOARCH`:** each agent embeds only its own arch's binaries (`arm64` agent → arm64 humble+jazzy; `amd64` likewise), selected by Go build tag.
- **Strictly additive:** every bridge path has a fallback to today's behavior; a missing/broken bridge never fails a request that previously worked.
- **No shell interpolation of untrusted input:** binary + args are passed via `"$@"`; topic/type/service names are validated by the existing `validateROS2GraphName` before reaching the bridge.
- **Frame format (canonical):** every frame is `[uint32 LE total_len][uint8 tag][payload]`, where `total_len` counts `tag` + `payload` (not the 4 length bytes). Strings are `[uint16 LE len][bytes]`. Trailing CDR runs to `4 + total_len`.
- **Sidecar image is not Wendy-owned:** it is stock `ros:<distro>` or the app's own image; the bridge binary reaches it only by host bind-mount added at sidecar creation.
- **Spec:** `specs/superpowers/2026-07-07-foxglove-native-cdr-bridge-design.md`.

---

## Wire protocol (shared contract)

Agent → bridge (stdin), tag = op:

| op | name | payload after tag |
|----|------|-------------------|
| 1 | SUBSCRIBE | `[u32 subID][str topic][str type][u8 qos]` (qos: 0 = auto-match, 1 = force best-effort/depth-1) |
| 2 | UNSUBSCRIBE | `[u32 subID]` |
| 3 | PUBLISH | `[str topic][str type][cdr…]` |

Bridge → agent (stdout), tag = kind:

| kind | name | payload after tag |
|------|------|-------------------|
| 1 | MESSAGE | `[u32 subID][u64 ts_ns][cdr…]` |
| 3 | SUB_ERROR | `[u32 subID][str msg]` |
| 4 | READY | `[str distro][u8 caps]` (caps reserved; bit 0 = generic service client present) |

Service calls (op 4 / kind 2) are **out of v1 scope** (Task 11, deferred). The op/kind numbers are reserved so the codec is forward-compatible.

---

### Task 1: Go protocol codec (`foxglovebridge`)

New package holding the shared frame codec, used by the manager (Task 7) and its tests. Pure bytes, no ROS/containerd deps.

**Files:**
- Create: `go/internal/agent/foxglovebridge/protocol.go`
- Test: `go/internal/agent/foxglovebridge/protocol_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - Consts `OpSubscribe=1`, `OpUnsubscribe=2`, `OpPublish=3`; `KindMessage=1`, `KindSubError=3`, `KindReady=4`; `QoSAuto=0`, `QoSForceBestEffort=1`.
  - `func AppendString(dst []byte, s string) []byte`
  - `func AppendSubscribe(dst []byte, subID uint32, topic, msgType string, qos uint8) []byte`
  - `func AppendUnsubscribe(dst []byte, subID uint32) []byte`
  - `func AppendPublish(dst []byte, topic, msgType string, cdr []byte) []byte`
  - `type Frame struct { Tag uint8; Body []byte }`
  - `func ReadFrame(r io.Reader, buf []byte) (Frame, []byte, error)` — returns the frame (Body aliases the returned buffer) and the (possibly grown) buffer to reuse next call; `io.EOF` at a clean boundary.
  - `type Message struct { SubID uint32; TimestampNs int64; CDR []byte }`
  - `func ParseMessage(body []byte) (Message, error)`
  - `func ParseSubError(body []byte) (subID uint32, msg string, err error)`
  - `func ParseReady(body []byte) (distro string, caps uint8, err error)`

- [ ] **Step 1: Write the failing test**

```go
package foxglovebridge

import (
	"bytes"
	"io"
	"testing"
)

func TestSubscribeRoundTripFrames(t *testing.T) {
	// A SUBSCRIBE command followed by a MESSAGE event, back-to-back in one stream.
	var stream []byte
	stream = AppendSubscribe(stream, 7, "/img", "sensor_msgs/msg/Image", QoSAuto)

	// Hand-build a MESSAGE frame the way the bridge would, to prove ParseMessage.
	var msg []byte
	msg = appendU32(msg, 7)
	msg = appendU64(msg, 123456789)
	msg = append(msg, 0xDE, 0xAD)
	var evt []byte
	evt = appendFrame(evt, KindMessage, msg)
	stream = append(stream, evt...)

	r := bytes.NewReader(stream)
	var buf []byte

	f1, buf, err := ReadFrame(r, buf)
	if err != nil {
		t.Fatalf("read subscribe: %v", err)
	}
	if f1.Tag != OpSubscribe {
		t.Fatalf("tag = %d, want %d", f1.Tag, OpSubscribe)
	}

	f2, buf, err := ReadFrame(r, buf)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	m, err := ParseMessage(f2.Body)
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if m.SubID != 7 || m.TimestampNs != 123456789 || !bytes.Equal(m.CDR, []byte{0xDE, 0xAD}) {
		t.Fatalf("message = %+v", m)
	}

	if _, _, err := ReadFrame(r, buf); err != io.EOF {
		t.Fatalf("want clean EOF, got %v", err)
	}
}

func TestReadyAndSubError(t *testing.T) {
	var s []byte
	s = appendFrame(s, KindReady, appendString(appendString(nil, "jazzy")[:len(appendString(nil, "jazzy"))], "")[:0]) // placeholder, replaced below
	_ = s
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/foxglovebridge/ -run TestSubscribeRoundTripFrames -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// Package foxglovebridge is the length-framed binary control protocol spoken
// between the agent and the compiled wendy-ros2-bridge process. Frame layout:
//
//	[uint32 LE total_len][uint8 tag][payload]   total_len counts tag+payload
//
// Strings inside a payload are [uint16 LE len][bytes]; a trailing CDR payload
// runs to the end of the frame.
package foxglovebridge

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	OpSubscribe   uint8 = 1
	OpUnsubscribe uint8 = 2
	OpPublish     uint8 = 3

	KindMessage  uint8 = 1
	KindSubError uint8 = 3
	KindReady    uint8 = 4

	QoSAuto            uint8 = 0
	QoSForceBestEffort uint8 = 1
)

func appendU16(dst []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(dst, v) }
func appendU32(dst []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(dst, v) }
func appendU64(dst []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(dst, v) }

func appendString(dst []byte, s string) []byte {
	dst = appendU16(dst, uint16(len(s)))
	return append(dst, s...)
}

// AppendString is the exported form used by callers building custom frames.
func AppendString(dst []byte, s string) []byte { return appendString(dst, s) }

// appendFrame wraps body in a [len][tag][body] envelope.
func appendFrame(dst []byte, tag uint8, body []byte) []byte {
	dst = appendU32(dst, uint32(1+len(body)))
	dst = append(dst, tag)
	return append(dst, body...)
}

func AppendSubscribe(dst []byte, subID uint32, topic, msgType string, qos uint8) []byte {
	var b []byte
	b = appendU32(b, subID)
	b = appendString(b, topic)
	b = appendString(b, msgType)
	b = append(b, qos)
	return appendFrame(dst, OpSubscribe, b)
}

func AppendUnsubscribe(dst []byte, subID uint32) []byte {
	return appendFrame(dst, OpUnsubscribe, appendU32(nil, subID))
}

func AppendPublish(dst []byte, topic, msgType string, cdr []byte) []byte {
	var b []byte
	b = appendString(b, topic)
	b = appendString(b, msgType)
	b = append(b, cdr...)
	return appendFrame(dst, OpPublish, b)
}

// Frame is one decoded envelope. Body aliases the reusable buffer returned by
// ReadFrame; copy anything you retain past the next ReadFrame call.
type Frame struct {
	Tag  uint8
	Body []byte
}

// maxFrame caps a single frame at 128 MiB to bound memory on a corrupt stream.
const maxFrame = 128 << 20

func ReadFrame(r io.Reader, buf []byte) (Frame, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Frame{}, buf, fmt.Errorf("truncated frame length")
		}
		return Frame{}, buf, err // io.EOF at a clean boundary
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 {
		return Frame{}, buf, fmt.Errorf("zero-length frame")
	}
	if n > maxFrame {
		return Frame{}, buf, fmt.Errorf("frame too large: %d", n)
	}
	if uint32(cap(buf)) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, buf, fmt.Errorf("truncated frame body (want %d): %w", n, err)
	}
	return Frame{Tag: buf[0], Body: buf[1:]}, buf, nil
}

type Message struct {
	SubID       uint32
	TimestampNs int64
	CDR         []byte
}

func ParseMessage(body []byte) (Message, error) {
	if len(body) < 12 {
		return Message{}, fmt.Errorf("MESSAGE body too short: %d", len(body))
	}
	return Message{
		SubID:       binary.LittleEndian.Uint32(body[0:4]),
		TimestampNs: int64(binary.LittleEndian.Uint64(body[4:12])),
		CDR:         body[12:],
	}, nil
}

func readString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, fmt.Errorf("string length truncated")
	}
	n := int(binary.LittleEndian.Uint16(b[0:2]))
	b = b[2:]
	if len(b) < n {
		return "", nil, fmt.Errorf("string body truncated: want %d have %d", n, len(b))
	}
	return string(b[:n]), b[n:], nil
}

func ParseSubError(body []byte) (uint32, string, error) {
	if len(body) < 4 {
		return 0, "", fmt.Errorf("SUB_ERROR body too short")
	}
	subID := binary.LittleEndian.Uint32(body[0:4])
	msg, _, err := readString(body[4:])
	return subID, msg, err
}

func ParseReady(body []byte) (string, uint8, error) {
	distro, rest, err := readString(body)
	if err != nil {
		return "", 0, err
	}
	if len(rest) < 1 {
		return "", 0, fmt.Errorf("READY missing caps byte")
	}
	return distro, rest[0], nil
}
```

- [ ] **Step 4: Replace the placeholder `TestReadyAndSubError` with a real test**

```go
func TestReadyAndSubError(t *testing.T) {
	var ready []byte
	ready = appendString(ready, "jazzy")
	ready = append(ready, 0x01)
	distro, caps, err := ParseReady(ready)
	if err != nil || distro != "jazzy" || caps != 0x01 {
		t.Fatalf("ready = %q %d %v", distro, caps, err)
	}

	var se []byte
	se = appendU32(se, 9)
	se = appendString(se, "boom")
	id, msg, err := ParseSubError(se)
	if err != nil || id != 9 || msg != "boom" {
		t.Fatalf("suberror = %d %q %v", id, msg, err)
	}
}

func TestReadFrameTruncated(t *testing.T) {
	// length says 10 but only 3 bytes follow.
	stream := append(appendU32(nil, 10), 1, 2, 3)
	_, _, err := ReadFrame(bytesReader(stream), nil)
	if err == nil {
		t.Fatal("want truncation error")
	}
}
```

Add the tiny helper at the bottom of the test file:

```go
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/foxglovebridge/ -v`
Expected: PASS (all three tests).

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/foxglovebridge/
git commit -m "feat(ros2): Go codec for the Foxglove native-CDR bridge protocol"
```

---

### Task 2: C++ bridge package skeleton + frame codec

Create the `ament_cmake` package and its pure-bytes frame codec (no rclcpp yet) so the build and a codec unit test pass before adding ROS.

**Files:**
- Create: `ros2/wendy_ros2_bridge/package.xml`
- Create: `ros2/wendy_ros2_bridge/CMakeLists.txt`
- Create: `ros2/wendy_ros2_bridge/src/protocol.hpp`
- Create: `ros2/wendy_ros2_bridge/test/test_protocol.cpp`

**Interfaces:**
- Produces (C++, namespace `wendy_bridge`): `append_u16/u32/u64`, `append_string`, `Framer::begin/end`, `FrameReader::next(uint8_t& tag, std::vector<uint8_t>& body)`; matching op/kind/qos constants mirroring Task 1.

- [ ] **Step 1: Write `package.xml`**

```xml
<?xml version="1.0"?>
<?xml-model href="http://download.ros.org/schema/package_format3.xsd" schematypens="http://www.w3.org/2001/XMLSchema"?>
<package format="3">
  <name>wendy_ros2_bridge</name>
  <version>0.1.0</version>
  <description>Wendy Foxglove native-CDR data-plane bridge (subscribe/publish).</description>
  <maintainer email="joannis@wendy.sh">Wendy Labs</maintainer>
  <license>Proprietary</license>

  <buildtool_depend>ament_cmake</buildtool_depend>
  <depend>rclcpp</depend>

  <test_depend>ament_cmake_gtest</test_depend>

  <export>
    <build_type>ament_cmake</build_type>
  </export>
</package>
```

- [ ] **Step 2: Write `CMakeLists.txt`**

```cmake
cmake_minimum_required(VERSION 3.8)
project(wendy_ros2_bridge)

if(NOT CMAKE_CXX_STANDARD)
  set(CMAKE_CXX_STANDARD 17)
endif()
if(CMAKE_COMPILER_IS_GNUCXX OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

find_package(ament_cmake REQUIRED)
find_package(rclcpp REQUIRED)

add_executable(wendy-ros2-bridge src/bridge_main.cpp)
target_include_directories(wendy-ros2-bridge PRIVATE src)
ament_target_dependencies(wendy-ros2-bridge rclcpp)

install(TARGETS wendy-ros2-bridge DESTINATION lib/${PROJECT_NAME})

if(BUILD_TESTING)
  find_package(ament_cmake_gtest REQUIRED)
  ament_add_gtest(test_protocol test/test_protocol.cpp)
  target_include_directories(test_protocol PRIVATE src)
endif()

ament_package()
```

- [ ] **Step 3: Write `src/protocol.hpp`**

```cpp
#pragma once
#include <cstdint>
#include <cstring>
#include <string>
#include <vector>
#include <stdexcept>
#include <cstdio>

namespace wendy_bridge {

enum Op : uint8_t { OP_SUBSCRIBE = 1, OP_UNSUBSCRIBE = 2, OP_PUBLISH = 3 };
enum Kind : uint8_t { KIND_MESSAGE = 1, KIND_SUB_ERROR = 3, KIND_READY = 4 };
enum QoS : uint8_t { QOS_AUTO = 0, QOS_FORCE_BEST_EFFORT = 1 };

inline void append_u16(std::vector<uint8_t>& b, uint16_t v) {
  b.push_back(uint8_t(v)); b.push_back(uint8_t(v >> 8));
}
inline void append_u32(std::vector<uint8_t>& b, uint32_t v) {
  for (int i = 0; i < 4; i++) b.push_back(uint8_t(v >> (8 * i)));
}
inline void append_u64(std::vector<uint8_t>& b, uint64_t v) {
  for (int i = 0; i < 8; i++) b.push_back(uint8_t(v >> (8 * i)));
}
inline void append_string(std::vector<uint8_t>& b, const std::string& s) {
  append_u16(b, uint16_t(s.size()));
  b.insert(b.end(), s.begin(), s.end());
}

inline uint16_t read_u16(const uint8_t* p) { return uint16_t(p[0]) | uint16_t(p[1]) << 8; }
inline uint32_t read_u32(const uint8_t* p) {
  return uint32_t(p[0]) | uint32_t(p[1]) << 8 | uint32_t(p[2]) << 16 | uint32_t(p[3]) << 24;
}

// Framer writes [u32 len][tag][body] envelopes to a FILE* (thread-caller holds
// the lock). Returns false on write error.
class Framer {
 public:
  explicit Framer(std::FILE* out) : out_(out) {}
  bool write(uint8_t tag, const std::vector<uint8_t>& body) {
    uint8_t hdr[5];
    uint32_t n = uint32_t(1 + body.size());
    for (int i = 0; i < 4; i++) hdr[i] = uint8_t(n >> (8 * i));
    hdr[4] = tag;
    if (std::fwrite(hdr, 1, 5, out_) != 5) return false;
    if (!body.empty() && std::fwrite(body.data(), 1, body.size(), out_) != body.size()) return false;
    return std::fflush(out_) == 0;
  }
 private:
  std::FILE* out_;
};

// FrameReader reads envelopes from a FILE*. next() returns false on clean EOF,
// throws std::runtime_error on a truncated/oversized frame.
class FrameReader {
 public:
  explicit FrameReader(std::FILE* in) : in_(in) {}
  static constexpr uint32_t kMaxFrame = 128u << 20;
  bool next(uint8_t& tag, std::vector<uint8_t>& body) {
    uint8_t hdr[4];
    size_t got = std::fread(hdr, 1, 4, in_);
    if (got == 0) return false;
    if (got != 4) throw std::runtime_error("truncated frame length");
    uint32_t n = read_u32(hdr);
    if (n == 0 || n > kMaxFrame) throw std::runtime_error("bad frame length");
    std::vector<uint8_t> frame(n);
    if (std::fread(frame.data(), 1, n, in_) != n) throw std::runtime_error("truncated frame body");
    tag = frame[0];
    body.assign(frame.begin() + 1, frame.end());
    return true;
  }
 private:
  std::FILE* in_;
};

}  // namespace wendy_bridge
```

- [ ] **Step 4: Write `test/test_protocol.cpp`**

```cpp
#include <gtest/gtest.h>
#include "protocol.hpp"
using namespace wendy_bridge;

TEST(Protocol, StringRoundTrip) {
  std::vector<uint8_t> b;
  append_string(b, "hello");
  ASSERT_EQ(read_u16(b.data()), 5u);
  EXPECT_EQ(std::string(b.begin() + 2, b.end()), "hello");
}

TEST(Protocol, U64LittleEndian) {
  std::vector<uint8_t> b;
  append_u64(b, 0x0102030405060708ull);
  EXPECT_EQ(b[0], 0x08);
  EXPECT_EQ(b[7], 0x01);
}
```

- [ ] **Step 5: Add a stub `src/bridge_main.cpp` so the target links**

```cpp
#include "protocol.hpp"
int main() { return 0; }  // replaced in Task 3
```

- [ ] **Step 6: Build + test in a Humble container**

Run:
```bash
docker run --rm -v "$PWD/ros2/wendy_ros2_bridge:/ws/src/wendy_ros2_bridge" -w /ws ros:humble \
  bash -lc "source /opt/ros/humble/setup.bash && colcon build && colcon test && colcon test-result --verbose"
```
Expected: build succeeds; `test_protocol` PASSES.

- [ ] **Step 7: Commit**

```bash
git add ros2/wendy_ros2_bridge/
git commit -m "feat(ros2-bridge): ament package skeleton + C++ frame codec"
```

---

### Task 3: Bridge node — subscribe path (native raw CDR + QoS auto-match)

Replace the stub `bridge_main.cpp` with the real node: read SUBSCRIBE/UNSUBSCRIBE, create raw generic subscriptions, emit MESSAGE frames, emit READY at startup.

**Files:**
- Modify: `ros2/wendy_ros2_bridge/src/bridge_main.cpp`

**Interfaces:**
- Consumes: `protocol.hpp` (Task 2).
- Produces: the `wendy-ros2-bridge` executable honoring op 1/2 and emitting kind 1/3/4.

- [ ] **Step 1: Write `src/bridge_main.cpp`**

```cpp
// wendy-ros2-bridge: reads the Wendy bridge control protocol on stdin and writes
// framed events on stdout. Subscribe path uses rclcpp generic (raw CDR)
// subscriptions so DDS's serialized bytes flow straight through to Foxglove.
#include <atomic>
#include <chrono>
#include <map>
#include <mutex>
#include <thread>

#include <rclcpp/rclcpp.hpp>
#include <rclcpp/generic_subscription.hpp>
#include <rclcpp/serialized_message.hpp>

#include "protocol.hpp"

using namespace wendy_bridge;

namespace {

std::mutex g_out_mu;  // serializes all stdout writes
Framer* g_framer = nullptr;

int64_t now_ns() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(
             std::chrono::system_clock::now().time_since_epoch())
      .count();
}

void emit(uint8_t kind, const std::vector<uint8_t>& body) {
  std::lock_guard<std::mutex> lk(g_out_mu);
  g_framer->write(kind, body);
}

void emit_sub_error(uint32_t sub_id, const std::string& msg) {
  std::vector<uint8_t> b;
  append_u32(b, sub_id);
  append_string(b, msg);
  emit(KIND_SUB_ERROR, b);
}

// Pick a QoS compatible with the topic's publishers. Falls back to
// best-effort/KEEP_LAST(1) when none are visible yet or when forced.
rclcpp::QoS choose_qos(rclcpp::Node& node, const std::string& topic, uint8_t qos_flag) {
  if (qos_flag == QOS_FORCE_BEST_EFFORT) {
    return rclcpp::QoS(rclcpp::KeepLast(1)).best_effort();
  }
  auto infos = node.get_publishers_info_by_topic(topic);
  bool any_reliable = false, any_transient_local = false;
  for (const auto& info : infos) {
    const auto& q = info.qos_profile();
    if (q.reliability() == rclcpp::ReliabilityPolicy::Reliable) any_reliable = true;
    if (q.durability() == rclcpp::DurabilityPolicy::TransientLocal) any_transient_local = true;
  }
  rclcpp::QoS q(rclcpp::KeepLast(10));
  if (any_reliable) q.reliable(); else q.best_effort();
  if (any_transient_local) q.transient_local();
  return q;
}

}  // namespace

int main(int argc, char** argv) {
  rclcpp::init(argc, argv);
  auto node = std::make_shared<rclcpp::Node>("wendy_foxglove_bridge");

  Framer framer(stdout);
  g_framer = &framer;

  // READY: report distro (from env) and caps (no generic service client in v1).
  {
    const char* distro = std::getenv("ROS_DISTRO");
    std::vector<uint8_t> b;
    append_string(b, distro ? distro : "");
    b.push_back(0);  // caps: bit0=0 (services handled by fallback)
    emit(KIND_READY, b);
  }

  std::map<uint32_t, rclcpp::GenericSubscription::SharedPtr> subs;
  std::mutex subs_mu;

  // Reader thread: parse stdin commands and mutate the subscription table.
  std::atomic<bool> stop{false};
  std::thread reader([&] {
    FrameReader fr(stdin);
    uint8_t tag;
    std::vector<uint8_t> body;
    try {
      while (!stop && fr.next(tag, body)) {
        if (tag == OP_SUBSCRIBE) {
          const uint8_t* p = body.data();
          uint32_t sub_id = read_u32(p); p += 4;
          uint16_t tn = read_u16(p); p += 2;
          std::string topic((const char*)p, tn); p += tn;
          uint16_t yn = read_u16(p); p += 2;
          std::string type((const char*)p, yn); p += yn;
          uint8_t qos_flag = *p;
          try {
            auto qos = choose_qos(*node, topic, qos_flag);
            auto sub = node->create_generic_subscription(
                topic, type, qos,
                [sub_id](std::shared_ptr<rclcpp::SerializedMessage> msg) {
                  const auto& rcl = msg->get_rcl_serialized_message();
                  std::vector<uint8_t> out;
                  out.reserve(12 + rcl.buffer_length);
                  append_u32(out, sub_id);
                  append_u64(out, uint64_t(now_ns()));
                  out.insert(out.end(), rcl.buffer, rcl.buffer + rcl.buffer_length);
                  emit(KIND_MESSAGE, out);
                });
            std::lock_guard<std::mutex> lk(subs_mu);
            subs[sub_id] = sub;
          } catch (const std::exception& e) {
            emit_sub_error(sub_id, e.what());
          }
        } else if (tag == OP_UNSUBSCRIBE) {
          uint32_t sub_id = read_u32(body.data());
          std::lock_guard<std::mutex> lk(subs_mu);
          subs.erase(sub_id);
        }
        // OP_PUBLISH handled in Task 4.
      }
    } catch (const std::exception&) {
      // stdin closed or corrupt: fall through to shutdown.
    }
    stop = true;
    rclcpp::shutdown();
  });

  rclcpp::executors::MultiThreadedExecutor exec;
  exec.add_node(node);
  exec.spin();

  stop = true;
  reader.join();
  return 0;
}
```

- [ ] **Step 2: Build in Humble and Jazzy**

Run (both distros):
```bash
for d in humble jazzy; do
  docker run --rm -v "$PWD/ros2/wendy_ros2_bridge:/ws/src/wendy_ros2_bridge" -w /ws ros:$d \
    bash -lc "source /opt/ros/$d/setup.bash && colcon build" || exit 1
done
```
Expected: both builds succeed. (Fix any rclcpp signature drift the compiler flags — e.g. Humble's callback takes `std::shared_ptr<rclcpp::SerializedMessage>`, which this matches.)

- [ ] **Step 3: Manual smoke test against a live talker**

Run:
```bash
docker run --rm -it -v "$PWD/ros2/wendy_ros2_bridge:/ws/src/wendy_ros2_bridge" -w /ws ros:humble bash -lc '
  source /opt/ros/humble/setup.bash && colcon build && source install/setup.bash &&
  ros2 run demo_nodes_cpp talker & sleep 2 &&
  printf "" | ros2 run wendy_ros2_bridge wendy-ros2-bridge >/tmp/out.bin & sleep 1 &&
  # drive one SUBSCRIBE for /chatter via a tiny python framer:
  python3 - <<PY | ros2 run wendy_ros2_bridge wendy-ros2-bridge | head -c 200 | xxd | head
import sys,struct
def s(x): return struct.pack("<H",len(x))+x.encode()
body=struct.pack("<I",1)+s("/chatter")+s("std_msgs/msg/String")+b"\x00"
sys.stdout.buffer.write(struct.pack("<I",1+len(body))+b"\x01"+body); sys.stdout.buffer.flush()
import time; time.sleep(3)
PY
'
```
Expected: hex dump shows READY then MESSAGE frames (tag `0x01`) carrying CDR.

- [ ] **Step 4: Commit**

```bash
git add ros2/wendy_ros2_bridge/src/bridge_main.cpp
git commit -m "feat(ros2-bridge): native raw-CDR subscribe path with QoS auto-match"
```

---

### Task 4: Bridge node — publish path (native raw CDR)

Handle OP_PUBLISH with a cached generic publisher per (topic,type), publishing the client's CDR verbatim.

**Files:**
- Modify: `ros2/wendy_ros2_bridge/src/bridge_main.cpp`

- [ ] **Step 1: Add the publisher cache and OP_PUBLISH branch**

Add near the subscription map:

```cpp
  std::map<std::string, rclcpp::GenericPublisher::SharedPtr> pubs;
  std::mutex pubs_mu;
```

Add this branch in the reader loop, after OP_UNSUBSCRIBE:

```cpp
        else if (tag == OP_PUBLISH) {
          const uint8_t* p = body.data();
          const uint8_t* end = body.data() + body.size();
          uint16_t tn = read_u16(p); p += 2;
          std::string topic((const char*)p, tn); p += tn;
          uint16_t yn = read_u16(p); p += 2;
          std::string type((const char*)p, yn); p += yn;
          size_t cdr_len = size_t(end - p);
          std::string key = type + "\n" + topic;
          rclcpp::GenericPublisher::SharedPtr pub;
          {
            std::lock_guard<std::mutex> lk(pubs_mu);
            auto it = pubs.find(key);
            if (it == pubs.end()) {
              pub = node->create_generic_publisher(topic, type, rclcpp::QoS(rclcpp::KeepLast(10)));
              pubs[key] = pub;
            } else {
              pub = it->second;
            }
          }
          rclcpp::SerializedMessage sm(cdr_len);
          auto& rcl = sm.get_rcl_serialized_message();
          std::memcpy(rcl.buffer, p, cdr_len);
          rcl.buffer_length = cdr_len;
          pub->publish(sm);
        }
```

Add the include near the top:

```cpp
#include <rclcpp/generic_publisher.hpp>
```

- [ ] **Step 2: Build both distros**

Run:
```bash
for d in humble jazzy; do
  docker run --rm -v "$PWD/ros2/wendy_ros2_bridge:/ws/src/wendy_ros2_bridge" -w /ws ros:$d \
    bash -lc "source /opt/ros/$d/setup.bash && colcon build" || exit 1
done
```
Expected: both succeed.

- [ ] **Step 3: Manual round-trip test**

Run a bridge that subscribes to `/chatter` from Task 3, then drive a PUBLISH of a captured CDR frame and confirm a native `ros2 topic echo /chatter` shows the message. (Capture one CDR by logging a MESSAGE frame's payload from Task 3's smoke test.)
Expected: the published message appears in `ros2 topic echo`.

- [ ] **Step 4: Commit**

```bash
git add ros2/wendy_ros2_bridge/src/bridge_main.cpp
git commit -m "feat(ros2-bridge): native raw-CDR publish path"
```

---

### Task 5: Embed + stage bridge binaries (Go, arch-scoped)

Embed the built binaries into the agent and stage the distro-matching one to a host dir at startup.

**Files:**
- Create: `go/internal/agent/foxglovebridge/embed_arm64.go`
- Create: `go/internal/agent/foxglovebridge/embed_amd64.go`
- Create: `go/internal/agent/foxglovebridge/embed_other.go`
- Create: `go/internal/agent/foxglovebridge/stage.go`
- Create: `go/internal/agent/foxglovebridge/stage_test.go`
- Create (build output, git-ignored placeholder for now): `go/internal/agent/foxglovebridge/bin/arm64/humble/wendy-ros2-bridge`, `.../arm64/jazzy/...`, `.../amd64/humble/...`, `.../amd64/jazzy/...` (populated by CI in Task 9; commit tiny placeholder files so `go:embed` compiles).

**Interfaces:**
- Produces:
  - `var binaries map[string][]byte` keyed by distro (per arch, set in the arch file).
  - `const StageRoot = "/var/wendy/ros2-bridge"`
  - `func BinaryHostPath(distro string) string` → `"/var/wendy/ros2-bridge/<distro>/wendy-ros2-bridge"`
  - `func Stage(root string) error` — writes every embedded binary to `<root>/<distro>/wendy-ros2-bridge` (0755) idempotently.
  - `func Available(distro string) bool`

- [ ] **Step 1: Write `embed_arm64.go`**

```go
//go:build arm64

package foxglovebridge

import _ "embed"

//go:embed bin/arm64/humble/wendy-ros2-bridge
var binHumble []byte

//go:embed bin/arm64/jazzy/wendy-ros2-bridge
var binJazzy []byte

func init() { binaries = map[string][]byte{"humble": binHumble, "jazzy": binJazzy} }
```

- [ ] **Step 2: Write `embed_amd64.go`** (identical but `//go:build amd64` and `bin/amd64/...`)

```go
//go:build amd64

package foxglovebridge

import _ "embed"

//go:embed bin/amd64/humble/wendy-ros2-bridge
var binHumble []byte

//go:embed bin/amd64/jazzy/wendy-ros2-bridge
var binJazzy []byte

func init() { binaries = map[string][]byte{"humble": binHumble, "jazzy": binJazzy} }
```

- [ ] **Step 3: Write `embed_other.go`** (so non-arm64/amd64 builds still compile with no binaries)

```go
//go:build !arm64 && !amd64

package foxglovebridge

func init() { binaries = map[string][]byte{} }
```

- [ ] **Step 4: Write `stage.go`**

```go
package foxglovebridge

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// binaries is populated by the arch-specific embed file's init().
var binaries map[string][]byte

const StageRoot = "/var/wendy/ros2-bridge"

func BinaryHostPath(distro string) string {
	return filepath.Join(StageRoot, distro, "wendy-ros2-bridge")
}

// Available reports whether an embedded bridge binary exists for distro.
func Available(distro string) bool {
	b, ok := binaries[distro]
	return ok && len(b) > 0
}

// Stage writes each embedded bridge binary under root/<distro>/wendy-ros2-bridge.
// It is idempotent: a file whose contents already match is left untouched.
func Stage(root string) error {
	for distro, data := range binaries {
		if len(data) == 0 {
			continue
		}
		dir := filepath.Join(root, distro)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		path := filepath.Join(dir, "wendy-ros2-bridge")
		if existing, err := os.ReadFile(path); err == nil && sha256.Sum256(existing) == sha256.Sum256(data) {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("rename %s: %w", path, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Write `stage_test.go`**

```go
package foxglovebridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageWritesEmbedded(t *testing.T) {
	// Inject a fake binary set so the test is arch-independent.
	saved := binaries
	binaries = map[string][]byte{"humble": []byte("ELFish")}
	defer func() { binaries = saved }()

	root := t.TempDir()
	if err := Stage(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "humble", "wendy-ros2-bridge"))
	if err != nil || string(got) != "ELFish" {
		t.Fatalf("staged = %q err=%v", got, err)
	}
	// Idempotent second run.
	if err := Stage(root); err != nil {
		t.Fatalf("second stage: %v", err)
	}
}
```

- [ ] **Step 6: Create placeholder embedded files so `go:embed` compiles**

```bash
mkdir -p go/internal/agent/foxglovebridge/bin/{arm64,amd64}/{humble,jazzy}
for a in arm64 amd64; do for d in humble jazzy; do
  printf 'placeholder-%s-%s' "$a" "$d" > go/internal/agent/foxglovebridge/bin/$a/$d/wendy-ros2-bridge
done; done
```

- [ ] **Step 7: Run tests**

Run: `cd go && go test ./internal/agent/foxglovebridge/ -v`
Expected: PASS (protocol + stage tests).

- [ ] **Step 8: Commit**

```bash
git add go/internal/agent/foxglovebridge/
git commit -m "feat(ros2): embed + stage per-distro bridge binaries (arch-scoped)"
```

---

### Task 6: containerd — mount bridge dir at sidecar creation + stdin-capable exec

Give sidecars the bind-mount they need and add an exec variant that wires stdin.

**Files:**
- Modify: `go/internal/agent/containerd/ros2.go` (sidecar spec mounts ~line 352; add `ExecROS2Stream`; extend binary allowlist)
- Modify: `go/internal/agent/services/interfaces.go` (add `ExecROS2Stream` to `ROS2Runtime`; add `BridgeBinaryPath` field to `ROS2ExecOptions`)
- Modify: `go/cmd/wendy-agent/main.go` (call `foxglovebridge.Stage` at startup)
- Test: `go/internal/agent/containerd/ros2_test.go` (allowlist + mount assertions where an existing harness exists; otherwise a focused unit test on `ros2ExecBinary`)

**Interfaces:**
- Consumes: `foxglovebridge.StageRoot`, `foxglovebridge.BinaryHostPath` (Task 5).
- Produces:
  - `ROS2ExecOptions.BridgeBinary bool` — when true, the exec runs the bind-mounted bridge at the in-sidecar path instead of the ros2/python3 allowlist.
  - `ExecROS2Stream(ctx, opts, stdin io.Reader, stdout, stderr io.Writer) (int, error)` on `ROS2Runtime` and `*Client`.

- [ ] **Step 1: Bind-mount the staged bridge dir into every sidecar**

In `ensureROS2Sidecar` (the function creating the sidecar spec, ~line 352 where `ROS2BagDir` is appended), add after the bag mount:

```go
	// Bind-mount the host-staged bridge binaries read-only so ExecROS2Stream can
	// run the distro-matching wendy-ros2-bridge inside the sidecar (the data-plane
	// fast path). Read-only + nosuid/nodev: the sidecar only executes it.
	if _, err := os.Stat(foxglovebridge.StageRoot); err == nil {
		spec.Mounts = append(spec.Mounts, localoci.Mount{
			Destination: foxglovebridge.StageRoot,
			Type:        "bind",
			Source:      foxglovebridge.StageRoot,
			Options:     []string{"rbind", "ro", "nosuid", "nodev"},
		})
	}
```

Add the import `"github.com/wendylabsinc/wendy/go/internal/agent/foxglovebridge"`.

- [ ] **Step 2: Extend the binary resolution to allow the bridge path**

Replace `ros2ExecBinary` and its single call site. Add a helper that, given opts, returns the command to exec:

```go
// ros2ExecCommand returns the in-sidecar executable path for opts. The bridge
// runs from its bind-mounted host path (validated to live under StageRoot); all
// other execs use the fixed ros2/python3 allowlist.
func ros2ExecCommand(opts services.ROS2ExecOptions, distro string) string {
	if opts.BridgeBinary {
		return foxglovebridge.BinaryHostPath(distro)
	}
	return ros2ExecBinary(opts.Binary)
}
```

- [ ] **Step 3: Add `ExecROS2Stream` (refactor `ExecROS2` to delegate)**

Extract the body of `ExecROS2` into a private `execROS2(ctx, opts, stdin io.Reader, stdout, stderr io.Writer)` that uses `cio.NewCreator(cio.WithStreams(stdin, stdout, stderr))` and `ros2ExecCommand(opts, distro)`. Then:

```go
func (c *Client) ExecROS2(ctx context.Context, opts services.ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
	return c.execROS2(ctx, opts, nil, stdout, stderr)
}

func (c *Client) ExecROS2Stream(ctx context.Context, opts services.ROS2ExecOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	return c.execROS2(ctx, opts, stdin, stdout, stderr)
}
```

In `execROS2`, change the command construction from `bin := ros2ExecBinary(opts.Binary)` to `bin := ros2ExecCommand(opts, distro)` (keep the `"$@"` shell-safety wrapper unchanged; the bridge takes no args so `opts.Args` stays empty for it).

- [ ] **Step 4: Add both to the `ROS2Runtime` interface and `ROS2ExecOptions`**

In `interfaces.go`, add to `ROS2ExecOptions`:

```go
	// BridgeBinary runs the bind-mounted wendy-ros2-bridge (data-plane fast path)
	// instead of the ros2/python3 allowlist. Args must be empty.
	BridgeBinary bool
```

and to the `ROS2Runtime` interface:

```go
	// ExecROS2Stream is ExecROS2 with a stdin stream, used to drive the
	// long-lived wendy-ros2-bridge control protocol.
	ExecROS2Stream(ctx context.Context, opts ROS2ExecOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error)
```

- [ ] **Step 5: Stage binaries at agent startup**

In `go/cmd/wendy-agent/main.go`, near other startup staging, add:

```go
	if err := foxglovebridge.Stage(foxglovebridge.StageRoot); err != nil {
		logger.Warn("staging ROS 2 bridge binaries failed; foxglove falls back to rclpy", zap.Error(err))
	}
```

(import `foxglovebridge` and `zap` if not present.)

- [ ] **Step 6: Unit test the command resolution**

```go
func TestROS2ExecCommandBridge(t *testing.T) {
	got := ros2ExecCommand(services.ROS2ExecOptions{BridgeBinary: true}, "jazzy")
	want := "/var/wendy/ros2-bridge/jazzy/wendy-ros2-bridge"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if ros2ExecCommand(services.ROS2ExecOptions{Binary: "python3"}, "jazzy") != "python3" {
		t.Fatal("python3 allowlist regressed")
	}
}
```

- [ ] **Step 7: Build + test**

Run: `cd go && go build ./... && go test ./internal/agent/containerd/ -run ROS2ExecCommand -v`
Expected: build clean; test PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/agent/containerd/ros2.go go/internal/agent/services/interfaces.go go/cmd/wendy-agent/main.go go/internal/agent/containerd/ros2_test.go
git commit -m "feat(ros2): mount staged bridge into sidecars + stdin-capable ExecROS2Stream"
```

---

### Task 7: Go bridge manager (lifecycle + multiplexing)

The manager owns one bridge process per sidecar, assigns sub/req IDs, fans MESSAGE frames out to per-subscription channels, and reports readiness for fallback decisions.

**Files:**
- Create: `go/internal/agent/services/ros2_bridge.go`
- Test: `go/internal/agent/services/ros2_bridge_test.go`

**Interfaces:**
- Consumes: `foxglovebridge` codec (Task 1), `ROS2Runtime.ExecROS2Stream` (Task 6), `ros2SC` (existing).
- Produces:
  - `type ros2Bridge struct { … }` with `newROS2Bridge(rt ROS2Runtime) *ros2Bridge`
  - `func (b *ros2Bridge) Subscribe(ctx, sc ros2SC, topic, msgType string) (<-chan foxglovebridge.Message, func(), error)` — returns a per-subscription channel and a cancel func; `error` non-nil (e.g. bridge unavailable) tells the caller to fall back.
  - `func (b *ros2Bridge) Publish(ctx, sc ros2SC, topic, msgType string, cdr []byte) error`
  - `func (b *ros2Bridge) available(sc ros2SC) bool`

- [ ] **Step 1: Write the failing test (drives a fake bridge via a scripted ExecROS2Stream)**

```go
package services

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/foxglovebridge"
)

// fakeRuntime implements just enough of ROS2Runtime: ExecROS2Stream runs an
// in-process "bridge" that echoes READY then, on SUBSCRIBE, emits one MESSAGE.
type fakeBridgeRuntime struct{ ROS2Runtime }

func (f *fakeBridgeRuntime) ExecROS2Stream(ctx context.Context, opts ROS2ExecOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	// READY
	var ready []byte
	ready = foxglovebridge.AppendString(ready, "jazzy")
	ready = append(ready, 0)
	writeFrame(stdout, foxglovebridge.KindReady, ready)

	buf := make([]byte, 0, 64)
	for {
		fr, nb, err := foxglovebridge.ReadFrame(stdin, buf)
		buf = nb
		if err != nil {
			return 0, nil
		}
		if fr.Tag == foxglovebridge.OpSubscribe {
			// Reply with one MESSAGE for subID parsed from the body.
			subID := readU32(fr.Body)
			var m []byte
			m = appendU32(m, subID)
			m = appendU64(m, 42)
			m = append(m, 0xAB)
			writeFrame(stdout, foxglovebridge.KindMessage, m)
		}
	}
}

func TestBridgeSubscribeDeliversMessage(t *testing.T) {
	b := newROS2Bridge(&fakeBridgeRuntime{})
	sc := ros2SC{name: "sc", rmw: "rmw_cyclonedds_cpp", domainID: 0}
	ch, cancel, err := b.Subscribe(context.Background(), sc, "/t", "std_msgs/msg/String")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case m := <-ch:
		if m.TimestampNs != 42 || len(m.CDR) != 1 || m.CDR[0] != 0xAB {
			t.Fatalf("msg = %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
}
```

(`writeFrame`, `readU32`, `appendU32`, `appendU64` are small test helpers — add them to the test file, mirroring the codec's little-endian layout.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestBridgeSubscribeDeliversMessage -v`
Expected: FAIL — `newROS2Bridge` undefined.

- [ ] **Step 3: Implement `ros2_bridge.go`**

```go
package services

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/agent/foxglovebridge"
)

// ros2Bridge owns one long-lived wendy-ros2-bridge process per sidecar and
// multiplexes subscriptions/publishes over its stdin/stdout control protocol.
type ros2Bridge struct {
	rt   ROS2Runtime
	mu   sync.Mutex
	proc map[string]*bridgeProc // keyed by sidecar name
}

func newROS2Bridge(rt ROS2Runtime) *ros2Bridge {
	return &ros2Bridge{rt: rt, proc: map[string]*bridgeProc{}}
}

type bridgeProc struct {
	stdin   *io.PipeWriter
	mu      sync.Mutex // serializes stdin writes + subs map
	nextID  uint32
	subs    map[uint32]chan foxglovebridge.Message
	ready   chan struct{}
	distro  string
	dead    chan struct{}
}

// ensure starts the bridge for sc if not already running. Returns an error the
// caller treats as "fall back to the legacy path".
func (b *ros2Bridge) ensure(ctx context.Context, sc ros2SC) (*bridgeProc, error) {
	b.mu.Lock()
	if p, ok := b.proc[sc.name]; ok {
		b.mu.Unlock()
		return p, nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bridgeProc{
		stdin: inW,
		subs:  map[uint32]chan foxglovebridge.Message{},
		ready: make(chan struct{}),
		dead:  make(chan struct{}),
	}
	b.proc[sc.name] = p
	b.mu.Unlock()

	go func() {
		_, _ = b.rt.ExecROS2Stream(context.WithoutCancel(ctx), ROS2ExecOptions{
			DomainID:     sc.domainID,
			SidecarName:  sc.name,
			BridgeBinary: true,
		}, inR, outW, io.Discard)
		outW.Close()
		close(p.dead)
		b.mu.Lock()
		delete(b.proc, sc.name)
		b.mu.Unlock()
	}()

	go p.readLoop(outR)
	return p, nil
}

func (p *bridgeProc) readLoop(r io.Reader) {
	var buf []byte
	for {
		fr, nb, err := foxglovebridge.ReadFrame(r, buf)
		buf = nb
		if err != nil {
			p.failAll()
			return
		}
		switch fr.Tag {
		case foxglovebridge.KindReady:
			distro, _, _ := foxglovebridge.ParseReady(fr.Body)
			p.mu.Lock()
			p.distro = distro
			select {
			case <-p.ready:
			default:
				close(p.ready)
			}
			p.mu.Unlock()
		case foxglovebridge.KindMessage:
			m, err := foxglovebridge.ParseMessage(fr.Body)
			if err != nil {
				continue
			}
			cp := make([]byte, len(m.CDR)) // copy: buf is reused next iteration
			copy(cp, m.CDR)
			m.CDR = cp
			p.mu.Lock()
			ch := p.subs[m.SubID]
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default: // slow consumer: drop (freshest-sample-wins, matches CLI side)
				}
			}
		case foxglovebridge.KindSubError:
			subID, _, _ := foxglovebridge.ParseSubError(fr.Body)
			p.mu.Lock()
			if ch := p.subs[subID]; ch != nil {
				close(ch)
				delete(p.subs, subID)
			}
			p.mu.Unlock()
		}
	}
}

func (p *bridgeProc) failAll() {
	p.mu.Lock()
	for id, ch := range p.subs {
		close(ch)
		delete(p.subs, id)
	}
	p.mu.Unlock()
}

func (b *ros2Bridge) Subscribe(ctx context.Context, sc ros2SC, topic, msgType string) (<-chan foxglovebridge.Message, func(), error) {
	p, err := b.ensure(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan foxglovebridge.Message, 8)
	p.subs[id] = ch
	cmd := foxglovebridge.AppendSubscribe(nil, id, topic, msgType, foxglovebridge.QoSAuto)
	_, werr := p.stdin.Write(cmd)
	p.mu.Unlock()
	if werr != nil {
		return nil, nil, fmt.Errorf("bridge subscribe write: %w", werr)
	}
	cancel := func() {
		p.mu.Lock()
		if _, ok := p.subs[id]; ok {
			delete(p.subs, id)
			_, _ = p.stdin.Write(foxglovebridge.AppendUnsubscribe(nil, id))
		}
		p.mu.Unlock()
	}
	return ch, cancel, nil
}

func (b *ros2Bridge) Publish(ctx context.Context, sc ros2SC, topic, msgType string, cdr []byte) error {
	p, err := b.ensure(ctx, sc)
	if err != nil {
		return err
	}
	p.mu.Lock()
	_, werr := p.stdin.Write(foxglovebridge.AppendPublish(nil, topic, msgType, cdr))
	p.mu.Unlock()
	return werr
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/services/ -run TestBridgeSubscribeDeliversMessage -v`
Expected: PASS.

- [ ] **Step 5: Add a fallback-path test (bridge write failure → error)**

```go
func TestBridgeUnavailableReturnsError(t *testing.T) {
	// A runtime whose ExecROS2Stream exits immediately closes the pipe; the first
	// Subscribe write then fails, signaling the caller to fall back.
	b := newROS2Bridge(&deadRuntime{})
	_, _, err := b.Subscribe(context.Background(), ros2SC{name: "x"}, "/t", "T")
	if err == nil {
		t.Fatal("want fallback error when bridge is dead")
	}
}
```

Add `deadRuntime` whose `ExecROS2Stream` returns `(1, nil)` immediately.

- [ ] **Step 6: Run + commit**

Run: `cd go && go test ./internal/agent/services/ -run TestBridge -v`
Expected: PASS.

```bash
git add go/internal/agent/services/ros2_bridge.go go/internal/agent/services/ros2_bridge_test.go
git commit -m "feat(ros2): bridge manager — lifecycle, multiplexing, fallback signaling"
```

---

### Task 8: Route SubscribeRaw + Publish through the bridge (with fallback)

Wire the RPC handlers to prefer the bridge and fall back to today's paths.

**Files:**
- Modify: `Proto/wendy/agent/services/v2/ros2_service.proto` (add `bytes cdr = 5;` to `PublishROS2Request`)
- Modify: `go/internal/agent/services/ros2_service.go` (`ROS2Service` struct + `NewROS2Service` ~46/57; `SubscribeRaw` ~727; `Publish` ~375)
- Modify: `go/internal/cli/commands/foxglove_write.go` (`handleClientPublish` ~131 — send CDR, drop the decode)
- Test: `go/internal/agent/services/ros2_service_test.go`

**Interfaces:**
- Consumes: `ros2Bridge` (Task 7).
- Produces: `PublishROS2Request.cdr` (raw client CDR); RPC signatures otherwise unchanged; behavior now bridge-first with YAML fallback.

Confirmed current shapes (verified against the tree): `ROS2Service{logger, runtime, bagDir}`, constructor `NewROS2Service(logger *zap.Logger, runtime ROS2Runtime, bagDir string)`. `Publish` today runs `s.runIn(ctx, sc, "topic","pub","--once", topic, type, req.GetYaml())`. The CLI's `handleClientPublish` decodes the client CDR to YAML via `foxglovecdr` and sends `Yaml`.

- [ ] **Step 1: Add the bridge to `ROS2Service`**

Add the field to the struct:

```go
	bridge *ros2Bridge
```

and in `NewROS2Service`, set it in the returned literal: `bridge: newROS2Bridge(runtime),` (reuse the `runtime` arg).

- [ ] **Step 2: Make `SubscribeRaw` try the bridge first**

At the top of `SubscribeRaw`, after `sc := s.pickSidecarForTopic(...)`, insert:

```go
	// Fast path: the compiled bridge streams native CDR without a per-topic
	// python process. On any bridge failure fall through to the rclpy forwarder.
	msgType, terr := s.resolveTopicType(ctx, sc, req.GetTopic())
	if terr == nil {
		if ch, cancel, berr := s.bridge.Subscribe(ctx, sc, req.GetTopic(), msgType); berr == nil {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return nil
				case m, ok := <-ch:
					if !ok {
						// bridge subscription ended (SUB_ERROR/dead): fall back below.
						goto fallback
					}
					if serr := stream.Send(&agentpbv2.RawROS2Message{Cdr: m.CDR, TimestampNs: m.TimestampNs}); serr != nil {
						return serr
					}
				}
			}
		}
	}
fallback:
```

(Keep the entire existing rclpy-forwarder body below the `fallback:` label unchanged.)

Add a small helper `resolveTopicType` that runs `ros2 topic type <topic>` in `sc` (reuse `s.runIn`), returning the trimmed type or an error — this is one cheap call at subscription start, not per message.

- [ ] **Step 3: Add the `cdr` field to the proto and regenerate**

In `Proto/wendy/agent/services/v2/ros2_service.proto`, extend `PublishROS2Request`:

```proto
message PublishROS2Request {
    optional int32 domain_id = 1;
    string topic = 2;
    string type = 3;  // e.g. "geometry_msgs/msg/Twist"
    string yaml = 4;  // message body as YAML/JSON (fallback path)
    bytes cdr = 5;    // raw serialized CDR (native path); preferred when set
}
```

Regenerate: `cd go && bash scripts/generate-proto.sh` (protoc + plugins are on `$GOPATH/bin`, added to PATH by the script). Then normalize the version header if it differs from the tree's (`v7.35.1`), matching how the merge was resolved.

- [ ] **Step 4: Make `Publish` prefer CDR → bridge, fall back to YAML**

Replace the body of `Publish` after `sc := s.pickSidecarForTopic(...)`:

```go
	if cdr := req.GetCdr(); len(cdr) > 0 {
		if err := s.bridge.Publish(ctx, sc, req.GetTopic(), req.GetType(), cdr); err == nil {
			return &agentpbv2.PublishROS2Response{Success: true}, nil
		}
		// bridge unavailable/failed: fall through to the YAML ros2-CLI path only if
		// we also have YAML; otherwise report the failure.
		if req.GetYaml() == "" {
			return &agentpbv2.PublishROS2Response{Success: false, Message: "native publish failed and no YAML fallback provided"}, nil
		}
	}
	out, err := s.runIn(ctx, sc, "topic", "pub", "--once", req.GetTopic(), req.GetType(), req.GetYaml())
	if err != nil {
		return &agentpbv2.PublishROS2Response{Success: false, Message: err.Error()}, nil
	}
	return &agentpbv2.PublishROS2Response{Success: true, Message: strings.TrimSpace(out)}, nil
```

- [ ] **Step 5: CLI — send raw CDR instead of decoding to YAML**

In `handleClientPublish` (`foxglove_write.go`), replace the `foxglovecdr.Decode`/`ToYAML` block and the `Publish` call with a direct CDR send (the client `payload` is CDR with the encapsulation header, exactly what `GenericPublisher` expects):

```go
	resp, err := s.src.Publish(ctx, &agentpbv2.PublishROS2Request{
		DomainId: s.domainID,
		Topic:    ch.Topic,
		Type:     fgSchemaNameToType(ch.SchemaName),
		Cdr:      payload,
	})
```

Remove the now-unused `foxglovecdr.ParseSchema`/`Decode`/`ToYAML` lines in this function only (they remain used by the service-call path — do not delete the imports if still referenced there).

- [ ] **Step 6: Add a handler test with a fake runtime**

Assert that when the injected `ros2Bridge` (fake runtime delivering one MESSAGE) is present, `SubscribeRaw` emits a `RawROS2Message` with the bridge's bytes; and when the bridge write fails, the handler still reaches the rclpy path (assert no panic + fallback error surfaces as today). Add a `Publish` test: `req.Cdr` set → fake bridge records the CDR and `Success:true`; `req.Cdr` empty → the YAML path runs.

- [ ] **Step 7: Build + test**

Run: `cd go && go build ./... && go test ./internal/agent/services/ -run 'ROS2|Bridge' -v`
Expected: build clean; PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/agent/services/ros2_service.go go/internal/agent/services/ros2_service_test.go
git commit -m "feat(ros2): route SubscribeRaw+Publish through the native bridge with fallback"
```

---

### Task 9: CI — build matrix embeds the bridge binaries

Build `wendy_ros2_bridge` for `{humble,jazzy}×{arm64,amd64}` and drop the binaries into the embed tree before the agent is built/released.

**Files:**
- Modify: `.github/workflows/<agent build/release workflow>.yml` (add a `ros2-bridge` job feeding the agent job)
- Create: `scripts/build-ros2-bridge.sh`

- [ ] **Step 1: Write `scripts/build-ros2-bridge.sh`**

```bash
#!/usr/bin/env bash
# Builds wendy-ros2-bridge for one distro+arch and copies it into the agent's
# go:embed tree. Requires docker with qemu binfmt for cross-arch.
set -euo pipefail
distro="$1"; arch="$2"   # arch: arm64 | amd64
platform="linux/${arch}"
out="go/internal/agent/foxglovebridge/bin/${arch}/${distro}/wendy-ros2-bridge"
mkdir -p "$(dirname "$out")"
cid=$(docker create --platform "$platform" -v "$PWD/ros2/wendy_ros2_bridge:/ws/src/wendy_ros2_bridge" -w /ws "ros:${distro}" \
  bash -lc "source /opt/ros/${distro}/setup.bash && colcon build --cmake-args -DCMAKE_BUILD_TYPE=Release")
docker start -a "$cid"
docker cp "$cid:/ws/install/wendy_ros2_bridge/lib/wendy_ros2_bridge/wendy-ros2-bridge" "$out"
docker rm "$cid" >/dev/null
echo "built $out"
```

- [ ] **Step 2: Add the CI job**

In the agent build/release workflow, add a job that runs before the agent build (using the large runner per repo policy):

```yaml
  ros2-bridge:
    runs-on: [self-hosted, large]  # match repo policy: always the larger runner
    strategy:
      matrix:
        distro: [humble, jazzy]
        arch: [arm64, amd64]
    steps:
      - uses: actions/checkout@v4
      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3
      - name: Build bridge
        run: bash scripts/build-ros2-bridge.sh ${{ matrix.distro }} ${{ matrix.arch }}
      - uses: actions/upload-artifact@v4
        with:
          name: bridge-${{ matrix.arch }}-${{ matrix.distro }}
          path: go/internal/agent/foxglovebridge/bin/${{ matrix.arch }}/${{ matrix.distro }}/wendy-ros2-bridge
```

Then in the agent build job, add `needs: ros2-bridge` and download all `bridge-*` artifacts into `go/internal/agent/foxglovebridge/bin/` before `go build`.

- [ ] **Step 3: Verify locally (arm64 host or with qemu)**

Run: `bash scripts/build-ros2-bridge.sh humble arm64 && file go/internal/agent/foxglovebridge/bin/arm64/humble/wendy-ros2-bridge`
Expected: `ELF 64-bit ... ARM aarch64`.

- [ ] **Step 4: Commit**

```bash
git add scripts/build-ros2-bridge.sh .github/workflows/
git commit -m "ci(ros2-bridge): build humble/jazzy × arm64/amd64 and embed in agent"
```

---

### Task 10: On-device acceptance (hardware gate)

**Files:** none (verification only). Record results in the PR.

- [ ] **Step 1:** Flash/boot a device with the new agent; deploy a ROS 2 app (e.g. a camera or `demo_nodes_cpp` talker) on both a Humble and a Jazzy app image if available.
- [ ] **Step 2:** `wendy device foxglove serve`, connect Foxglove Studio, confirm topics advertise and a Raw Message + structured panel render live via the **bridge** (check agent logs show the bridge process, not the rclpy forwarder).
- [ ] **Step 3:** Subscribe a large topic (image/pointcloud); measure sustained rate + agent CPU vs. the rclpy forwarder (temporarily force fallback). Record the delta — this is the justification for the compiled path.
- [ ] **Step 4:** Confirm QoS auto-match delivers a reliable/transient-local topic (e.g. `/tf_static`) that the old best-effort forwarder never showed.
- [ ] **Step 5:** Publish to a topic from Foxglove (`--allow-control`) and confirm it arrives via `ros2 topic echo` on-device.
- [ ] **Step 6:** Remove/rename the staged binary and confirm subscribe **falls back** to the rclpy path with no client-visible failure.
- [ ] **Step 7:** Update the PR description (the current one still describes the P1 `ros2 topic echo --raw` path) to reflect the native bridge + fallback.

---

### Task 11 (deferred, optional): Native service calls on Jazzy

Out of v1 scope — services are low-rate request/response, not a throughput concern, and `create_generic_client` is Jazzy-only. When picked up: add op 4 / kind 2 to the C++ node behind the READY caps bit, add `CallService` routing with the Humble text fallback, and gate on `caps&1`. The protocol already reserves the op/kind numbers.

---

## Self-review

- **Spec coverage:** subscribe native (T3,T8), publish native (T4,T8), consolidation/one-process-per-graph (T7), QoS auto-match (T3), embed+delivery (T5,T6,T9), fallback everywhere (T7,T8,T10), Humble+Jazzy matrix (T9), discovery/schema/params unchanged (untouched), services deferred with reserved protocol (T11) — matches the approved spec's explicit deferral sequencing.
- **Placeholders:** none — every code step carries full content; the one conditional (`publishCDR` shape in T8 Step 3) is spelled out with both branches and a fallback note tied to T10 verification.
- **Type consistency:** `foxglovebridge` names (`AppendSubscribe`, `ReadFrame`, `ParseMessage`, `Message{SubID,TimestampNs,CDR}`) are used identically across T1/T7; `ROS2ExecOptions.BridgeBinary` and `ExecROS2Stream` defined in T6 and consumed in T7; `ros2Bridge.Subscribe/Publish` defined in T7 and consumed in T8.
