# Top-level project CMake for the ESP32-S31-Korvo-1 wendy-lite demo.
# Mirrors gmondada/wlite-native-demo1's boilerplate.
cmake_minimum_required(VERSION 3.16)

# WAMR (pulled in transitively via wendy_core -> wendy_wasm) declares
# cmake_minimum_required(VERSION 3.5), which CMake 4.x rejects outright.
# Allow it via the compatibility policy, same as wendy-lite itself does.
set(CMAKE_POLICY_VERSION_MINIMUM 3.5)

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(esp32s31-korvo-demo)
