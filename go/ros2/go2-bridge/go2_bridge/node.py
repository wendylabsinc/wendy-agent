"""Go2 Air ROS 2 bridge: the agent's paired WebRTC connection as the simulator's
ROS 2 contract (unitree_go/unitree_api types, same topic names and frames).

Robot data arrives through WendyUnitreeRobotService.Stream on the agent socket;
commands go back through WendyUnitreeRobotService.Command, which also stops a
move after 500 ms unless it is refreshed.
"""

import logging
import os
import threading
import time

import grpc
import numpy as np
import rclpy
from foxglove_msgs.msg import CompressedVideo
from rclpy._rclpy_pybind11 import RCLError
from rclpy.executors import ExternalShutdownException
from geometry_msgs.msg import TransformStamped, Twist
from nav_msgs.msg import Odometry
from rclpy.node import Node
from rclpy.qos import QoSProfile, ReliabilityPolicy
from sensor_msgs.msg import Image, Imu, JointState, Joy, PointCloud2, PointField
from std_msgs.msg import String, UInt8MultiArray
from tf2_ros import StaticTransformBroadcaster, TransformBroadcaster
from unitree_api.msg import Request, Response
from unitree_go.msg import LowState, SportModeState

from . import convert
from . import unitree_robot_service_pb2 as robot_pb
from . import unitree_robot_service_pb2_grpc as robot_grpc
from . import unitree_service_pb2 as pairing_pb
from . import unitree_service_pb2_grpc as pairing_grpc

log = logging.getLogger("go2-bridge")

ROBOT_TOPICS = [
    "rt/lf/lowstate",
    "rt/lf/sportmodestate",
    "rt/sportmodestate",
    "rt/utlidar/voxel_map_compressed",
    "rt/utlidar/robot_pose",
    "rt/wirelesscontroller",
    "rt/multiplestate",
    "rt/uslam/frontend/cloud_world_ds",
]
MAX_FORWARD, MAX_YAW = 0.3, 0.6  # the agent's limits
# Response codes used by the Go2 simulator (go2_sim/native_commands.py).
UNSUPPORTED, DENIED = 3203, 3205

# Mount positions match the simulator (go/simulator/go2/go2_sim/sensors.py).
MOUNTS = {"imu_link": (0.0, 0.0, 0.0), "utlidar_imu": (0.0, 0.0, 0.0),
          "lidar_link": (0.28, 0.0, 0.08), "utlidar_lidar": (0.28, 0.0, 0.08),
          "camera_link": (0.40, 0.0, 0.04)}
CAMERA_FRAME = "camera_optical_frame"

COMMANDS = {
    "move": robot_pb.UNITREE_COMMAND_MOVE,
    "stop": robot_pb.UNITREE_COMMAND_STOP,
    "stand_up": robot_pb.UNITREE_COMMAND_STAND_UP,
    "lie_down": robot_pb.UNITREE_COMMAND_LIE_DOWN,
    "hello": robot_pb.UNITREE_COMMAND_HELLO,
}


def fill(msg, data):
    """Copy matching fields from robot JSON into a ROS message, recursively.
    Missing or mistyped fields keep their defaults."""
    if not isinstance(data, dict):
        return
    for name, kind in msg.get_fields_and_field_types().items():
        if name not in data:
            continue
        value, current = data[name], getattr(msg, name)
        try:
            if "/" in kind and "[" in kind:  # fixed array of messages
                for item, element in zip(current, value):
                    fill(item, element)
            elif "/" in kind:
                fill(current, value)
            elif "[" in kind or kind.startswith("sequence"):
                cast = float if "float" in kind or "double" in kind else int
                values = [cast(v) for v in value]
                if "[" in kind and not kind.startswith("sequence"):
                    size = len(current)
                    values = (values + [cast(v) for v in current[len(values):]])[:size]
                setattr(msg, name, values)
            elif kind in ("float", "double"):
                setattr(msg, name, float(value))
            elif kind == "boolean":
                setattr(msg, name, bool(value))
            elif kind == "string":
                setattr(msg, name, str(value))
            else:
                setattr(msg, name, int(value))
        except (TypeError, ValueError, AssertionError):
            continue


class Bridge(Node):
    def __init__(self, channel, serial):
        super().__init__("go2_bridge")
        self.serial = serial
        self.robot = robot_grpc.WendyUnitreeRobotServiceStub(channel)
        qos = QoSProfile(depth=1, reliability=ReliabilityPolicy.RELIABLE)
        pub = self.create_publisher
        self.pubs = {
            "lowstate": pub(LowState, "/lowstate", qos),
            "lf_lowstate": pub(LowState, "/lf/lowstate", qos),
            "sportmodestate": pub(SportModeState, "/sportmodestate", qos),
            "lf_sportmodestate": pub(SportModeState, "/lf/sportmodestate", qos),
            "imu": pub(Imu, "/imu/data", qos),
            "joints": pub(JointState, "/joint_states", qos),
            "odom": pub(Odometry, "/odom", qos),
            "cloud": pub(PointCloud2, "/utlidar/cloud", qos),
            "joy": pub(Joy, "/joy", qos),
            "image": pub(Image, "/camera/color/image_raw", qos),
            "h264": pub(CompressedVideo, "/camera/color/h264", qos),
            "response": pub(Response, "/api/sport/response", qos),
        }
        self.raw_pubs = {}
        self.tf = TransformBroadcaster(self)
        self.static_tf = StaticTransformBroadcaster(self)
        self.publish_mounts()
        self.decoder = None
        self.create_subscription(Twist, "/cmd_vel", self.on_cmd_vel, qos)
        self.create_subscription(Request, "/api/sport/request", self.on_sport_request, 10)

    # --- robot -> ROS ---------------------------------------------------

    def run_stream(self):
        """Stream from the agent forever; reconnect when the robot or agent drops."""
        while rclpy.ok():
            try:
                request = robot_pb.UnitreeStreamRequest(serial=self.serial, topics=ROBOT_TOPICS, video=True)
                for message in self.robot.Stream(request):
                    if message.HasField("topic"):
                        self.on_topic(message.topic)
                    elif message.HasField("video"):
                        self.on_video(message.video)
            except grpc.RpcError as e:
                log.warning("robot stream ended: %s", e.details() if hasattr(e, "details") else e)
            time.sleep(2)

    def stamp(self, received_unix_nanos):
        t = self.get_clock().now().to_msg()
        if received_unix_nanos:
            t.sec, t.nanosec = divmod(received_unix_nanos, 1_000_000_000)
        return t

    def on_topic(self, m):
        try:
            topic, data, payload = convert.parse_envelope(m.data, m.binary)
        except (ValueError, KeyError, IndexError):
            return
        self.publish_raw(m.topic, m.data, m.binary)
        stamp = self.stamp(m.received_unix_nanos)
        if topic == "rt/lf/lowstate":
            self.on_lowstate(data, stamp)
        elif topic in ("rt/lf/sportmodestate", "rt/sportmodestate"):
            self.on_sportmodestate(data, stamp, low_frequency=topic == "rt/lf/sportmodestate")
        elif topic == "rt/utlidar/voxel_map_compressed" and payload is not None:
            self.on_voxels(data, payload, stamp)
        elif topic == "rt/wirelesscontroller":
            msg = Joy()
            msg.header.stamp = stamp
            msg.axes, msg.buttons = convert.joy(data or {})
            self.pubs["joy"].publish(msg)

    def publish_raw(self, topic, data, binary):
        pub = self.raw_pubs.get(topic)
        if pub is None:
            kind = UInt8MultiArray if binary else String
            pub = self.raw_pubs[topic] = self.create_publisher(kind, convert.raw_topic_name(topic), 10)
        if pub.get_subscription_count() == 0:
            return
        if binary:
            msg = UInt8MultiArray()
            msg.data = bytes(data)
        else:
            msg = String()
            msg.data = data.decode("utf-8", "replace")
        pub.publish(msg)

    def on_lowstate(self, data, stamp):
        low = LowState()
        fill(low, data)
        self.pubs["lowstate"].publish(low)
        self.pubs["lf_lowstate"].publish(low)
        imu_state = (data or {}).get("imu_state") or {}
        if "quaternion" in imu_state:
            imu = Imu()
            imu.header.stamp, imu.header.frame_id = stamp, "imu_link"
            q = imu.orientation
            q.x, q.y, q.z, q.w = convert.quaternion_xyzw(imu_state["quaternion"])
            g, a = imu_state.get("gyroscope", [0, 0, 0]), imu_state.get("accelerometer", [0, 0, 0])
            imu.angular_velocity.x, imu.angular_velocity.y, imu.angular_velocity.z = map(float, g)
            imu.linear_acceleration.x, imu.linear_acceleration.y, imu.linear_acceleration.z = map(float, a)
            self.pubs["imu"].publish(imu)
        joints = convert.joint_state(data or {})
        if joints:
            msg = JointState()
            msg.header.stamp = stamp
            msg.name, msg.position, msg.velocity, msg.effort = joints
            self.pubs["joints"].publish(msg)

    def on_sportmodestate(self, data, stamp, low_frequency):
        sport = SportModeState()
        fill(sport, data)
        self.pubs["lf_sportmodestate" if low_frequency else "sportmodestate"].publish(sport)
        data = data or {}
        quaternion = (data.get("imu_state") or {}).get("quaternion")
        position = data.get("position")
        if not quaternion or not position:
            return
        odom = Odometry()
        odom.header.stamp, odom.header.frame_id, odom.child_frame_id = stamp, "odom", "base_link"
        p = odom.pose.pose.position
        p.x, p.y, p.z = (float(v) for v in position[:3])
        q = odom.pose.pose.orientation
        q.x, q.y, q.z, q.w = convert.quaternion_xyzw(quaternion)
        # Assumed world-frame velocity (verify on hardware); ROS twist is in base_link.
        velocity = data.get("velocity") or [0.0, 0.0, 0.0]
        vx, vy = convert.world_to_body(float(velocity[0]), float(velocity[1]), convert.yaw_of(quaternion))
        odom.twist.twist.linear.x, odom.twist.twist.linear.y = vx, vy
        odom.twist.twist.angular.z = float(data.get("yaw_speed", 0.0))
        self.pubs["odom"].publish(odom)
        tf = TransformStamped()
        tf.header = odom.header
        tf.child_frame_id = "base_link"
        tf.transform.translation.x, tf.transform.translation.y, tf.transform.translation.z = p.x, p.y, p.z
        tf.transform.rotation = q
        self.tf.sendTransform(tf)

    def on_voxels(self, meta, payload, stamp):
        if self.pubs["cloud"].get_subscription_count() == 0:
            return
        try:
            points = convert.decode_voxels(payload, meta)
        except Exception as e:  # malformed frame
            log.warning("voxel decode failed: %s", e)
            return
        msg = PointCloud2()
        msg.header.stamp = stamp
        msg.header.frame_id = str(meta.get("frame_id") or "odom")
        msg.height, msg.width = 1, len(points)
        msg.fields = [PointField(name=n, offset=4 * i, datatype=PointField.FLOAT32, count=1)
                      for i, n in enumerate("xyz")]
        msg.is_bigendian, msg.point_step, msg.is_dense = False, 12, True
        msg.row_step = 12 * len(points)
        msg.data = points.tobytes()
        self.pubs["cloud"].publish(msg)

    def on_video(self, frame):
        stamp = self.stamp(frame.received_unix_nanos)
        if self.pubs["h264"].get_subscription_count():
            msg = CompressedVideo()
            msg.timestamp, msg.frame_id, msg.format = stamp, CAMERA_FRAME, "h264"
            msg.data = bytes(frame.annexb)
            self.pubs["h264"].publish(msg)
        # Decode only while someone watches; a new viewer waits for the next keyframe.
        if self.pubs["image"].get_subscription_count() == 0:
            self.decoder = None
            return
        image = self.decode(frame.annexb)
        if image is not None:
            msg = Image()
            msg.header.stamp, msg.header.frame_id = stamp, CAMERA_FRAME
            msg.height, msg.width = image.shape[:2]
            msg.encoding, msg.step = "rgb8", image.shape[1] * 3
            msg.data = image.tobytes()
            self.pubs["image"].publish(msg)

    def decode(self, annexb):
        import av  # imported lazily: only needed once video arrives
        if self.decoder is None:
            self.decoder = av.CodecContext.create("h264", "r")
        try:
            frames = self.decoder.decode(av.Packet(bytes(annexb)))
        except av.AVError:
            return None
        return frames[-1].to_ndarray(format="rgb24") if frames else None

    def publish_mounts(self):
        transforms = []
        now = self.get_clock().now().to_msg()
        for name, (x, y, z) in MOUNTS.items():
            tf = TransformStamped()
            tf.header.stamp, tf.header.frame_id, tf.child_frame_id = now, "base_link", name
            tf.transform.translation.x, tf.transform.translation.y, tf.transform.translation.z = x, y, z
            tf.transform.rotation.w = 1.0
            transforms.append(tf)
        optical = TransformStamped()
        optical.header.stamp, optical.header.frame_id, optical.child_frame_id = now, "camera_link", CAMERA_FRAME
        r = optical.transform.rotation
        r.x, r.y, r.z, r.w = 0.5, -0.5, 0.5, -0.5
        transforms.append(optical)
        self.static_tf.sendTransform(transforms)

    # --- ROS -> robot ---------------------------------------------------

    def command(self, name, forward=0.0, yaw=0.0):
        try:
            self.robot.Command(robot_pb.UnitreeCommandRequest(
                serial=self.serial, command=COMMANDS[name],
                forward=convert.clamp(forward, MAX_FORWARD), yaw=convert.clamp(yaw, MAX_YAW)), timeout=1)
            return True
        except grpc.RpcError as e:
            log.warning("command %s failed: %s", name, e.details())
            return False

    def on_cmd_vel(self, twist):
        if twist.linear.x == 0 and twist.angular.z == 0:
            self.command("stop")
        else:
            self.command("move", twist.linear.x, twist.angular.z)

    def on_sport_request(self, request):
        identity = request.header.identity
        mapped = convert.sport_request(identity.api_id, request.parameter)
        response = Response()
        response.header.identity.id, response.header.identity.api_id = identity.id, identity.api_id
        if mapped is None:
            log.warning("sport API %d is not supported by the Go2 bridge", identity.api_id)
            response.header.status.code = UNSUPPORTED
        else:
            name, forward, yaw = mapped
            response.header.status.code = 0 if self.command(name, forward, yaw) else DENIED
        self.pubs["response"].publish(response)


def find_serial(channel):
    serial = os.environ.get("GO2_SERIAL")
    if serial:
        return serial
    pairing = pairing_grpc.WendyUnitreeServiceStub(channel)
    while True:
        try:
            robots = pairing.ListUnitreePairings(pairing_pb.ListUnitreePairingsRequest(), timeout=5).robots
            if len(robots) == 1:
                return robots[0].serial
            log.warning("found %d paired Go2 robots; set GO2_SERIAL", len(robots))
        except grpc.RpcError as e:
            log.warning("agent unavailable: %s", e.details())
        time.sleep(5)


def main():
    logging.basicConfig(level=logging.INFO, format="%(name)s: %(message)s")
    channel = grpc.insecure_channel("unix:" + os.environ["WENDY_AGENT_SOCKET"])
    serial = find_serial(channel)
    log.info("bridging Go2 %s", serial)
    rclpy.init()
    node = Bridge(channel, serial)
    threading.Thread(target=node.run_stream, daemon=True).start()
    try:
        rclpy.spin(node)
    except (KeyboardInterrupt, ExternalShutdownException):
        pass
    except RCLError:  # a callback raced shutdown
        pass
    finally:
        node.command("stop")
        rclpy.try_shutdown()


if __name__ == "__main__":
    main()
