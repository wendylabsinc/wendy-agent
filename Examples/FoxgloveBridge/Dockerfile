# foxglove_bridge as a Wendy app. Runs the upstream ROS 2 node and serves the
# Foxglove WebSocket on :8765. With host networking (see wendy.json) it joins
# the device's ROS 2 graph — including a robot's native host ROS 2 (e.g. Go2).
FROM ros:humble
RUN apt-get update && apt-get install -y --no-install-recommends \
      ros-humble-foxglove-bridge \
      ros-humble-rmw-cyclonedds-cpp \
    && rm -rf /var/lib/apt/lists/*
# Add the robot's custom message packages here so their schemas render, e.g.:
#   ros-humble-unitree-go  (if available via apt)  — otherwise custom types
#   show up but can't be deserialized by Studio.
CMD ["bash","-lc","source /opt/ros/humble/setup.bash && exec ros2 launch foxglove_bridge foxglove_bridge_launch.xml port:=8765 address:=0.0.0.0"]
