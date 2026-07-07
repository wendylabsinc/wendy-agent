#include <gtest/gtest.h>
#include <cstdio>
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

TEST(Protocol, FramerReaderRoundTrip) {
  std::FILE* f = std::tmpfile();
  ASSERT_NE(f, nullptr);

  std::vector<uint8_t> body{1, 2, 3, 4, 5};
  {
    Framer framer(f);
    ASSERT_TRUE(framer.write(KIND_MESSAGE, body));
  }
  std::rewind(f);

  FrameReader reader(f);
  uint8_t tag = 0;
  std::vector<uint8_t> read_body;
  ASSERT_TRUE(reader.next(tag, read_body));
  EXPECT_EQ(tag, uint8_t(KIND_MESSAGE));
  EXPECT_EQ(read_body, body);

  // Clean EOF on the second read.
  uint8_t tag2 = 0;
  std::vector<uint8_t> body2;
  EXPECT_FALSE(reader.next(tag2, body2));

  std::fclose(f);
}

TEST(Protocol, ReaderRejectsZeroLength) {
  const uint8_t buf[4] = {0, 0, 0, 0};  // length prefix = 0
  std::FILE* f = fmemopen(const_cast<uint8_t*>(buf), sizeof(buf), "rb");
  ASSERT_NE(f, nullptr);

  FrameReader reader(f);
  uint8_t tag = 0;
  std::vector<uint8_t> body;
  EXPECT_THROW(reader.next(tag, body), std::runtime_error);

  std::fclose(f);
}

TEST(Protocol, ReaderRejectsOversize) {
  const uint8_t buf[4] = {0xFF, 0xFF, 0xFF, 0xFF};  // length prefix = 0xFFFFFFFF
  std::FILE* f = fmemopen(const_cast<uint8_t*>(buf), sizeof(buf), "rb");
  ASSERT_NE(f, nullptr);

  FrameReader reader(f);
  uint8_t tag = 0;
  std::vector<uint8_t> body;
  // Must throw before attempting to allocate/read ~4GiB.
  EXPECT_THROW(reader.next(tag, body), std::runtime_error);

  std::fclose(f);
}

TEST(Protocol, ReaderRejectsTruncatedHeader) {
  const uint8_t buf[2] = {0x05, 0x00};  // only 2 of the 4 length bytes present
  std::FILE* f = fmemopen(const_cast<uint8_t*>(buf), sizeof(buf), "rb");
  ASSERT_NE(f, nullptr);

  FrameReader reader(f);
  uint8_t tag = 0;
  std::vector<uint8_t> body;
  EXPECT_THROW(reader.next(tag, body), std::runtime_error);

  std::fclose(f);
}

TEST(Protocol, ReaderRejectsTruncatedBody) {
  // Length prefix says n=10, but only 3 bytes follow.
  const uint8_t buf[7] = {10, 0, 0, 0, 1, 2, 3};
  std::FILE* f = fmemopen(const_cast<uint8_t*>(buf), sizeof(buf), "rb");
  ASSERT_NE(f, nullptr);

  FrameReader reader(f);
  uint8_t tag = 0;
  std::vector<uint8_t> body;
  EXPECT_THROW(reader.next(tag, body), std::runtime_error);

  std::fclose(f);
}
