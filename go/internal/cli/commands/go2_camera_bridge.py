#!/usr/bin/env python3
"""Publish JPEGs from Unitree's official Go2 camera client into ROS 2."""

import argparse
import os
import struct
import subprocess
import sys
import time


def read_exact(fd: int, size: int) -> bytes:
    chunks = bytearray()
    while len(chunks) < size:
        chunk = os.read(fd, size - len(chunks))
        if not chunk:
            raise EOFError("Unitree camera client stopped")
        chunks.extend(chunk)
    return bytes(chunks)


def capture(interface: str, output_fd: int) -> None:
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
    with os.fdopen(output_fd, "wb", buffering=0) as output:
        while True:
            code, data = client.GetImageSample()
            if code != 0:
                now = time.monotonic()
                if now >= next_warning_at:
                    print(
                        f"Go2 camera unavailable (code {code}); retrying in "
                        f"{retry_delay:.1f}s",
                        file=sys.stderr,
                        flush=True,
                    )
                    next_warning_at = now + 30.0
                time.sleep(retry_delay)
                retry_delay = min(retry_delay * 2.0, 5.0)
                continue

            retry_delay = 0.1
            next_warning_at = 0.0
            jpeg = bytes(data)
            output.write(struct.pack("<I", len(jpeg)))
            output.write(jpeg)


def publish(interface: str) -> None:
    import rclpy
    from rclpy.qos import DurabilityPolicy, HistoryPolicy, QoSProfile, ReliabilityPolicy
    from sensor_msgs.msg import CompressedImage

    read_fd, write_fd = os.pipe()
    camera_env = os.environ.copy()
    camera_env.pop("CYCLONEDDS_URI", None)
    camera = subprocess.Popen(
        [
            sys.executable,
            __file__,
            "--capture",
            "--interface",
            interface,
            "--output-fd",
            str(write_fd),
        ],
        env=camera_env,
        pass_fds=(write_fd,),
        stdout=subprocess.DEVNULL,
    )
    os.close(write_fd)

    rclpy.init()
    node = rclpy.create_node("wendy_go2_camera_bridge")
    publisher = node.create_publisher(
        CompressedImage,
        "/front_camera/image/compressed",
        QoSProfile(
            reliability=ReliabilityPolicy.BEST_EFFORT,
            durability=DurabilityPolicy.VOLATILE,
            history=HistoryPolicy.KEEP_LAST,
            depth=1,
        ),
    )

    logged_first_frame = False
    last_published_at = 0.0
    try:
        while rclpy.ok():
            size = struct.unpack("<I", read_exact(read_fd, 4))[0]
            if size == 0 or size > 10_000_000:
                raise ValueError(f"invalid Go2 camera frame size: {size}")

            jpeg = read_exact(read_fd, size)
            now = time.monotonic()
            # A 720p JPEG is roughly 200 KB on this Go2. Ten FPS keeps the
            # preview responsive while bounding it to roughly 2 MB/s.
            if now - last_published_at < 1.0 / 10.0:
                continue

            message = CompressedImage()
            message.header.stamp = node.get_clock().now().to_msg()
            message.header.frame_id = "go2_front_camera"
            message.format = "jpeg"
            message.data = jpeg
            publisher.publish(message)
            last_published_at = now
            rclpy.spin_once(node, timeout_sec=0.0)

            if not logged_first_frame:
                node.get_logger().info(
                    "Publishing Go2 JPEG frames on /front_camera/image/compressed"
                )
                logged_first_frame = True
    finally:
        camera.terminate()
        try:
            camera.wait(timeout=5)
        except subprocess.TimeoutExpired:
            camera.kill()
            camera.wait()
        os.close(read_fd)
        node.destroy_node()
        rclpy.shutdown()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--interface", required=True)
    parser.add_argument("--capture", action="store_true")
    parser.add_argument("--output-fd", type=int)
    args = parser.parse_args()

    if args.capture:
        if args.output_fd is None:
            parser.error("--capture requires --output-fd")
        capture(args.interface, args.output_fd)
    else:
        publish(args.interface)


if __name__ == "__main__":
    main()
