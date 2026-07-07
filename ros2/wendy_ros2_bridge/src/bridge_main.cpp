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
