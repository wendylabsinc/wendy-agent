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
