#!/usr/bin/env python3
"""
Hello Audio — plays a bundled sound sample through the device's speaker.

Demonstrates:
  - Requesting the `audio` entitlement in wendy.json (grants ALSA/PipeWire
    access via /dev/snd)
  - Enumerating available audio output devices
  - Loading and playing a WAV file with sounddevice + soundfile
"""

import sys
from pathlib import Path

import sounddevice as sd
import soundfile as sf

SECTION = "=" * 60
SAMPLE_PATH = Path(__file__).parent / "sounds" / "gong.wav"


def print_section(title: str) -> None:
    print(f"\n{SECTION}")
    print(f"  {title}")
    print(SECTION)


def check_output_device() -> None:
    print_section("Audio devices")
    devices = sd.query_devices()
    outputs = [d for d in devices if d["max_output_channels"] > 0]
    for d in devices:
        marker = " (output)" if d["max_output_channels"] > 0 else ""
        print(f"  [{d['index']}] {d['name']}{marker}")

    if not outputs:
        print("\nFAIL: No audio output device found.")
        print("  Ensure the 'audio' entitlement is set in wendy.json and")
        print("  that the host has a sound device available.")
        sys.exit(1)

    default_output = sd.default.device[1]
    print(f"  ✓ Using default output device index {default_output}")


def play_sample() -> None:
    print_section("Playing sample")
    print(f"  File: {SAMPLE_PATH.name}")

    data, samplerate = sf.read(SAMPLE_PATH, dtype="float32")
    duration = len(data) / samplerate
    print(f"  Duration: {duration:.1f}s @ {samplerate} Hz")

    sd.play(data, samplerate)
    sd.wait()
    print("  ✓ Playback finished")


def main() -> None:
    print(f"\n{SECTION}")
    print("  Hello Audio — playing a sound sample on WendyOS")
    print(SECTION)

    check_output_device()
    play_sample()

    print(f"\n{SECTION}")
    print("  ✓ Audio playback complete!")
    print(SECTION)
    sys.exit(0)


if __name__ == "__main__":
    main()
