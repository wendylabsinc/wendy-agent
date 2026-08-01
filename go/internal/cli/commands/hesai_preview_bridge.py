#!/usr/bin/env python3
"""Publish a bandwidth-bounded preview of the Go2 Hesai point cloud."""

import time

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

        source = memoryview(message.data)
        total_points = message.width * message.height
        max_samples = (total_points + POINT_STRIDE - 1) // POINT_STRIDE
        sampled = bytearray(max_samples * message.point_step)
        sampled_points = 0

        for flat_index in range(0, total_points, POINT_STRIDE):
            row, column = divmod(flat_index, message.width)
            source_start = row * message.row_step + column * message.point_step
            source_end = source_start + message.point_step
            if source_end > len(source):
                break
            output_start = sampled_points * message.point_step
            sampled[output_start : output_start + message.point_step] = source[
                source_start:source_end
            ]
            sampled_points += 1

        if sampled_points == 0:
            return

        preview = PointCloud2()
        preview.header = message.header
        preview.height = 1
        preview.width = sampled_points
        preview.fields = message.fields
        preview.is_bigendian = message.is_bigendian
        preview.point_step = message.point_step
        preview.row_step = sampled_points * message.point_step
        preview.data = sampled[: preview.row_step]
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
