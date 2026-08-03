#!/usr/bin/env python3
"""Demand-driven, bandwidth-bounded ROS 2 gateway for Wendy Observe."""

import argparse
import asyncio
import dataclasses
import io
import json
import os
import signal
import ssl
import struct
import threading
import time
from typing import Dict, Optional


FRAME_VERSION = 1
DEFAULT_POINT_STRIDE = 4
DEFAULT_MAX_HZ = 10.0
# Eight megabits per second. The gateway applies this once across the session,
# not once per topic, so opening more panels cannot multiply the link budget.
DEFAULT_MAX_BYTES_PER_SECOND = 1_000_000
DEFAULT_JPEG_QUALITY = 65
DEFAULT_MAX_WIDTH = 960
UPSTREAM_IDLE_GRACE_SECONDS = 0.5


class ObserveError(Exception):
    """An error safe to return to an Observe client."""


@dataclasses.dataclass(frozen=True)
class StreamSpec:
    stream_id: str
    topic: str
    type_name: str = ""
    profile: str = "auto"
    max_hz: float = DEFAULT_MAX_HZ
    max_bytes_per_second: int = DEFAULT_MAX_BYTES_PER_SECOND
    point_stride: int = DEFAULT_POINT_STRIDE
    jpeg_quality: int = DEFAULT_JPEG_QUALITY
    max_width: int = DEFAULT_MAX_WIDTH


@dataclasses.dataclass
class ProcessedFrame:
    payload: bytes
    encoding: str
    profile: str
    timestamp_ns: int


@dataclasses.dataclass
class SharedROSSubscription:
    node: object = None
    handle: object = None
    executor: object = None
    executor_thread: object = None
    consumers: set = dataclasses.field(default_factory=set)
    teardown: object = None


def pack_frame(spec: StreamSpec, type_name: str, frame: ProcessedFrame, dropped: int) -> bytes:
    """Encode one transport-independent Observe frame.

    Layout: uint32 body_length, uint32 header_length, JSON header, payload.
    Both integer fields use network byte order. WebSocket sends one packed frame
    per binary message; HTTPS streaming concatenates packed frames.
    """
    header = json.dumps(
        {
            "version": FRAME_VERSION,
            "stream_id": spec.stream_id,
            "topic": spec.topic,
            "type": type_name,
            "encoding": frame.encoding,
            "profile": frame.profile,
            "timestamp_ns": frame.timestamp_ns,
            "dropped": dropped,
        },
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    body_length = 4 + len(header) + len(frame.payload)
    return struct.pack(">II", body_length, len(header)) + header + frame.payload


class ByteBudget:
    """One-second token bucket used to bound each stream independently."""

    def __init__(self, bytes_per_second: int) -> None:
        self.rate = max(1, bytes_per_second)
        self.tokens = float(self.rate)
        self.updated_at = time.monotonic()

    def allow(self, size: int, now: float) -> bool:
        elapsed = max(0.0, now - self.updated_at)
        self.updated_at = now
        self.tokens = min(float(self.rate), self.tokens + elapsed * self.rate)
        if size > self.tokens:
            return False
        self.tokens -= size
        return True


def clamp_spec(raw: dict, limits) -> StreamSpec:
    stream_id = str(raw.get("id", "")).strip()
    topic = str(raw.get("topic", "")).strip()
    if not stream_id:
        raise ObserveError("subscription id is required")
    if not topic.startswith("/") or "\x00" in topic:
        raise ObserveError("topic must be an absolute ROS name")
    profile = str(raw.get("profile", "auto")).strip().lower()
    if profile not in {"auto", "latest", "pointcloud", "jpeg", "raw"}:
        raise ObserveError(f"unsupported profile {profile!r}")

    def positive_float(name: str, default: float) -> float:
        try:
            value = float(raw.get(name, default))
        except (TypeError, ValueError) as exc:
            raise ObserveError(f"{name} must be a number") from exc
        if value <= 0:
            raise ObserveError(f"{name} must be greater than zero")
        return value

    def positive_int(name: str, default: int) -> int:
        try:
            value = int(raw.get(name, default))
        except (TypeError, ValueError) as exc:
            raise ObserveError(f"{name} must be an integer") from exc
        if value <= 0:
            raise ObserveError(f"{name} must be greater than zero")
        return value

    return StreamSpec(
        stream_id=stream_id,
        topic=topic,
        type_name=str(raw.get("type", "")).strip(),
        profile=profile,
        max_hz=min(positive_float("max_hz", limits.max_hz), limits.max_hz),
        max_bytes_per_second=min(
            positive_int("max_bytes_per_second", limits.max_bytes_per_second),
            limits.max_bytes_per_second,
        ),
        point_stride=max(
            positive_int("point_stride", limits.point_stride), limits.point_stride
        ),
        jpeg_quality=min(
            positive_int("jpeg_quality", limits.jpeg_quality), limits.jpeg_quality
        ),
        max_width=min(positive_int("max_width", limits.max_width), limits.max_width),
    )


class ObserveSubscription:
    """One demand-owned ROS subscription with a newest-only output queue."""

    def __init__(self, gateway, spec: StreamSpec, type_name: str, message_class) -> None:
        self.gateway = gateway
        self.spec = spec
        self.type_name = type_name
        self.message_class = message_class
        self.queue = asyncio.Queue(maxsize=1)
        self.shared_key = None
        self.last_emitted_at = 0.0
        self.dropped = 0
        self.budget = ByteBudget(spec.max_bytes_per_second)
        self.closed = False

    def start(self) -> None:
        self.gateway.attach_consumer(self)

    def _on_message(self, message) -> None:
        if self.closed:
            return
        now = time.monotonic()
        if now - self.last_emitted_at < 1.0 / self.spec.max_hz:
            self.dropped += 1
            return
        try:
            frame = process_message(message, self.type_name, self.spec)
        except Exception as exc:
            self.gateway.log(f"processor failed for {self.spec.topic}: {exc}")
            self.dropped += 1
            return
        if not self.budget.allow(len(frame.payload), now):
            self.dropped += 1
            return
        if not self.gateway.allow_session_bytes(len(frame.payload), now):
            self.dropped += 1
            return
        self.last_emitted_at = now
        self.gateway.loop.call_soon_threadsafe(self._put_latest, frame)

    def _put_latest(self, frame: ProcessedFrame) -> None:
        if self.closed:
            return
        if self.queue.full():
            try:
                self.queue.get_nowait()
                self.dropped += 1
            except asyncio.QueueEmpty:
                pass
        self.queue.put_nowait(frame)

    async def next_packed_frame(self) -> bytes:
        frame = await self.queue.get()
        dropped, self.dropped = self.dropped, 0
        return pack_frame(self.spec, self.type_name, frame, dropped)

    def close(self) -> None:
        if self.closed:
            return
        self.closed = True
        self.gateway.detach_consumer(self)


def resolve_profile(type_name: str, requested: str) -> str:
    if requested != "auto":
        return requested
    if type_name == "sensor_msgs/msg/PointCloud2":
        return "pointcloud"
    if type_name == "sensor_msgs/msg/Image":
        return "jpeg"
    if type_name == "sensor_msgs/msg/CompressedImage":
        return "jpeg"
    return "latest"


def process_message(message, type_name: str, spec: StreamSpec) -> ProcessedFrame:
    from rclpy.serialization import serialize_message

    profile = resolve_profile(type_name, spec.profile)
    timestamp_ns = time.time_ns()
    if profile == "pointcloud":
        if type_name != "sensor_msgs/msg/PointCloud2":
            raise ObserveError("pointcloud profile requires sensor_msgs/msg/PointCloud2")
        message = downsample_pointcloud(message, spec.point_stride)
        return ProcessedFrame(
            bytes(serialize_message(message)), "cdr", profile, timestamp_ns
        )
    if profile == "jpeg":
        if type_name == "sensor_msgs/msg/CompressedImage":
            return ProcessedFrame(bytes(message.data), "jpeg", profile, timestamp_ns)
        if type_name != "sensor_msgs/msg/Image":
            raise ObserveError("jpeg profile requires sensor_msgs/msg/Image")
        return ProcessedFrame(
            image_to_jpeg(message, spec.max_width, spec.jpeg_quality),
            "jpeg",
            profile,
            timestamp_ns,
        )
    return ProcessedFrame(
        bytes(serialize_message(message)), "cdr", profile, timestamp_ns
    )


def downsample_pointcloud(message, stride: int):
    import numpy
    from sensor_msgs.msg import PointCloud2

    if message.width == 0 or message.height == 0 or message.point_step == 0:
        return message
    source = numpy.frombuffer(message.data, dtype=numpy.uint8)
    required_size = message.height * message.row_step
    if source.size < required_size:
        raise ObserveError("PointCloud2 payload is shorter than its dimensions")
    rows = source[:required_size].reshape(message.height, message.row_step)
    points = rows[:, : message.width * message.point_step].reshape(
        message.width * message.height, message.point_step
    )
    sampled = points[::stride].tobytes()

    preview = PointCloud2()
    preview.header = message.header
    preview.height = 1
    preview.width = len(sampled) // message.point_step
    preview.fields = message.fields
    preview.is_bigendian = message.is_bigendian
    preview.point_step = message.point_step
    preview.row_step = preview.width * preview.point_step
    preview.data = sampled
    preview.is_dense = message.is_dense
    return preview


def image_to_jpeg(message, max_width: int, quality: int) -> bytes:
    import numpy
    from PIL import Image

    encodings = {
        "rgb8": ("RGB", 3),
        "bgr8": ("BGR", 3),
        "rgba8": ("RGBA", 4),
        "bgra8": ("BGRA", 4),
        "mono8": ("L", 1),
    }
    encoding = message.encoding.lower()
    if encoding not in encodings:
        raise ObserveError(f"unsupported image encoding {message.encoding!r}")
    mode, channels = encodings[encoding]
    source = numpy.frombuffer(message.data, dtype=numpy.uint8)
    required_size = message.height * message.step
    if source.size < required_size:
        raise ObserveError("Image payload is shorter than its dimensions")
    rows = source[:required_size].reshape(message.height, message.step)
    pixels = rows[:, : message.width * channels].reshape(
        message.height, message.width, channels
    )
    if channels == 1:
        pixels = pixels[:, :, 0]
    if mode == "BGR":
        pixels = pixels[:, :, ::-1]
        mode = "RGB"
    elif mode == "BGRA":
        pixels = pixels[:, :, [2, 1, 0, 3]]
        mode = "RGBA"
    image = Image.fromarray(pixels, mode=mode)
    if image.mode == "RGBA":
        image = image.convert("RGB")
    if max_width and image.width > max_width:
        height = max(1, round(image.height * max_width / image.width))
        image = image.resize((max_width, height), Image.Resampling.LANCZOS)
    output = io.BytesIO()
    image.save(output, format="JPEG", quality=quality, optimize=False)
    return output.getvalue()


class ObserveGateway:
    def __init__(self, args, loop: asyncio.AbstractEventLoop) -> None:
        import rclpy
        from rclpy.executors import MultiThreadedExecutor, SingleThreadedExecutor
        from rclpy.qos import (
            DurabilityPolicy,
            HistoryPolicy,
            QoSProfile,
            ReliabilityPolicy,
        )

        self.args = args
        self.loop = loop
        self.log_lock = threading.Lock()
        rclpy.init()
        self.rclpy = rclpy
        self.node = rclpy.create_node("wendy_observe_gateway")
        self.sensor_qos = QoSProfile(
            reliability=ReliabilityPolicy.BEST_EFFORT,
            durability=DurabilityPolicy.VOLATILE,
            history=HistoryPolicy.KEEP_LAST,
            depth=1,
        )
        self.executor = MultiThreadedExecutor(num_threads=2)
        self.session_budget = ByteBudget(args.max_bytes_per_second)
        self.session_budget_lock = threading.Lock()
        self.subscription_lock = threading.Lock()
        self.shared_subscriptions = {}
        self.subscription_sequence = 0
        self.stream_executor_factory = SingleThreadedExecutor
        self.stream_thread_factory = lambda target: threading.Thread(
            target=target, daemon=True
        )
        self.executor.add_node(self.node)
        self.executor_thread = threading.Thread(target=self.executor.spin, daemon=True)
        self.executor_thread.start()

    def attach_consumer(self, consumer: ObserveSubscription) -> None:
        key = (consumer.spec.topic, consumer.type_name)
        with self.subscription_lock:
            shared = self.shared_subscriptions.get(key)
            if shared is None:
                shared = SharedROSSubscription()
                self.shared_subscriptions[key] = shared
                self.subscription_sequence += 1
                try:
                    shared.node = self.rclpy.create_node(
                        f"wendy_observe_stream_{self.subscription_sequence}"
                    )
                    shared.handle = shared.node.create_subscription(
                        consumer.message_class,
                        consumer.spec.topic,
                        lambda message, subscription_key=key: self._dispatch_message(
                            subscription_key, message
                        ),
                        self.sensor_qos,
                    )
                    # A demand-driven reader owns its executor thread. This
                    # lets teardown stop and join the wait set before the ROS
                    # handle is destroyed; mutating an executor that is
                    # concurrently spinning can raise rclpy InvalidHandle.
                    shared.executor = self.stream_executor_factory()
                    shared.executor.add_node(shared.node)
                    shared.executor_thread = self.stream_thread_factory(
                        shared.executor.spin
                    )
                    shared.executor_thread.start()
                except Exception:
                    del self.shared_subscriptions[key]
                    if shared.executor is not None:
                        shared.executor.shutdown(timeout_sec=3.0)
                    if shared.node is not None:
                        shared.node.destroy_node()
                    raise
            if shared.teardown is not None:
                shared.teardown.cancel()
                shared.teardown = None
            shared.consumers.add(consumer)
            consumer.shared_key = key

    def detach_consumer(self, consumer: ObserveSubscription) -> None:
        key = consumer.shared_key
        consumer.shared_key = None
        if key is None:
            return
        with self.subscription_lock:
            shared = self.shared_subscriptions.get(key)
            if shared is None:
                return
            shared.consumers.discard(consumer)
            if not shared.consumers and shared.teardown is None:
                # Reuse the DDS reader when a panel closes and immediately
                # reopens. Destroying and recreating the entity back-to-back
                # can race rclpy's executor wait-set rebuild.
                shared.teardown = self.loop.call_later(
                    UPSTREAM_IDLE_GRACE_SECONDS,
                    self._destroy_idle_subscription,
                    key,
                    shared,
                )

    def _dispatch_message(self, key, message) -> None:
        with self.subscription_lock:
            shared = self.shared_subscriptions.get(key)
            consumers = tuple(shared.consumers) if shared is not None else ()
        for consumer in consumers:
            consumer._on_message(message)

    def _destroy_idle_subscription(self, key, expected) -> None:
        with self.subscription_lock:
            shared = self.shared_subscriptions.get(key)
            if shared is not expected or shared.consumers:
                return
            del self.shared_subscriptions[key]
            shared.teardown = None
            handle = shared.handle
            node = shared.node
            shared.handle = None
            shared.node = None
        if node is not None:
            self._stop_shared_subscription(shared, node, handle)

    def _stop_shared_subscription(self, shared, node=None, handle=None) -> None:
        node = shared.node if node is None else node
        handle = shared.handle if handle is None else handle
        if shared.executor is not None:
            shared.executor.shutdown(timeout_sec=3.0)
        if shared.executor_thread is not None:
            shared.executor_thread.join(timeout=3.0)
        if shared.executor is not None and node is not None:
            shared.executor.remove_node(node)
        if node is not None:
            if handle is not None:
                node.destroy_subscription(handle)
            node.destroy_node()

    def allow_session_bytes(self, size: int, now: float) -> bool:
        with self.session_budget_lock:
            return self.session_budget.allow(size, now)

    def log(self, message: str) -> None:
        with self.log_lock:
            print(f"wendy-observe: {message}", flush=True)

    def catalog(self) -> list:
        return [
            {"name": name, "types": sorted(types)}
            for name, types in sorted(self.node.get_topic_names_and_types())
            if not name.startswith("/_")
        ]

    def resolve_type(self, spec: StreamSpec) -> str:
        matches = dict(self.node.get_topic_names_and_types()).get(spec.topic, [])
        if spec.type_name:
            if matches and spec.type_name not in matches:
                raise ObserveError(
                    f"topic {spec.topic} does not advertise type {spec.type_name}"
                )
            return spec.type_name
        if not matches:
            raise ObserveError(f"topic {spec.topic} was not found")
        if len(matches) != 1:
            raise ObserveError(
                f"topic {spec.topic} has multiple types; specify one of {matches}"
            )
        return matches[0]

    def open_subscription(self, spec: StreamSpec) -> ObserveSubscription:
        from rosidl_runtime_py.utilities import get_message

        type_name = self.resolve_type(spec)
        try:
            message_class = get_message(type_name)
        except (AttributeError, ModuleNotFoundError, ValueError) as exc:
            raise ObserveError(
                f"message support for {type_name} is not installed in Observe"
            ) from exc
        subscription = ObserveSubscription(self, spec, type_name, message_class)
        subscription.start()
        self.log(
            f"subscribed {spec.stream_id} to {spec.topic} as "
            f"{resolve_profile(type_name, spec.profile)} via demand"
        )
        return subscription

    def close(self) -> None:
        with self.subscription_lock:
            subscriptions = tuple(self.shared_subscriptions.values())
            self.shared_subscriptions.clear()
            for shared in subscriptions:
                if shared.teardown is not None:
                    shared.teardown.cancel()
                shared.consumers.clear()
        for shared in subscriptions:
            if shared.node is not None:
                self._stop_shared_subscription(shared)
        self.executor.shutdown(timeout_sec=3.0)
        self.executor_thread.join(timeout=3.0)
        self.node.destroy_node()
        self.rclpy.shutdown()


def request_spec(request, args, stream_id: str) -> StreamSpec:
    raw = dict(request.query)
    raw["id"] = raw.get("id", stream_id)
    return clamp_spec(raw, args)


async def health_handler(request):
    return request.app["web"].json_response(
        {
            "ok": True,
            "name": "wendy-observe",
            "version": FRAME_VERSION,
            "transports": ["websocket", "https"],
            "queue_policy": "latest",
        }
    )


async def catalog_handler(request):
    gateway = request.app["gateway"]
    return request.app["web"].json_response({"topics": gateway.catalog()})


async def snapshot_handler(request):
    from aiohttp import web

    gateway = request.app["gateway"]
    try:
        spec = request_spec(request, gateway.args, "snapshot")
        subscription = gateway.open_subscription(spec)
    except ObserveError as exc:
        raise web.HTTPBadRequest(text=str(exc)) from exc
    try:
        packed = await asyncio.wait_for(
            subscription.next_packed_frame(), timeout=gateway.args.snapshot_timeout
        )
        return web.Response(
            body=packed,
            content_type="application/vnd.wendy.observe-frame",
            headers={"Cache-Control": "no-store"},
        )
    except asyncio.TimeoutError as exc:
        raise web.HTTPGatewayTimeout(text="timed out waiting for a ROS message") from exc
    finally:
        subscription.close()


async def https_stream_handler(request):
    from aiohttp import web

    gateway = request.app["gateway"]
    try:
        spec = request_spec(request, gateway.args, "https-stream")
        subscription = gateway.open_subscription(spec)
    except ObserveError as exc:
        raise web.HTTPBadRequest(text=str(exc)) from exc
    response = web.StreamResponse(
        status=200,
        headers={
            "Content-Type": "application/vnd.wendy.observe-stream",
            "Cache-Control": "no-store",
            "X-Content-Type-Options": "nosniff",
        },
    )
    await response.prepare(request)
    try:
        while True:
            await response.write(await subscription.next_packed_frame())
    except (ConnectionError, asyncio.CancelledError):
        pass
    finally:
        subscription.close()
    return response


async def websocket_writer(websocket, subscription: ObserveSubscription) -> None:
    while True:
        await websocket.send_bytes(await subscription.next_packed_frame())


async def websocket_handler(request):
    from aiohttp import WSMsgType, web

    gateway = request.app["gateway"]
    websocket = web.WebSocketResponse(max_msg_size=1_048_576, heartbeat=15.0)
    await websocket.prepare(request)
    subscriptions: Dict[str, ObserveSubscription] = {}
    writers: Dict[str, asyncio.Task] = {}

    async def unsubscribe(stream_id: str) -> None:
        task = writers.pop(stream_id, None)
        if task is not None:
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                pass
            except (ConnectionError, RuntimeError):
                pass
        subscription = subscriptions.pop(stream_id, None)
        if subscription is not None:
            subscription.close()
            gateway.log(f"unsubscribed {stream_id} from {subscription.spec.topic}")

    await websocket.send_json(
        {
            "op": "hello",
            "version": FRAME_VERSION,
            "transports": ["websocket", "https"],
            "queue_policy": "latest",
        }
    )
    try:
        async for message in websocket:
            if message.type != WSMsgType.TEXT:
                await websocket.send_json({"op": "error", "error": "expected JSON text"})
                continue
            try:
                command = json.loads(message.data)
                operation = command.get("op")
                stream_id = str(command.get("id", "")).strip()
                if operation == "subscribe":
                    spec = clamp_spec(command, gateway.args)
                    await unsubscribe(spec.stream_id)
                    subscription = gateway.open_subscription(spec)
                    subscriptions[spec.stream_id] = subscription
                    writers[spec.stream_id] = asyncio.create_task(
                        websocket_writer(websocket, subscription)
                    )
                    await websocket.send_json(
                        {
                            "op": "subscribed",
                            "id": spec.stream_id,
                            "topic": spec.topic,
                            "type": subscription.type_name,
                            "profile": resolve_profile(
                                subscription.type_name, spec.profile
                            ),
                        }
                    )
                elif operation == "unsubscribe":
                    if not stream_id:
                        raise ObserveError("unsubscribe id is required")
                    await unsubscribe(stream_id)
                    await websocket.send_json({"op": "unsubscribed", "id": stream_id})
                elif operation == "catalog":
                    await websocket.send_json(
                        {"op": "catalog", "topics": gateway.catalog()}
                    )
                else:
                    raise ObserveError(f"unsupported operation {operation!r}")
            except (ObserveError, json.JSONDecodeError) as exc:
                await websocket.send_json({"op": "error", "error": str(exc)})
    finally:
        for stream_id in list(subscriptions):
            await unsubscribe(stream_id)
    return websocket


def tls_context(cert_path: str, key_path: str) -> ssl.SSLContext:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(cert_path, key_path)
    return context


async def run(args) -> None:
    from aiohttp import web

    loop = asyncio.get_running_loop()
    gateway = ObserveGateway(args, loop)
    app = web.Application(client_max_size=1_048_576)
    app["gateway"] = gateway
    app["web"] = web
    app.router.add_get("/v1/health", health_handler)
    app.router.add_get("/v1/catalog", catalog_handler)
    app.router.add_get("/v1/snapshot", snapshot_handler)
    app.router.add_get("/v1/stream", https_stream_handler)
    app.router.add_get("/v1/live", websocket_handler)
    runner = web.AppRunner(app, access_log=None)
    await runner.setup()
    site = web.TCPSite(
        runner,
        args.address,
        args.port,
        ssl_context=tls_context(args.tls_cert, args.tls_key),
    )
    await site.start()
    gateway.log(
        f"ready on https://{args.address}:{args.port}; "
        "live WebSocket path is /v1/live"
    )

    stop = asyncio.Event()
    for name in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(name, stop.set)
        except NotImplementedError:
            pass
    try:
        await stop.wait()
    finally:
        await runner.cleanup()
        gateway.close()


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--address", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8780)
    parser.add_argument("--tls-cert", required=True)
    parser.add_argument("--tls-key", required=True)
    parser.add_argument("--max-hz", type=float, default=DEFAULT_MAX_HZ)
    parser.add_argument(
        "--max-bytes-per-second",
        type=int,
        default=DEFAULT_MAX_BYTES_PER_SECOND,
    )
    parser.add_argument("--point-stride", type=int, default=DEFAULT_POINT_STRIDE)
    parser.add_argument("--jpeg-quality", type=int, default=DEFAULT_JPEG_QUALITY)
    parser.add_argument("--max-width", type=int, default=DEFAULT_MAX_WIDTH)
    parser.add_argument("--snapshot-timeout", type=float, default=5.0)
    args = parser.parse_args()
    for name in (
        "max_hz",
        "max_bytes_per_second",
        "point_stride",
        "jpeg_quality",
        "max_width",
        "snapshot_timeout",
    ):
        if getattr(args, name) <= 0:
            parser.error(f"--{name.replace('_', '-')} must be greater than zero")
    if args.jpeg_quality > 100:
        parser.error("--jpeg-quality must be at most 100")
    return args


if __name__ == "__main__":
    asyncio.run(run(parse_args()))
