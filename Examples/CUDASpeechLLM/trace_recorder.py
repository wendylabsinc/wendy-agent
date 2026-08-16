"""Low-overhead, correlated wall-clock and GPU tracing for live voice turns."""

from __future__ import annotations

import collections
import datetime as dt
import json
import os
from pathlib import Path
import shutil
import subprocess
import threading
import time
from typing import Any, Callable


def _utc_iso(unix_ns: int) -> str:
    value = dt.datetime.fromtimestamp(unix_ns / 1_000_000_000, tz=dt.timezone.utc)
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def _number(value: str) -> float | None:
    cleaned = value.strip()
    if cleaned in {"", "N/A", "[N/A]", "Not Supported"}:
        return None
    try:
        return float(cleaned)
    except ValueError:
        return None


class TraceRecorder:
    """Append JSONL trace events and sample NVIDIA telemetry during active turns."""

    GPU_FIELDS = (
        "gpu_name",
        "gpu_utilization_percent",
        "memory_utilization_percent",
        "memory_used_mib",
        "memory_total_mib",
        "power_draw_w",
        "sm_clock_mhz",
    )

    def __init__(
        self,
        path: str,
        *,
        device: str = "spark",
        monotonic_clock: Callable[[], int] = time.monotonic_ns,
        wall_clock: Callable[[], int] = time.time_ns,
        gpu_interval_ms: int = 250,
        max_bytes: int = 128 * 1024 * 1024,
    ) -> None:
        self.path = Path(path)
        self.device = device
        self._monotonic_clock = monotonic_clock
        self._wall_clock = wall_clock
        self._gpu_interval_ms = max(100, gpu_interval_ms)
        self._max_bytes = max(1_048_576, max_bytes)
        self._lock = threading.RLock()
        self._sequence = 0
        self._active_turns: set[str] = set()
        self._gpu_process: subprocess.Popen[str] | None = None
        self._stopping = threading.Event()

    def record(
        self,
        turn_id: str,
        event: str,
        *,
        component: str,
        kind: str = "instant",
        monotonic_ns: int | None = None,
        wall_unix_ns: int | None = None,
        duration_ns: int | None = None,
        details: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        if not turn_id:
            raise ValueError("turn_id is required")
        observed_monotonic_ns = (
            self._monotonic_clock() if monotonic_ns is None else int(monotonic_ns)
        )
        observed_wall_ns = self._wall_clock() if wall_unix_ns is None else int(wall_unix_ns)
        with self._lock:
            self._sequence += 1
            item: dict[str, Any] = {
                "schema": "wendy.modcon.trace.v1",
                "sequence": self._sequence,
                "turn_id": turn_id,
                "device": self.device,
                "clock_domain": f"{self.device}-CLOCK_MONOTONIC",
                "component": component,
                "event": event,
                "kind": kind,
                "wall_time_utc": _utc_iso(observed_wall_ns),
                "wall_unix_ns": observed_wall_ns,
                "monotonic_ns": observed_monotonic_ns,
                "details": details or {},
            }
            if duration_ns is not None:
                item["duration_ns"] = int(duration_ns)
                item["duration_ms"] = int(duration_ns) / 1_000_000
            self._append_locked(item)
        return item

    def activate(self, turn_id: str) -> None:
        with self._lock:
            self._active_turns.add(turn_id)

    def deactivate(self, turn_id: str) -> None:
        with self._lock:
            self._active_turns.discard(turn_id)

    def active_turns(self) -> tuple[str, ...]:
        with self._lock:
            return tuple(sorted(self._active_turns))

    def start_gpu_sampler(self) -> None:
        if self._gpu_process is not None or not shutil.which("nvidia-smi"):
            return
        query = (
            "name,utilization.gpu,utilization.memory,memory.used,memory.total,"
            "power.draw,clocks.sm"
        )
        try:
            process = subprocess.Popen(
                [
                    "nvidia-smi",
                    f"--query-gpu={query}",
                    "--format=csv,noheader,nounits",
                    f"--loop-ms={self._gpu_interval_ms}",
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                bufsize=1,
            )
        except OSError:
            return
        self._gpu_process = process
        threading.Thread(target=self._read_gpu, args=(process,), daemon=True).start()

    def close(self) -> None:
        self._stopping.set()
        process = self._gpu_process
        if process is not None and process.poll() is None:
            process.terminate()

    def recent(self, *, turn_id: str | None = None, limit: int = 1000) -> list[dict[str, Any]]:
        bounded = max(1, min(10_000, int(limit)))
        if not self.path.is_file():
            return []
        selected: collections.deque[dict[str, Any]] = collections.deque(maxlen=bounded)
        try:
            with self.path.open("r", encoding="utf-8") as source:
                for line in source:
                    try:
                        item = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if turn_id is None or item.get("turn_id") == turn_id:
                        selected.append(item)
        except OSError:
            return []
        return list(selected)

    def summarize(self, turn_id: str) -> dict[str, Any]:
        events = self.recent(turn_id=turn_id, limit=10_000)
        durations: dict[str, list[float]] = {}
        gpu_values: dict[str, list[float]] = {
            "gpu_utilization_percent": [],
            "memory_utilization_percent": [],
            "power_draw_w": [],
            "sm_clock_mhz": [],
        }
        for item in events:
            duration = item.get("duration_ms")
            if isinstance(duration, (int, float)):
                durations.setdefault(str(item.get("event")), []).append(float(duration))
            if item.get("event") == "gpu.sample":
                details = item.get("details") or {}
                for key in gpu_values:
                    value = details.get(key)
                    if isinstance(value, (int, float)):
                        gpu_values[key].append(float(value))
        phase_totals_ms = {key: round(sum(values), 3) for key, values in durations.items()}
        def summarize_gpu(values_by_key: dict[str, list[float]]) -> dict[str, dict[str, Any]]:
            return {
                key: {
                    "samples": len(values),
                    "average": round(sum(values) / len(values), 3) if values else None,
                    "peak": round(max(values), 3) if values else None,
                }
                for key, values in values_by_key.items()
            }

        gpu_summary = summarize_gpu(gpu_values)
        inference_intervals = [
            (
                int(item["monotonic_ns"]) - int(item["duration_ns"]),
                int(item["monotonic_ns"]),
            )
            for item in events
            if item.get("event") == "inference.request"
            and isinstance(item.get("monotonic_ns"), int)
            and isinstance(item.get("duration_ns"), int)
        ]
        inference_gpu_values: dict[str, list[float]] = {
            key: [] for key in gpu_values
        }
        for item in events:
            if item.get("event") != "gpu.sample":
                continue
            instant = item.get("monotonic_ns")
            if not isinstance(instant, int) or not any(
                started <= instant <= ended for started, ended in inference_intervals
            ):
                continue
            details = item.get("details") or {}
            for key in inference_gpu_values:
                value = details.get(key)
                if isinstance(value, (int, float)):
                    inference_gpu_values[key].append(float(value))
        inference_gpu_summary = summarize_gpu(inference_gpu_values)

        turn_started_ns = next(
            (
                item.get("monotonic_ns")
                for item in events
                if item.get("event") == "turn.started"
            ),
            None,
        )
        first_audio_ns = next(
            (
                item.get("monotonic_ns")
                for item in events
                if item.get("event") == "audio.playback.started"
            ),
            None,
        )
        time_to_first_audio_ms = (
            (first_audio_ns - turn_started_ns) / 1_000_000
            if isinstance(first_audio_ns, int) and isinstance(turn_started_ns, int)
            else None
        )
        inference_records = [
            item for item in events if item.get("event") == "inference.request"
        ]
        tool_calls = [
            item.get("details", {}).get("tool_call")
            for item in inference_records
            if item.get("details", {}).get("tool_call")
        ]
        generated_audio_ms = sum(
            float(item.get("details", {}).get("audio_duration_ms") or 0)
            for item in events
            if item.get("event") == "tts.synthesis"
        )
        observations: list[str] = []
        inference_ms = sum(
            value for key, value in phase_totals_ms.items() if key == "inference.request"
        )
        first_token = next(
            (
                item.get("details", {}).get("time_to_first_text_ms")
                for item in reversed(events)
                if item.get("event") == "inference.request"
            ),
            None,
        )
        average_gpu = inference_gpu_summary["gpu_utilization_percent"]["average"]
        if len(inference_records) > 1:
            observations.append(
                f"The model required {len(inference_records)} inference passes; the first selected "
                f"tool(s) {tool_calls or ['unknown']}, so a second completion delayed speech."
            )
        if isinstance(first_token, (int, float)) and first_token > 1500:
            observations.append(
                "Most perceived delay before speech is time-to-first-text: prompt ingestion, "
                "audio projection, and model prefill occur before the first speakable sentence."
            )
        if inference_ms and isinstance(average_gpu, (int, float)):
            if average_gpu < 35:
                observations.append(
                    "GPU utilization was low during the inference intervals; CPU-side request, audio "
                    "projection, synchronization, or sampling gaps likely dominate part of latency."
                )
            elif average_gpu > 75:
                observations.append(
                    "GPU utilization was high during the turn, consistent with model prefill/decode "
                    "being a material latency source."
                )
        if phase_totals_ms.get("tts.synthesis", 0) > inference_ms:
            observations.append("Kokoro synthesis took longer than measured model inference.")
        if phase_totals_ms.get("audio.playback", 0) > inference_ms:
            observations.append(
                "Playback duration dominates the completed turn; this is generated speech length, "
                "not inference latency."
            )
        if not observations:
            observations.append(
                "Use the phase durations and GPU samples directly; this turn does not support a "
                "single dominant inference bottleneck classification."
            )
        return {
            "schema": "wendy.modcon.trace-summary.v1",
            "turn_id": turn_id,
            "event_count": len(events),
            "first_wall_time_utc": events[0].get("wall_time_utc") if events else None,
            "last_wall_time_utc": events[-1].get("wall_time_utc") if events else None,
            "phase_totals_ms": phase_totals_ms,
            "time_to_first_audio_ms": (
                round(time_to_first_audio_ms, 3)
                if isinstance(time_to_first_audio_ms, (int, float))
                else None
            ),
            "inference_passes": len(inference_records),
            "tool_calls": tool_calls,
            "generated_audio_ms": round(generated_audio_ms, 3),
            "gpu": gpu_summary,
            "gpu_during_inference": inference_gpu_summary,
            "observations": observations,
        }

    def _read_gpu(self, process: subprocess.Popen[str]) -> None:
        if process.stdout is None:
            return
        while not self._stopping.is_set():
            line = process.stdout.readline()
            if not line:
                return
            values = [part.strip() for part in line.split(",")]
            if len(values) != len(self.GPU_FIELDS):
                continue
            details: dict[str, Any] = {"gpu_name": values[0]}
            for key, value in zip(self.GPU_FIELDS[1:], values[1:]):
                details[key] = _number(value)
            try:
                memory = {
                    line.split(":", 1)[0]: int(line.split()[1]) / 1024
                    for line in Path("/proc/meminfo").read_text(encoding="utf-8").splitlines()
                    if line.startswith(("MemTotal:", "MemAvailable:"))
                }
            except (OSError, ValueError, IndexError):
                memory = {}
            details["unified_memory_total_mib"] = memory.get("MemTotal")
            details["unified_memory_available_mib"] = memory.get("MemAvailable")
            for turn_id in self.active_turns():
                self.record(
                    turn_id,
                    "gpu.sample",
                    component="nvidia-smi",
                    kind="sample",
                    details=details,
                )

    def _append_locked(self, item: dict[str, Any]) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        try:
            if self.path.stat().st_size >= self._max_bytes:
                rotated = self.path.with_suffix(self.path.suffix + ".1")
                try:
                    rotated.unlink()
                except FileNotFoundError:
                    pass
                self.path.replace(rotated)
        except FileNotFoundError:
            pass
        encoded = json.dumps(
            item, sort_keys=True, separators=(",", ":"), default=str
        )
        with self.path.open("a", encoding="utf-8") as output:
            output.write(encoded + "\n")
