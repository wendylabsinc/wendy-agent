// wendy-ros2-bridge: reads the Wendy bridge control protocol on stdin and writes
// framed events on stdout. Subscribe path uses rclcpp generic (raw CDR)
// subscriptions so DDS's serialized bytes flow straight through to Foxglove.
#include <atomic>
#include <chrono>
#include <cstdlib>
#include <cstring>
#include <map>
#include <mutex>
#include <thread>

#include <rclcpp/rclcpp.hpp>
#include <rclcpp/generic_publisher.hpp>
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
// best-effort/KEEP_LAST(1) when none are visible yet or when forced. When
// publishers are visible, a reliable subscription is only chosen if every
// publisher is reliable (a reliable subscriber can't match a best-effort
// publisher, but a best-effort subscriber matches both); transient_local is
// adopted if any publisher uses it.
rclcpp::QoS choose_qos(rclcpp::Node& node, const std::string& topic, uint8_t qos_flag) {
  if (qos_flag == QOS_FORCE_BEST_EFFORT) {
    return rclcpp::QoS(rclcpp::KeepLast(1)).best_effort();
  }
  auto infos = node.get_publishers_info_by_topic(topic);
  if (infos.empty()) {
    return rclcpp::QoS(rclcpp::KeepLast(1)).best_effort();
  }
  bool all_reliable = true, any_transient_local = false;
  for (const auto& info : infos) {
    const auto& q = info.qos_profile();
    if (q.reliability() != rclcpp::ReliabilityPolicy::Reliable) all_reliable = false;
    if (q.durability() == rclcpp::DurabilityPolicy::TransientLocal) any_transient_local = true;
  }
  rclcpp::QoS q(rclcpp::KeepLast(10));
  if (all_reliable) q.reliable(); else q.best_effort();
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

  std::map<std::string, rclcpp::GenericPublisher::SharedPtr> pubs;
  std::mutex pubs_mu;

  // Reader thread: parse stdin commands and mutate the subscription table.
  std::atomic<bool> stop{false};
  std::thread reader([&] {
    FrameReader fr(stdin);
    uint8_t tag;
    std::vector<uint8_t> body;
    try {
      while (!stop && fr.next(tag, body)) {
        if (tag == OP_SUBSCRIBE) {
          // FrameReader guarantees body.size() == the frame's declared length,
          // but not that it's long enough for the fields below (a malformed or
          // truncated frame must never be read out of bounds). Validate
          // incrementally and skip the frame -- without throwing -- on any
          // shortfall, so a bad frame can't propagate to the outer catch and
          // tear down the whole bridge.
          if (body.size() < 4) {
            // Can't even recover a sub_id to report against; drop the frame.
            continue;
          }
          const uint8_t* p = body.data();
          size_t remaining = body.size();
          uint32_t sub_id = read_u32(p); p += 4; remaining -= 4;
          bool ok = remaining >= 2;
          uint16_t tn = 0, yn = 0;
          std::string topic, type;
          uint8_t qos_flag = 0;
          if (ok) {
            tn = read_u16(p); p += 2; remaining -= 2;
            ok = remaining >= tn;
          }
          if (ok) {
            topic.assign((const char*)p, tn); p += tn; remaining -= tn;
            ok = remaining >= 2;
          }
          if (ok) {
            yn = read_u16(p); p += 2; remaining -= 2;
            ok = remaining >= yn;
          }
          if (ok) {
            type.assign((const char*)p, yn); p += yn; remaining -= yn;
            ok = remaining >= 1;
          }
          if (ok) {
            qos_flag = *p;
          }
          if (!ok) {
            emit_sub_error(sub_id, "malformed SUBSCRIBE frame");
            continue;
          }
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
          if (body.size() < 4) {
            continue;
          }
          uint32_t sub_id = read_u32(body.data());
          std::lock_guard<std::mutex> lk(subs_mu);
          subs.erase(sub_id);
        } else if (tag == OP_PUBLISH) {
          // Same incremental-bounds-check idiom as OP_SUBSCRIBE above: validate
          // `remaining` bytes before every field read, and skip (continue) a
          // malformed/truncated frame rather than reading past `body` or
          // throwing into the outer catch.
          const uint8_t* p = body.data();
          size_t remaining = body.size();
          bool ok = remaining >= 2;
          uint16_t tn = 0, yn = 0;
          std::string topic, type;
          if (ok) {
            tn = read_u16(p); p += 2; remaining -= 2;
            ok = remaining >= tn;
          }
          if (ok) {
            topic.assign((const char*)p, tn); p += tn; remaining -= tn;
            ok = remaining >= 2;
          }
          if (ok) {
            yn = read_u16(p); p += 2; remaining -= 2;
            ok = remaining >= yn;
          }
          if (ok) {
            type.assign((const char*)p, yn); p += yn; remaining -= yn;
          }
          if (!ok) {
            // No sub_id to report against for PUBLISH; just drop the frame.
            continue;
          }
          // Whatever is left of the body is the raw CDR payload, verbatim.
          size_t cdr_len = remaining;
          const uint8_t* cdr = p;

          std::string key = type + "\n" + topic;
          rclcpp::GenericPublisher::SharedPtr pub;
          {
            std::lock_guard<std::mutex> lk(pubs_mu);
            auto it = pubs.find(key);
            if (it == pubs.end()) {
              try {
                pub = node->create_generic_publisher(topic, type, rclcpp::QoS(rclcpp::KeepLast(10)));
              } catch (const std::exception&) {
                // Can't create the publisher (e.g. bad type name); drop the frame.
                continue;
              }
              pubs[key] = pub;
            } else {
              pub = it->second;
            }
          }
          try {
            rclcpp::SerializedMessage sm(cdr_len);
            auto& rcl = sm.get_rcl_serialized_message();
            if (cdr_len > 0) {
              std::memcpy(rcl.buffer, cdr, cdr_len);
            }
            rcl.buffer_length = cdr_len;
            pub->publish(sm);
          } catch (const std::exception&) {
            // Runtime publish failure (e.g. DDS writer error, shutdown race);
            // drop this frame instead of tearing down the whole bridge.
            continue;
          }
        }
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

  // exec.spin() can return either because the reader thread saw stdin EOF and
  // called rclcpp::shutdown() itself (in which case it has already finished,
  // or is about to), or because a signal (e.g. SIGINT via rclcpp's own
  // handler) triggered rclcpp::shutdown() out-of-band. In the latter case the
  // reader thread is still blocked in a plain fread(stdin) with no portable,
  // race-free way to interrupt it, so reader.join() here could hang forever.
  //
  // Instead: detach the reader and terminate the process immediately with
  // std::_Exit. _Exit skips destructors for both automatic-storage objects
  // (node, subs, framer, ...) and static-storage objects, and it reclaims the
  // whole process image -- including any thread still parked in fread --
  // atomically from the OS's point of view. That means there is no window in
  // which the detached thread could wake up and touch `node`/`subs` while (or
  // after) they are being destroyed: the process is simply gone before that
  // could happen. Genuine stdin EOF/closed-pipe still drives the normal
  // shutdown path above (reader sets stop + calls rclcpp::shutdown() itself).
  stop = true;
  reader.detach();
  std::_Exit(0);
}
