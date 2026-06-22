FROM ros:humble
RUN apt-get update && apt-get install -y --no-install-recommends \
      ros-humble-demo-nodes-cpp ros-humble-rmw-cyclonedds-cpp \
 && rm -rf /var/lib/apt/lists/*
CMD ["bash","-lc","source /opt/ros/humble/setup.bash && exec ros2 run demo_nodes_cpp listener"]
