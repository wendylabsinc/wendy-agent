cmake_minimum_required(VERSION 3.16)

# This example is intentionally tied to the ESP32-C6. A command-line or
# environment-provided IDF_TARGET still takes precedence over this default.
set(IDF_TARGET "esp32c6" CACHE STRING "ESP-IDF target")

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(esp32c6_neopixel)
