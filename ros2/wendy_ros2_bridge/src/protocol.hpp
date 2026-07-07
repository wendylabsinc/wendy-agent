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
