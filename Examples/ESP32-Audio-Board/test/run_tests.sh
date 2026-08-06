#!/usr/bin/env bash
# Builds and runs the host-buildable unit tests with the system C compiler —
# no ESP-IDF toolchain needed, since these files have zero ESP-IDF dependency.
set -euo pipefail

cd "$(dirname "$0")"

CC="${CC:-cc}"
"$CC" -std=c11 -Wall -Wextra -O0 -g \
    test_main.c \
    ../main/text_utils.c \
    ../main/weather_codes.c \
    -o /tmp/esp32_audio_board_tests

/tmp/esp32_audio_board_tests
