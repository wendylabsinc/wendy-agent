#!/usr/bin/env python3
"""Publish a bandwidth-bounded preview of the Go2 Hesai point cloud."""

import time

import numpy
import rclpy
from rclpy.node import Node
from rclpy.qos import DurabilityPolicy, HistoryPolicy, QoSProfile, ReliabilityPolicy
from sensor_msgs.msg import PointCloud2


INPUT_TOPIC = "/hesai/points"
OUTPUT_TOPIC = "/hesai/points/preview"
MAX_FPS = 5.0
POINT_STRIDE = 4


class HesaiPreviewBridge(Node):
    def __init__(self) -> None:
        super().__init__("wendy_hesai_preview_bridge")
        qos = QoSProfile(
            reliability=ReliabilityPolicy.BEST_EFFORT,
            durability=DurabilityPolicy.VOLATILE,
            history=HistoryPolicy.KEEP_LAST,
            depth=1,
        )
        self.publisher = self.create_publisher(PointCloud2, OUTPUT_TOPIC, qos)
        self.subscription = self.create_subscription(
            PointCloud2, INPUT_TOPIC, self.publish_preview, qos
        )
        self.last_published_at = 0.0
        self.logged_first_cloud = False

    def publish_preview(self, message: PointCloud2) -> None:
        now = time.monotonic()
        if now - self.last_published_at < 1.0 / MAX_FPS:
            return
        if message.width == 0 or message.height == 0 or message.point_step == 0:
            return

        total_points = message.width * message.height
        source = numpy.frombuffer(message.data, dtype=numpy.uint8)
        required_size = message.height * message.row_step
        if source.size < required_size:
            return

        # PointCloud2 rows may contain padding after width * point_step. Strip
        # it before flattening, then select whole point records in NumPy so a
        # large Hesai scan never runs a Python loop for every point.
        rows = source[:required_size].reshape(message.height, message.row_step)
        points = rows[:, : message.width * message.point_step].reshape(
            total_points, message.point_step
        )
        sampled = points[::POINT_STRIDE].tobytes()
        sampled_points = len(sampled) // message.point_step

        preview = PointCloud2()
        preview.header = message.header
        preview.height = 1
        preview.width = sampled_points
        preview.fields = message.fields
        preview.is_bigendian = message.is_bigendian
        preview.point_step = message.point_step
        preview.row_step = sampled_points * message.point_step
        preview.data = sampled
        preview.is_dense = message.is_dense
        self.publisher.publish(preview)
        self.last_published_at = now

        if not self.logged_first_cloud:
            self.get_logger().info(
                f"Publishing {OUTPUT_TOPIC} at up to {MAX_FPS:g} Hz with "
                f"every {POINT_STRIDE}th point"
            )
            self.logged_first_cloud = True


def main() -> None:
    rclpy.init()
    node = HesaiPreviewBridge()
    try:
        rclpy.spin(node)
    finally:
        node.destroy_node()
        rclpy.shutdown()


if __name__ == "__main__":
    main()
