cmake_minimum_required(VERSION 3.8)
project(wendy_ros2_bridge)

if(NOT CMAKE_CXX_STANDARD)
  set(CMAKE_CXX_STANDARD 17)
endif()
if(CMAKE_COMPILER_IS_GNUCXX OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

find_package(ament_cmake REQUIRED)
find_package(rclcpp REQUIRED)

add_executable(wendy-ros2-bridge src/bridge_main.cpp)
target_include_directories(wendy-ros2-bridge PRIVATE src)
ament_target_dependencies(wendy-ros2-bridge rclcpp)

install(TARGETS wendy-ros2-bridge DESTINATION lib/${PROJECT_NAME})

if(BUILD_TESTING)
  find_package(ament_cmake_gtest REQUIRED)
  ament_add_gtest(test_protocol test/test_protocol.cpp)
  target_include_directories(test_protocol PRIVATE src)
endif()

ament_package()
