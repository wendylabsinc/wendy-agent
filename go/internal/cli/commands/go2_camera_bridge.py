#!/usr/bin/env python3
"""Publish JPEGs from Unitree's official Go2 camera client into ROS 2."""

import argparse
import collections
import io
import json
import os
import queue
import struct
import subprocess
import sys
import threading
import time


CAMERA_TOPIC = "/front_camera/image/compressed"
DEMAND_POLL_SECONDS = 0.1
IDLE_GRACE_SECONDS = 2.0
INITIAL_RESTART_DELAY_SECONDS = 0.5
MAX_RESTART_DELAY_SECONDS = 5.0
CAMERA_STALL_SECONDS = 12.0
MAX_CONSECUTIVE_FAILURES = 3


def read_interface_stats(interface: str) -> dict:
    base = f"/sys/class/net/{interface}"
    result = {"interface": interface}
    for name in ("operstate", "carrier", "speed", "duplex"):
        try:
            with open(os.path.join(base, name), encoding="utf-8") as source:
                result[name] = source.read().strip()
        except (FileNotFoundError, OSError):
            pass
    for name in (
        "rx_bytes",
        "rx_packets",
        "rx_errors",
        "rx_dropped",
        "rx_missed_errors",
        "tx_bytes",
        "tx_packets",
        "tx_errors",
        "tx_dropped",
    ):
        try:
            with open(
                os.path.join(base, "statistics", name), encoding="utf-8"
            ) as source:
                result[name] = int(source.read().strip())
        except (FileNotFoundError, OSError, ValueError):
            pass
    return result


def numeric_delta(before: dict, after: dict) -> dict:
    return {
        key: after[key] - before[key]
        for key in after.keys() & before.keys()
        if isinstance(after[key], int) and isinstance(before[key], int)
    }


def percentile(values: list, fraction: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, round((len(ordered) - 1) * fraction))
    return ordered[index]


def diagnose(interface: str, duration: float, timeout: float) -> int:
    """Run one VideoClient continuously and print a machine-readable soak report."""
    from unitree_sdk2py.core.channel import ChannelFactoryInitialize
    from unitree_sdk2py.go2.video.video_client import VideoClient

    # Match the isolated capture child: Unitree creates its own explicit DDS
    # participant and must not inherit the ROS participant's CycloneDDS config.
    os.environ.pop("CYCLONEDDS_URI", None)
    print(
        f"Starting Go2 camera diagnostic on {interface} for {duration:g}s "
        f"with a {timeout:g}s API timeout",
        file=sys.stderr,
        flush=True,
    )

    before = read_interface_stats(interface)
    started_wall = time.time()
    started = time.monotonic()
    ChannelFactoryInitialize(0, interface)
    client = VideoClient()
    client.SetTimeout(timeout)
    client.Init()

    calls = 0
    successes = 0
    total_bytes = 0
    invalid_jpegs = 0
    error_codes = collections.Counter()
    latencies_ms = []
    consecutive_failures = 0
    max_consecutive_failures = 0
    last_success_at = None
    longest_frame_gap = 0.0
    next_progress_at = started + 30.0

    while time.monotonic() - started < duration:
        call_started = time.monotonic()
        calls += 1
        try:
            code, data = client.GetImageSample()
        except Exception as exc:
            code = f"exception:{type(exc).__name__}"
            data = b""
            time.sleep(0.1)
        call_finished = time.monotonic()
        latencies_ms.append((call_finished - call_started) * 1000.0)

        if code == 0:
            jpeg = bytes(data)
            successes += 1
            total_bytes += len(jpeg)
            if not jpeg.startswith(b"\xff\xd8"):
                invalid_jpegs += 1
            if last_success_at is not None:
                longest_frame_gap = max(
                    longest_frame_gap, call_finished - last_success_at
                )
            last_success_at = call_finished
            consecutive_failures = 0
        else:
            error_codes[str(code)] += 1
            consecutive_failures += 1
            max_consecutive_failures = max(
                max_consecutive_failures, consecutive_failures
            )

        if call_finished >= next_progress_at:
            print(
                f"camera diagnostic: {successes}/{calls} successful calls; "
                f"errors={dict(error_codes)}",
                file=sys.stderr,
                flush=True,
            )
            next_progress_at = call_finished + 30.0

    finished = time.monotonic()
    elapsed = finished - started
    if last_success_at is None:
        longest_frame_gap = elapsed
    else:
        longest_frame_gap = max(longest_frame_gap, finished - last_success_at)
    after = read_interface_stats(interface)
    try:
        load_average = list(os.getloadavg())
    except OSError:
        load_average = []

    report = {
        "test": "wendy_go2_camera",
        "started_unix": started_wall,
        "duration_seconds": round(elapsed, 3),
        "interface": interface,
        "api_timeout_seconds": timeout,
        "calls": calls,
        "successful_calls": successes,
        "failed_calls": calls - successes,
        "success_percent": round(100.0 * successes / calls, 3) if calls else 0.0,
        "error_codes": dict(error_codes),
        "max_consecutive_failures": max_consecutive_failures,
        "successful_fps": round(successes / elapsed, 3) if elapsed else 0.0,
        "total_jpeg_bytes": total_bytes,
        "average_jpeg_bytes": round(total_bytes / successes, 1) if successes else 0.0,
        "invalid_jpeg_headers": invalid_jpegs,
        "longest_frame_gap_seconds": round(longest_frame_gap, 3),
        "call_latency_ms": {
            "p50": round(percentile(latencies_ms, 0.50), 3),
            "p95": round(percentile(latencies_ms, 0.95), 3),
            "max": round(max(latencies_ms), 3) if latencies_ms else 0.0,
        },
        "interface_before": before,
        "interface_after": after,
        "interface_delta": numeric_delta(before, after),
        "load_average": load_average,
    }
    print(json.dumps(report, indent=2, sort_keys=True), flush=True)
    return 0 if successes > 0 else 1


def read_exact(fd: int, size: int) -> bytes:
    chunks = bytearray()
    while len(chunks) < size:
        chunk = os.read(fd, size - len(chunks))
        if not chunk:
            raise EOFError("Unitree camera client stopped")
        chunks.extend(chunk)
    return bytes(chunks)


def capture(interface: str, output_fd: int) -> int:
    # Keep the Unitree CycloneDDS participant in its own process. A ROS 2
    # CycloneDDS participant cannot coexist with the explicit domain created by
    # this SDK; either initialization order returns DDS_RETCODE_PRECONDITION_NOT_MET.
    from unitree_sdk2py.core.channel import ChannelFactoryInitialize
    from unitree_sdk2py.go2.video.video_client import VideoClient

    ChannelFactoryInitialize(0, interface)
    client = VideoClient()
    client.SetTimeout(3.0)
    client.Init()

    retry_delay = 0.1
    next_warning_at = 0.0
    consecutive_failures = 0
    with os.fdopen(output_fd, "wb", buffering=0) as output:
        while True:
            code, data = client.GetImageSample()
            if code != 0:
                consecutive_failures += 1
                now = time.monotonic()
                if now >= next_warning_at:
                    print(
                        f"Go2 camera unavailable (code {code}); retrying in "
                        f"{retry_delay:.1f}s",
                        file=sys.stderr,
                        flush=True,
                    )
                    next_warning_at = now + 30.0
                if consecutive_failures >= MAX_CONSECUTIVE_FAILURES:
                    print(
                        "Go2 camera failed "
                        f"{consecutive_failures} consecutive requests (last code "
                        f"{code}); restarting VideoClient",
                        file=sys.stderr,
                        flush=True,
                    )
                    return 1
                time.sleep(retry_delay)
                retry_delay = min(retry_delay * 2.0, 5.0)
                continue

            consecutive_failures = 0
            retry_delay = 0.1
            next_warning_at = 0.0
            jpeg = bytes(data)
            try:
                output.write(struct.pack("<I", len(jpeg)))
                output.write(jpeg)
            except BrokenPipeError:
                return 0


class CameraCapture:
    """Own a restartable Unitree VideoClient child and retain only its newest frame."""

    def __init__(self, interface: str) -> None:
        self.interface = interface
        self.process = None
        self.reader_thread = None
        self.frames = queue.Queue(maxsize=1)
        self.started_at = 0.0
        self.last_frame_at = None
        self.reader_error = None

    def start(self) -> None:
        read_fd, write_fd = os.pipe()
        camera_env = os.environ.copy()
        camera_env.pop("CYCLONEDDS_URI", None)
        try:
            self.process = subprocess.Popen(
                [
                    sys.executable,
                    __file__,
                    "--capture",
                    "--interface",
                    self.interface,
                    "--output-fd",
                    str(write_fd),
                ],
                env=camera_env,
                pass_fds=(write_fd,),
                stdout=subprocess.DEVNULL,
            )
        except Exception:
            os.close(read_fd)
            os.close(write_fd)
            raise
        os.close(write_fd)
        self.started_at = time.monotonic()
        self.last_frame_at = None
        self.reader_error = None
        self.reader_thread = threading.Thread(
            target=self._read_frames, args=(read_fd,), daemon=True
        )
        self.reader_thread.start()

    def _read_frames(self, read_fd: int) -> None:
        try:
            while True:
                size = struct.unpack("<I", read_exact(read_fd, 4))[0]
                if size == 0 or size > 10_000_000:
                    raise ValueError(f"invalid Go2 camera frame size: {size}")
                jpeg = read_exact(read_fd, size)
                self.last_frame_at = time.monotonic()
                try:
                    self.frames.get_nowait()
                except queue.Empty:
                    pass
                self.frames.put_nowait(jpeg)
        except (EOFError, OSError, ValueError) as exc:
            self.reader_error = exc
        finally:
            os.close(read_fd)

    def take_latest(self):
        try:
            return self.frames.get_nowait()
        except queue.Empty:
            return None

    def returncode(self):
        if self.process is None:
            return None
        return self.process.poll()

    def stalled(self, now: float) -> bool:
        last_activity = self.last_frame_at or self.started_at
        return last_activity > 0 and now - last_activity >= CAMERA_STALL_SECONDS

    def stop(self) -> None:
        if self.process is not None:
            if self.process.poll() is None:
                self.process.terminate()
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait()
            else:
                self.process.wait()
        if self.reader_thread is not None:
            self.reader_thread.join(timeout=5)
        self.process = None
        self.reader_thread = None
        while True:
            try:
                self.frames.get_nowait()
            except queue.Empty:
                break


def transcode_jpeg(jpeg: bytes, quality: int, max_width: int) -> bytes:
    from PIL import Image, ImageFile

    # The Go2 occasionally returns a valid JPEG with a few trailing bytes
    # missing. Pillow can still decode these frames safely.
    ImageFile.LOAD_TRUNCATED_IMAGES = True

    with Image.open(io.BytesIO(jpeg)) as decoded:
        image = decoded.convert("RGB")
        if max_width > 0 and image.width > max_width:
            height = max(1, round(image.height * max_width / image.width))
            resampling = getattr(Image, "Resampling", Image)
            image = image.resize((max_width, height), resampling.BILINEAR)
        output = io.BytesIO()
        image.save(output, format="JPEG", quality=quality, optimize=False)
        return output.getvalue()


def publish(interface: str, fps: float, jpeg_quality: int, max_width: int) -> None:
    import rclpy
    from rclpy.qos import DurabilityPolicy, HistoryPolicy, QoSProfile, ReliabilityPolicy
    from sensor_msgs.msg import CompressedImage

    rclpy.init()
    node = rclpy.create_node("wendy_go2_camera_bridge")
    publisher = node.create_publisher(
        CompressedImage,
        CAMERA_TOPIC,
        QoSProfile(
            reliability=ReliabilityPolicy.BEST_EFFORT,
            durability=DurabilityPolicy.VOLATILE,
            history=HistoryPolicy.KEEP_LAST,
            depth=1,
        ),
    )

    logged_first_frame = False
    last_published_at = 0.0
    next_transcode_warning_at = 0.0
    last_subscriber_at = None
    capture_process = None
    restart_delay = INITIAL_RESTART_DELAY_SECONDS
    next_restart_at = 0.0
    try:
        while rclpy.ok():
            rclpy.spin_once(node, timeout_sec=DEMAND_POLL_SECONDS)
            now = time.monotonic()
            has_subscribers = publisher.get_subscription_count() > 0

            if has_subscribers:
                last_subscriber_at = now
                if capture_process is None and now >= next_restart_at:
                    capture_process = CameraCapture(interface)
                    try:
                        capture_process.start()
                        node.get_logger().info(
                            f"Starting Go2 VideoClient; {CAMERA_TOPIC} has a subscriber"
                        )
                    except Exception as exc:
                        capture_process = None
                        node.get_logger().warning(
                            f"Could not start Go2 VideoClient: {exc}; retrying in "
                            f"{restart_delay:.1f}s"
                        )
                        next_restart_at = now + restart_delay
                        restart_delay = min(
                            restart_delay * 2.0, MAX_RESTART_DELAY_SECONDS
                        )
            elif (
                capture_process is not None
                and last_subscriber_at is not None
                and now - last_subscriber_at >= IDLE_GRACE_SECONDS
            ):
                capture_process.stop()
                capture_process = None
                restart_delay = INITIAL_RESTART_DELAY_SECONDS
                next_restart_at = 0.0
                node.get_logger().info(
                    f"Stopped Go2 VideoClient; {CAMERA_TOPIC} has no subscribers"
                )

            if capture_process is None:
                continue

            returncode = capture_process.returncode()
            stalled = capture_process.stalled(now)
            if returncode is not None or stalled:
                reason = (
                    f"exited with code {returncode}"
                    if returncode is not None
                    else f"produced no frame for {CAMERA_STALL_SECONDS:g}s"
                )
                capture_process.stop()
                capture_process = None
                if has_subscribers:
                    node.get_logger().warning(
                        f"Go2 VideoClient {reason}; retrying in "
                        f"{restart_delay:.1f}s"
                    )
                    next_restart_at = now + restart_delay
                    restart_delay = min(
                        restart_delay * 2.0, MAX_RESTART_DELAY_SECONDS
                    )
                continue

            jpeg = capture_process.take_latest()
            if jpeg is None:
                continue
            restart_delay = INITIAL_RESTART_DELAY_SECONDS
            if now - last_published_at < 1.0 / fps:
                continue

            try:
                jpeg = transcode_jpeg(jpeg, jpeg_quality, max_width)
            except Exception as exc:
                if now >= next_transcode_warning_at:
                    node.get_logger().warning(
                        f"Could not transcode Go2 JPEG; dropping frame: {exc}"
                    )
                    next_transcode_warning_at = now + 30.0
                continue

            message = CompressedImage()
            message.header.stamp = node.get_clock().now().to_msg()
            message.header.frame_id = "go2_front_camera"
            message.format = "jpeg"
            message.data = jpeg
            publisher.publish(message)
            last_published_at = now

            if not logged_first_frame:
                node.get_logger().info(
                    f"Publishing Go2 JPEG frames on {CAMERA_TOPIC} "
                    f"at up to {fps:g} FPS, quality {jpeg_quality}, "
                    f"max width {max_width or 'source'}"
                )
                logged_first_frame = True
    finally:
        if capture_process is not None:
            capture_process.stop()
        node.destroy_node()
        rclpy.shutdown()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--interface", required=True)
    parser.add_argument("--capture", action="store_true")
    parser.add_argument("--output-fd", type=int)
    parser.add_argument(
        "--diagnose-seconds",
        type=float,
        default=0.0,
        help="run a direct VideoClient soak test and print a JSON report",
    )
    parser.add_argument("--diagnose-timeout", type=float, default=3.0)
    parser.add_argument("--fps", type=float, default=8.0)
    parser.add_argument("--jpeg-quality", type=int, default=65)
    parser.add_argument("--max-width", type=int, default=960)
    args = parser.parse_args()

    if args.diagnose_seconds > 0:
        raise SystemExit(
            diagnose(args.interface, args.diagnose_seconds, args.diagnose_timeout)
        )
    if args.capture:
        if args.output_fd is None:
            parser.error("--capture requires --output-fd")
        raise SystemExit(capture(args.interface, args.output_fd))
    else:
        publish(args.interface, args.fps, args.jpeg_quality, args.max_width)


if __name__ == "__main__":
    main()
