#!/usr/bin/env python3
"""Synthesizes assets/sleigh-bells.wav — no downloads, no committed binaries.

Generates a two-second sleigh-bell jingle: a train of bright, quickly
decaying bell strikes (clusters of inharmonic high partials with per-strike
detune and timing jitter). Deterministic (seeded RNG) and stdlib-only, so
the Docker build stays hermetic and reproducible.
"""
import math
import random
import struct
import wave
from pathlib import Path

RATE = 48000
DURATION = 2.0
OUT = Path(__file__).parent / "assets" / "sleigh-bells.wav"

# One shake of a bell cluster every ~125 ms, like bells on a moving strap.
STRIKE_INTERVAL = 0.125
STRIKE_DECAY = 0.075  # seconds to fall to 1/e — small bells ring briefly
PARTIALS = (2400.0, 3250.0, 4150.0, 5300.0)  # inharmonic, jingle-bell bright


def synthesize() -> list[float]:
    rng = random.Random(20260807)
    samples = [0.0] * int(RATE * DURATION)

    t = 0.02
    while t < DURATION - STRIKE_DECAY:
        start = int(t * RATE)
        loudness = rng.uniform(0.5, 1.0)
        # Each strike rings for a few decay constants, then is inaudible.
        length = int(STRIKE_DECAY * 5 * RATE)
        for freq in PARTIALS:
            f = freq * rng.uniform(0.97, 1.03)
            phase = rng.uniform(0.0, 2.0 * math.pi)
            amp = loudness * rng.uniform(0.5, 1.0) / len(PARTIALS)
            for i in range(min(length, len(samples) - start)):
                dt = i / RATE
                samples[start + i] += (
                    amp * math.exp(-dt / STRIKE_DECAY) * math.sin(2.0 * math.pi * f * dt + phase)
                )
        t += STRIKE_INTERVAL * rng.uniform(0.8, 1.2)

    # Fade the tail so the clip never ends on a click, then normalize.
    fade = int(0.15 * RATE)
    for i in range(fade):
        samples[-fade + i] *= 1.0 - (i / fade)
    peak = max(abs(s) for s in samples)
    return [s * 0.8 / peak for s in samples]


def main() -> None:
    OUT.parent.mkdir(parents=True, exist_ok=True)
    samples = synthesize()
    with wave.open(str(OUT), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(
            b"".join(struct.pack("<h", int(s * 32767)) for s in samples)
        )
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes, {DURATION:.1f}s @ {RATE} Hz)")


if __name__ == "__main__":
    main()
