#!/usr/bin/env python3
"""Headless ALSA microphone -> Ultravox -> Kokoro -> ALSA voice loop."""

from __future__ import annotations

import array
import base64
import collections
import hashlib
import io
import json
import math
import mimetypes
import os
import queue
import re
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from g1_tools import G1ControlClient, G1ToolError, TOOL_SCHEMAS
from trace_recorder import TraceRecorder


PUBLIC_DIR = Path(os.environ.get("VOICE_WEB_ROOT", "/app/web")).resolve()
VOICE_PORT = int(os.environ.get("VOICE_PORT", "8080"))
LLAMA_PORT = int(os.environ.get("LLAMA_PORT", "8081"))
LLAMA_URL = f"http://127.0.0.1:{LLAMA_PORT}"
MODEL_ALIAS = os.environ.get("MODEL_ALIAS", "ultravox-v0.5-llama-3.1-8b-q4-k-m")
SAMPLE_RATE = 16_000
FRAME_MS = 20
FRAME_BYTES = SAMPLE_RATE * 2 * FRAME_MS // 1000
TTS_MODEL_DIR = Path(os.environ.get("TTS_MODEL_DIR", "/models/kokoro-multi-lang-v1_0"))
TTS_SPEAKER_ID = int(os.environ.get("TTS_SPEAKER_ID", "3"))
TTS_SPEED = float(os.environ.get("TTS_SPEED", "1.04"))
TTS_THREADS = int(os.environ.get("TTS_THREADS", "8"))
ALSA_PLAYBACK_DEVICE = os.environ.get("ALSA_PLAYBACK_DEVICE", "default")
ALSA_PLAYBACK_PREROLL_MS = max(
    0, int(os.environ.get("ALSA_PLAYBACK_PREROLL_MS", "300"))
)
SYSTEM_PROMPT_PATH = Path(os.environ.get("SYSTEM_PROMPT_PATH", "/app/SOUL.md"))
KNOWLEDGE_DIR = Path(os.environ.get("KNOWLEDGE_DIR", "/app/knowledge"))
KNOWLEDGE_MAX_CHARS = int(os.environ.get("KNOWLEDGE_MAX_CHARS", "30000"))
TTS_CHUNK_MIN_CHARS = int(os.environ.get("TTS_CHUNK_MIN_CHARS", "24"))
TTS_CHUNK_QUEUE_SIZE = int(os.environ.get("TTS_CHUNK_QUEUE_SIZE", "8"))
MIN_AUDIO_RMS = float(os.environ.get("MIN_AUDIO_RMS", "0.002"))
RUNAWAY_VOICE_LIMIT = int(os.environ.get("RUNAWAY_VOICE_LIMIT", "3"))
RUNAWAY_VOICE_WINDOW_SECONDS = float(
    os.environ.get("RUNAWAY_VOICE_WINDOW_SECONDS", "10")
)
AUTO_RESUME_COOLDOWN_SECONDS = float(
    os.environ.get("AUTO_RESUME_COOLDOWN_SECONDS", "3")
)
AUTO_RESUME_QUIET_SECONDS = float(
    os.environ.get("AUTO_RESUME_QUIET_SECONDS", "1")
)
AUTO_RESUME_MAX_SECONDS = float(
    os.environ.get("AUTO_RESUME_MAX_SECONDS", "12")
)
TRACE_LOG_PATH = os.environ.get(
    "TRACE_LOG_PATH", "/models/traces/speechllm-trace.jsonl"
)
TRACE_GPU_INTERVAL_MS = int(os.environ.get("TRACE_GPU_INTERVAL_MS", "250"))
AUTO_LISTEN = os.environ.get("AUTO_LISTEN", "true").lower() not in {
    "0",
    "false",
    "no",
    "off",
}

DEFAULT_SYSTEM_PROMPT = (
    "You are a warm, quick voice assistant in a live conversation. Listen "
    "carefully to each audio turn and reply naturally in one to three short "
    "sentences. Do not describe your process, mention transcription, or use "
    "markdown unless the user asks for it."
)


def load_system_prompt(
    soul_path: Path = SYSTEM_PROMPT_PATH,
    knowledge_dir: Path = KNOWLEDGE_DIR,
) -> str:
    """Load Walter's identity and bounded conference reference material."""
    parts: list[str] = []
    try:
        soul = soul_path.read_text(encoding="utf-8").strip()
    except OSError:
        soul = ""
    remaining = max(0, KNOWLEDGE_MAX_CHARS)
    if remaining and knowledge_dir.is_dir():
        for path in sorted(knowledge_dir.glob("*.md")):
            try:
                text = path.read_text(encoding="utf-8").strip()
            except OSError:
                continue
            if not text:
                continue
            excerpt = text[:remaining]
            parts.append(f"Reference: {path.stem}\n{excerpt}")
            remaining -= len(excerpt)
            if remaining <= 0:
                break

    # Put behavioral and identity instructions after reference material so a
    # small local model sees the decision policy closest to the user turn.
    if soul:
        parts.append(f"Final identity and conversation policy:\n{soul}")

    return "\n\n".join(parts) if parts else DEFAULT_SYSTEM_PROMPT


SYSTEM_PROMPT = load_system_prompt()
G1_CLIENT = G1ControlClient.from_env()
TRACE = TraceRecorder(
    TRACE_LOG_PATH,
    device=os.environ.get("TRACE_DEVICE_NAME", "spark-3011"),
    gpu_interval_ms=TRACE_GPU_INTERVAL_MS,
)
TRACE_THREAD = threading.local()


def runtime_system_prompt(
    base_prompt: str = SYSTEM_PROMPT, *, g1_tools_enabled: bool | None = None
) -> str:
    """Describe the live robot boundary without inviting unavailable tool calls."""
    enabled = G1_CLIENT.enabled if g1_tools_enabled is None else g1_tools_enabled
    if enabled:
        return base_prompt
    return (
        f"{base_prompt}\n\n"
        "Live operator state: G1 control is offline and robot tools are disabled. "
        "Do not call, retry, or suggest a G1 task. If asked for robot status or "
        "motion, state the unavailability once in one short sentence, then continue "
        "with non-robot conversation."
    )


class GestureCommandGate:
    """Require current-turn command evidence before a gesture can schedule."""

    _DENIED_TEXT = re.compile(
        r"\b(?:hypothetical|hypothetically|example|explain|describe|discuss|"
        r"what\s+(?:would|will)\s+happen|if\s+(?:someone|somebody|a person)|"
        r"do\s+not|don't|dont|without\s+(?:moving|doing\s+it))\b",
        re.IGNORECASE,
    )
    _DIRECT_TEXT = {
        "raise_hand": re.compile(
            r"\b(?:raise|lift|put)\s+(?:your\s+)?(?:right\s+)?hand(?:\s+up)?\b",
            re.IGNORECASE,
        ),
        "wave_hand": re.compile(
            r"\bwave(?:\s+(?:your\s+)?hand)?(?:\s+(?:to|at)\s+(?:me|us|the\s+audience))?\b",
            re.IGNORECASE,
        ),
        "shake_hand": re.compile(
            r"\b(?:shake\s+(?:my|our|the|your)\s+hand|handshake)\b",
            re.IGNORECASE,
        ),
        "stop": re.compile(
            r"\b(?:stop|cancel|halt|hold\s+still|release)\b",
            re.IGNORECASE,
        ),
    }
    _SPOKEN_DIRECT_TEXT = {
        "raise_hand": re.compile(
            r"^(?:hey\s+)?walter[,\s]+(?:please\s+)?"
            r"(?:raise|lift|put)\s+(?:your\s+)?(?:right\s+)?hand(?:\s+up)?"
            r"(?:\s+(?:now|please))?[.!?]*$",
            re.IGNORECASE,
        ),
        "wave_hand": re.compile(
            r"^(?:hey\s+)?walter[,\s]+(?:please\s+)?wave"
            r"(?:\s+(?:your\s+)?hand)?"
            r"(?:\s+(?:to|at)\s+(?:me|us|the\s+audience))?"
            r"(?:\s+(?:now|please))?[.!?]*$",
            re.IGNORECASE,
        ),
        "shake_hand": re.compile(
            r"^(?:hey\s+)?walter[,\s]+(?:please\s+)?"
            r"(?:shake\s+(?:my|our)\s+hand|give\s+me\s+a\s+handshake|handshake)"
            r"(?:\s+(?:now|please))?[.!?]*$",
            re.IGNORECASE,
        ),
        # A false stop is safer than ignoring an urgent stop, so it does not
        # require the Walter address that motion-starting commands require.
        "stop": re.compile(
            r"^(?:(?:hey\s+)?walter[,\s]+)?(?:please\s+)?"
            r"(?:stop|cancel|halt|hold\s+still|release)"
            r"(?:\s+(?:now|please))?[.!?]*$",
            re.IGNORECASE,
        ),
    }

    @staticmethod
    def _text(user_message: dict) -> str | None:
        content = user_message.get("content")
        return content if isinstance(content, str) else None

    def clear(self) -> None:
        """Retained for reset/interrupt symmetry; direct mode has no armed state."""

    @staticmethod
    def _rejection(error: str) -> tuple[str, dict]:
        return (
            "reject",
            {"success": False, "accepted": False, "error": error},
        )

    def evaluate(
        self,
        user_message: dict,
        action: str,
        *,
        command_text: str | None = None,
    ) -> tuple[str, dict]:
        """Return execute or reject using evidence from only the current turn."""
        text = self._text(user_message)
        if text is not None:
            if self._DENIED_TEXT.search(text):
                return self._rejection(
                    "gesture request was hypothetical, explanatory, or negated"
                )
            pattern = self._DIRECT_TEXT.get(action)
            if pattern is None or pattern.search(text) is None:
                return self._rejection(
                    "gesture was not an explicit direct command in this turn"
                )
            self.clear()
            return "execute", {}

        # A model-selected tool is not sufficient authorization for microphone
        # input. Require the same tool call to carry current-turn command words,
        # and accept only a narrow, addressed imperative that agrees with the
        # selected action. This rejects Walter's declarative TTS, actuator noise,
        # unrelated speech, explanations, and stale/partial tool arguments.
        if not isinstance(action, str):
            return self._rejection("spoken tool selected an invalid action")
        if not isinstance(command_text, str) or not command_text.strip():
            return self._rejection("no current-turn spoken command evidence")
        normalized = " ".join(command_text.split())
        if self._DENIED_TEXT.search(normalized):
            return self._rejection(
                "spoken gesture request was hypothetical, explanatory, or negated"
            )
        pattern = self._SPOKEN_DIRECT_TEXT.get(action)
        if pattern is None or pattern.fullmatch(normalized) is None:
            return self._rejection(
                "spoken command was not an exact addressed imperative for this action"
            )
        self.clear()
        return "execute", {}


GESTURE_GATE = GestureCommandGate()


class VoiceState:
    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.turn_idle = threading.Condition(self.lock)
        self.turn_active = False
        self.active_turn_id: str | None = None
        self.listening = AUTO_LISTEN
        self.speaking = False
        self.generating = False
        self.model_ready = False
        self.phase = "loading"
        self.capture_backend = "detecting"
        self.capture_source = os.environ.get("AUDIO_SOURCE", "default")
        self.level = 0.0
        self.last_error = ""
        self.history: list[dict] = []
        self.recent_spoken_sentences: collections.deque[str] = collections.deque(
            maxlen=12
        )
        self.recent_audio_digests: collections.deque[str] = collections.deque(
            maxlen=12
        )
        self.unusable_voice_turns: collections.deque[float] = collections.deque()
        self.listen_pause_reason = ""
        self.auto_resume_pending = False
        self.auto_resume_not_before = 0.0
        self.auto_resume_force_at = 0.0
        self.auto_resume_quiet_since: float | None = None
        self.auto_resume_turn_id: str | None = None
        self.subscribers: list[queue.Queue] = []
        self.stop = threading.Event()
        self.wake = threading.Event()
        self.cancel_generation = threading.Event()
        self.active_speech_queue: queue.Queue | None = None

    def snapshot(self) -> dict:
        with self.lock:
            now = time.monotonic()
            resume_in_ms = (
                max(0, round((self.auto_resume_not_before - now) * 1000))
                if self.auto_resume_pending
                else 0
            )
            return {
                "listening": self.listening,
                "speaking": self.speaking,
                "generating": self.generating,
                "turn_active": self.turn_active,
                "active_turn_id": self.active_turn_id,
                "model_ready": self.model_ready,
                "phase": self.phase,
                "capture_backend": self.capture_backend,
                "capture_source": self.capture_source,
                "level": self.level,
                "model": MODEL_ALIAS,
                "g1_tools_enabled": G1_CLIENT.enabled,
                "trace_log_path": TRACE_LOG_PATH,
                "listen_pause_reason": self.listen_pause_reason,
                "auto_resume_pending": self.auto_resume_pending,
                "auto_resume_in_ms": resume_in_ms,
                "last_error": self.last_error,
            }

    def publish(self, event: str, data: dict | None = None) -> None:
        payload = {"event": event, "data": data or {}, "state": self.snapshot()}
        with self.lock:
            subscribers = list(self.subscribers)
        for subscriber in subscribers:
            try:
                subscriber.put_nowait(payload)
            except queue.Full:
                try:
                    subscriber.get_nowait()
                    subscriber.put_nowait(payload)
                except (queue.Empty, queue.Full):
                    pass

    def set_phase(self, phase: str, error: str = "") -> None:
        with self.lock:
            self.phase = phase
            self.last_error = error
        self.publish("state")


STATE = VoiceState()
TTS_ENGINE = None
TTS_MODULE = None
TTS_INIT_LOCK = threading.Lock()
TTS_GENERATE_LOCK = threading.Lock()
PLAYBACK_LOCK = threading.Lock()
PLAYBACK_PROCESS: subprocess.Popen | None = None


class RepeatedFirstSentenceError(RuntimeError):
    """The model tried to start a microphone reply with recent spoken output."""

    def __init__(self, sentence: str) -> None:
        super().__init__("model repeated a recently spoken first sentence")
        self.sentence = sentence


def _trace_turn() -> str | None:
    value = getattr(TRACE_THREAD, "turn_id", None)
    if value:
        return str(value)
    with STATE.lock:
        return STATE.active_turn_id


def _trace_event(
    event: str,
    *,
    component: str,
    turn_id: str | None = None,
    monotonic_ns: int | None = None,
    duration_ns: int | None = None,
    details: dict | None = None,
) -> None:
    current = turn_id or _trace_turn()
    if not current:
        return
    try:
        TRACE.record(
            current,
            event,
            component=component,
            monotonic_ns=monotonic_ns,
            duration_ns=duration_ns,
            details=details,
        )
    except OSError as exc:
        print(f"Trace write failed: {exc}", flush=True)


def api_ready(timeout: float = 0.7) -> bool:
    try:
        with urllib.request.urlopen(f"{LLAMA_URL}/health", timeout=timeout) as response:
            return response.status == 200
    except (OSError, urllib.error.URLError):
        return False


def wav_bytes(raw_pcm: bytes) -> bytes:
    output = io.BytesIO()
    with wave.open(output, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(SAMPLE_RATE)
        wav.writeframes(raw_pcm)
    return output.getvalue()


def samples_to_wav(samples, sample_rate: int) -> bytes:
    pcm = array.array(
        "h",
        (
            int(max(-1.0, min(1.0, float(sample))) * 32767)
            for sample in samples
        ),
    )
    if sys.byteorder != "little":
        pcm.byteswap()
    output = io.BytesIO()
    with wave.open(output, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(sample_rate)
        wav.writeframes(pcm.tobytes())
    return output.getvalue()


def add_wav_preroll(wav_data: bytes, preroll_ms: int) -> bytes:
    """Prepend silence so a newly opened USB playback path cannot eat speech."""
    if preroll_ms <= 0:
        return wav_data

    source = io.BytesIO(wav_data)
    with wave.open(source, "rb") as wav:
        channels = wav.getnchannels()
        sample_width = wav.getsampwidth()
        sample_rate = wav.getframerate()
        compression = wav.getcomptype()
        compression_name = wav.getcompname()
        frames = wav.readframes(wav.getnframes())

    silent_frame_count = round(sample_rate * preroll_ms / 1000)
    silent_sample = b"\x80" if sample_width == 1 else b"\x00" * sample_width
    silence = silent_sample * channels * silent_frame_count

    output = io.BytesIO()
    with wave.open(output, "wb") as wav:
        wav.setnchannels(channels)
        wav.setsampwidth(sample_width)
        wav.setframerate(sample_rate)
        wav.setcomptype(compression, compression_name)
        wav.writeframes(silence + frames)
    return output.getvalue()


def kokoro_engine():
    global TTS_ENGINE, TTS_MODULE
    if TTS_ENGINE is not None:
        return TTS_ENGINE, TTS_MODULE
    with TTS_INIT_LOCK:
        if TTS_ENGINE is not None:
            return TTS_ENGINE, TTS_MODULE
        import sherpa_onnx

        config = sherpa_onnx.OfflineTtsConfig(
            model=sherpa_onnx.OfflineTtsModelConfig(
                kokoro=sherpa_onnx.OfflineTtsKokoroModelConfig(
                    model=str(TTS_MODEL_DIR / "model.onnx"),
                    voices=str(TTS_MODEL_DIR / "voices.bin"),
                    tokens=str(TTS_MODEL_DIR / "tokens.txt"),
                    data_dir=str(TTS_MODEL_DIR / "espeak-ng-data"),
                    lexicon=str(TTS_MODEL_DIR / "lexicon-us-en.txt"),
                ),
                provider="cpu",
                num_threads=TTS_THREADS,
                debug=False,
            ),
            max_num_sentences=1,
        )
        if not config.validate():
            raise RuntimeError("Kokoro model configuration is invalid")
        TTS_ENGINE = sherpa_onnx.OfflineTts(config)
        TTS_MODULE = sherpa_onnx
        print(
            f"Kokoro TTS ready (af_heart, {TTS_THREADS} CPU threads)",
            flush=True,
        )
    return TTS_ENGINE, TTS_MODULE


def synthesize_speech(text: str) -> bytes:
    engine, sherpa_onnx = kokoro_engine()
    generation = sherpa_onnx.GenerationConfig()
    generation.sid = TTS_SPEAKER_ID
    generation.speed = TTS_SPEED
    generation.silence_scale = 0.18
    started_ns = time.monotonic_ns()
    _trace_event(
        "tts.synthesis.started",
        component="kokoro",
        monotonic_ns=started_ns,
        details={"characters": len(text), "chunk_index": getattr(TRACE_THREAD, "chunk_index", None)},
    )
    try:
        with TTS_GENERATE_LOCK:
            audio = engine.generate(text, generation)
    except Exception as exc:
        ended_ns = time.monotonic_ns()
        _trace_event(
            "tts.synthesis.failed",
            component="kokoro",
            monotonic_ns=ended_ns,
            duration_ns=ended_ns - started_ns,
            details={"error": str(exc)},
        )
        raise
    if len(audio.samples) == 0:
        raise RuntimeError("Kokoro produced no audio")
    duration = len(audio.samples) / audio.sample_rate
    ended_ns = time.monotonic_ns()
    elapsed = (ended_ns - started_ns) / 1_000_000_000
    _trace_event(
        "tts.synthesis",
        component="kokoro",
        monotonic_ns=ended_ns,
        duration_ns=ended_ns - started_ns,
        details={
            "characters": len(text),
            "audio_duration_ms": duration * 1000,
            "realtime_factor": elapsed / duration,
            "chunk_index": getattr(TRACE_THREAD, "chunk_index", None),
            "threads": TTS_THREADS,
        },
    )
    print(
        f"Kokoro synthesized {duration:.2f}s in {elapsed:.2f}s "
        f"(RTF {elapsed / duration:.2f})",
        flush=True,
    )
    return samples_to_wav(audio.samples, audio.sample_rate)


def prewarm_tts() -> None:
    """Load Kokoro during startup so the first spoken turn is low latency."""
    try:
        kokoro_engine()
    except Exception as exc:
        print(f"Kokoro TTS prewarm failed: {exc}", flush=True)


def play_wav_alsa(wav: bytes) -> None:
    """Play a complete WAV through ALSA, without a browser audio stack."""
    global PLAYBACK_PROCESS
    if not shutil.which("aplay"):
        raise RuntimeError("aplay is not installed")

    playback_wav = add_wav_preroll(wav, ALSA_PLAYBACK_PREROLL_MS)
    started_ns = time.monotonic_ns()
    process = subprocess.Popen(
        ["aplay", "-q", "-D", ALSA_PLAYBACK_DEVICE, "-t", "wav"],
        stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    STATE.publish(
        "playback_start",
        {"device": ALSA_PLAYBACK_DEVICE, "monotonic_ns": started_ns},
    )
    _trace_event(
        "audio.playback.started",
        component="alsa",
        monotonic_ns=started_ns,
        details={
            "device": ALSA_PLAYBACK_DEVICE,
            "wav_bytes": len(playback_wav),
            "source_wav_bytes": len(wav),
            "preroll_ms": ALSA_PLAYBACK_PREROLL_MS,
            "chunk_index": getattr(TRACE_THREAD, "chunk_index", None),
        },
    )
    with PLAYBACK_LOCK:
        PLAYBACK_PROCESS = process
    try:
        _, stderr = process.communicate(input=playback_wav)
    finally:
        with PLAYBACK_LOCK:
            if PLAYBACK_PROCESS is process:
                PLAYBACK_PROCESS = None
    ended_ns = time.monotonic_ns()
    _trace_event(
        "audio.playback",
        component="alsa",
        monotonic_ns=ended_ns,
        duration_ns=ended_ns - started_ns,
        details={
            "device": ALSA_PLAYBACK_DEVICE,
            "return_code": process.returncode,
            "cancelled": STATE.cancel_generation.is_set(),
            "chunk_index": getattr(TRACE_THREAD, "chunk_index", None),
        },
    )
    if process.returncode:
        detail = stderr.decode("utf-8", "replace").strip()
        raise RuntimeError(
            f"ALSA playback failed on {ALSA_PLAYBACK_DEVICE}"
            + (f": {detail}" if detail else "")
        )


def speak_alsa(text: str, before_playback=None) -> None:
    wav = synthesize_speech(text)
    if STATE.cancel_generation.is_set():
        return
    if before_playback is not None:
        before_playback()
    if STATE.cancel_generation.is_set():
        return
    started = time.monotonic()
    try:
        play_wav_alsa(wav)
    except RuntimeError:
        if STATE.cancel_generation.is_set():
            return
        raise
    print(
        f"ALSA playback completed on {ALSA_PLAYBACK_DEVICE} "
        f"in {time.monotonic() - started:.2f}s",
        flush=True,
    )


def stop_playback() -> None:
    with PLAYBACK_LOCK:
        process = PLAYBACK_PROCESS
    if process is not None and process.poll() is None:
        _trace_event(
            "audio.playback.interrupt_requested",
            component="alsa",
            details={"pid": process.pid},
        )
        process.terminate()


def discard_queued_speech(speech_queue: queue.Queue | None) -> int:
    """Remove synthesized-but-not-started speech after an interruption."""
    if speech_queue is None:
        return 0
    dropped = 0
    saw_sentinel = False
    while True:
        try:
            item = speech_queue.get_nowait()
        except queue.Empty:
            if saw_sentinel:
                # The worker still needs its end-of-stream marker; dropping it
                # here would leave generate_reply blocked in join forever.
                speech_queue.put_nowait(None)
            return dropped
        if item is None:
            saw_sentinel = True
        else:
            dropped += 1


def interrupt_for_barge_in(
    *, monotonic_ns: int | None = None, next_turn_id: str | None = None
) -> bool:
    """Cancel the active reply immediately on a confirmed microphone onset."""
    with STATE.lock:
        if not (STATE.turn_active or STATE.generating or STATE.speaking):
            return False
        interrupted_turn_id = STATE.active_turn_id
        STATE.cancel_generation.set()
        speech_queue = STATE.active_speech_queue
    stop_playback()
    dropped = discard_queued_speech(speech_queue)
    STATE.publish(
        "barge_in",
        {
            "monotonic_ns": time.monotonic_ns()
            if monotonic_ns is None
            else monotonic_ns,
            "queued_chunks_discarded": dropped,
        },
    )
    if interrupted_turn_id:
        _trace_event(
            "conversation.barge_in",
            component="vad",
            turn_id=interrupted_turn_id,
            monotonic_ns=monotonic_ns,
            details={
                "next_turn_id": next_turn_id,
                "queued_chunks_discarded": dropped,
            },
        )
    return True


def normalized_rms(frame: bytes) -> float:
    samples = array.array("h")
    samples.frombytes(frame[: len(frame) - (len(frame) % 2)])
    if not samples:
        return 0.0
    total = sum(sample * sample for sample in samples)
    return min(1.0, math.sqrt(total / len(samples)) / 32768.0)


def rescan_capture_devices() -> list[str]:
    """Return the configured source followed by currently attached hardware."""
    configured = os.environ.get("ALSA_CAPTURE_DEVICE", "default")
    devices = [configured]
    try:
        result = subprocess.run(
            ["arecord", "-l"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=2,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return devices

    for line in result.stdout.splitlines():
        match = re.match(r"^card\s+(\d+):.*?,\s+device\s+(\d+):", line)
        if not match:
            continue
        device = f"plughw:{match.group(1)},{match.group(2)}"
        if device not in devices:
            devices.append(device)
    print(f"ALSA capture rescan: {', '.join(devices)}", flush=True)
    return devices


def start_capture() -> subprocess.Popen | None:
    if not shutil.which("arecord"):
        return None
    for capture_device in rescan_capture_devices():
        try:
            process = subprocess.Popen(
                [
                    "arecord",
                    "-q",
                    "-D",
                    capture_device,
                    "-f",
                    "S16_LE",
                    "-r",
                    str(SAMPLE_RATE),
                    "-c",
                    "1",
                    "-t",
                    "raw",
                    "-",
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                bufsize=0,
            )
        except OSError:
            continue
        time.sleep(0.15)
        if process.poll() is not None:
            process.wait()
            continue
        with STATE.lock:
            STATE.capture_backend = "ALSA"
            STATE.capture_source = capture_device
            STATE.last_error = ""
        STATE.publish("source", {"backend": "ALSA", "source": capture_device})
        return process
    return None


def should_capture() -> bool:
    with STATE.lock:
        # The PowerConf remains open while Walter thinks and speaks so a human
        # voice can interrupt without waiting for the current turn to finish.
        # During an automatic runaway cooldown it also stays open for level-only
        # observation so quiet can re-arm listening without a process restart.
        return STATE.model_ready and (
            STATE.listening or STATE.auto_resume_pending
        )


def should_begin_audio_turn() -> bool:
    """Do not let room noise supersede a turn that has not started speaking."""
    with STATE.lock:
        return (
            not STATE.auto_resume_pending
            and (not STATE.generating or STATE.speaking)
        )


def observe_auto_resume(
    level: float,
    threshold: float,
    *,
    now: float | None = None,
) -> bool:
    """Re-arm a runaway pause after quiet, with a bounded forced retry.

    Inference stays disabled while this function observes microphone levels.
    Sustained quiet is preferred so room noise does not immediately create
    another empty turn. The maximum timeout guarantees that an unlucky noisy
    sample can never leave the conference demo permanently paused.
    """
    timestamp = time.monotonic() if now is None else now
    resumed_by = ""
    resume_turn_id: str | None = None
    with STATE.lock:
        if not STATE.auto_resume_pending:
            return False
        quiet = level < threshold * 0.72
        if quiet:
            if STATE.auto_resume_quiet_since is None:
                STATE.auto_resume_quiet_since = timestamp
        else:
            STATE.auto_resume_quiet_since = None
        quiet_ready = (
            STATE.auto_resume_quiet_since is not None
            and timestamp >= STATE.auto_resume_not_before
            and timestamp - STATE.auto_resume_quiet_since
            >= AUTO_RESUME_QUIET_SECONDS
        )
        forced = timestamp >= STATE.auto_resume_force_at
        if not quiet_ready and not forced:
            return False
        resumed_by = "quiet" if quiet_ready else "maximum_cooldown"
        resume_turn_id = STATE.auto_resume_turn_id
        STATE.listening = True
        STATE.phase = "listening"
        STATE.listen_pause_reason = ""
        STATE.unusable_voice_turns.clear()
        STATE.auto_resume_pending = False
        STATE.auto_resume_not_before = 0.0
        STATE.auto_resume_force_at = 0.0
        STATE.auto_resume_quiet_since = None
        STATE.auto_resume_turn_id = None
    _trace_event(
        "listening.auto_resumed",
        component="speechllm",
        turn_id=resume_turn_id,
        details={"reason": resumed_by, "level": level, "threshold": threshold},
    )
    STATE.wake.set()
    STATE.publish("listening_auto_resumed", {"reason": resumed_by})
    return True


def capture_loop() -> None:
    pre_roll: collections.deque[bytes] = collections.deque(maxlen=15)
    process: subprocess.Popen | None = None
    buffer = bytearray()
    utterance: list[bytes] = []
    utterance_turn_id: str | None = None
    utterance_started_ns: int | None = None
    in_speech = False
    hot_frames = 0
    silent_frames = 0
    noise_floor = 0.006
    calibration_frames = 75
    last_level_publish = 0.0

    while not STATE.stop.is_set():
        if not should_capture():
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    process.kill()
                process = None
            STATE.wake.wait(0.2)
            STATE.wake.clear()
            continue

        if process is None:
            process = start_capture()
            if process is None:
                STATE.set_phase("error", "No ALSA capture source is available")
                time.sleep(2)
                continue
            with STATE.lock:
                starting_in_cooldown = STATE.auto_resume_pending
            STATE.set_phase("cooldown" if starting_in_cooldown else "listening")

        assert process.stdout is not None
        try:
            chunk = os.read(process.stdout.fileno(), FRAME_BYTES * 4)
        except OSError:
            chunk = b""
        if not chunk:
            process.terminate()
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            process = None
            buffer.clear()
            pre_roll.clear()
            utterance = []
            utterance_turn_id = None
            utterance_started_ns = None
            in_speech = False
            hot_frames = 0
            silent_frames = 0
            calibration_frames = 75
            STATE.set_phase("error", "ALSA capture failed; rescanning audio devices")
            continue
        buffer.extend(chunk)

        while len(buffer) >= FRAME_BYTES:
            frame = bytes(buffer[:FRAME_BYTES])
            del buffer[:FRAME_BYTES]
            level = normalized_rms(frame)
            now = time.monotonic()
            with STATE.lock:
                STATE.level = level
            if now - last_level_publish >= 0.08:
                STATE.publish("level", {"level": level})
                last_level_publish = now

            if calibration_frames > 0:
                noise_floor = noise_floor * 0.9 + level * 0.1
                calibration_frames -= 1
                if calibration_frames == 74:
                    with STATE.lock:
                        calibrating_in_cooldown = STATE.auto_resume_pending
                    STATE.set_phase(
                        "cooldown" if calibrating_in_cooldown else "calibrating"
                    )
                if calibration_frames == 0:
                    with STATE.lock:
                        calibrated_in_cooldown = STATE.auto_resume_pending
                    STATE.set_phase(
                        "cooldown" if calibrated_in_cooldown else "listening"
                    )
                pre_roll.clear()
                continue

            threshold = max(0.012, noise_floor * 1.8)
            with STATE.lock:
                auto_resume_pending = STATE.auto_resume_pending
            if auto_resume_pending:
                # Track a bounded room-noise estimate while inference is gated.
                # Clipping prevents nearby speech from teaching the detector that
                # a human voice is background noise.
                noise_floor = noise_floor * 0.96 + min(level, 0.025) * 0.04
                observe_auto_resume(level, max(0.012, noise_floor * 1.8), now=now)
                hot_frames = 0
                pre_roll.clear()
                continue
            if not in_speech:
                if not should_begin_audio_turn():
                    hot_frames = 0
                    pre_roll.clear()
                    continue
                if level < threshold:
                    noise_floor = noise_floor * 0.96 + min(level, 0.025) * 0.04
                pre_roll.append(frame)
                hot_frames = hot_frames + 1 if level >= threshold else 0
                if hot_frames >= 3:
                    in_speech = True
                    silent_frames = 0
                    utterance = list(pre_roll)
                    onset_confirmed_ns = time.monotonic_ns()
                    utterance_turn_id = f"voice-{uuid.uuid4().hex}"
                    utterance_started_ns = onset_confirmed_ns - (
                        len(utterance) * FRAME_MS * 1_000_000
                    )
                    _trace_event(
                        "input.speech.started",
                        component="powerconf-vad",
                        turn_id=utterance_turn_id,
                        monotonic_ns=utterance_started_ns,
                        details={
                            "onset_confirmed_ns": onset_confirmed_ns,
                            "confirmation_frames": 3,
                            "frame_ms": FRAME_MS,
                            "threshold": threshold,
                            "level": level,
                        },
                    )
                    interrupt_for_barge_in(
                        monotonic_ns=onset_confirmed_ns,
                        next_turn_id=utterance_turn_id,
                    )
                    STATE.set_phase("hearing")
            else:
                utterance.append(frame)
                silent_frames = silent_frames + 1 if level < threshold * 0.72 else 0
                too_long = len(utterance) >= 30_000 // FRAME_MS
                end_of_turn = silent_frames >= 45 and len(utterance) >= 20
                if too_long or end_of_turn:
                    trailing = silent_frames if end_of_turn else 0
                    usable = utterance[:-trailing] if trailing else utterance
                    duration = len(usable) * FRAME_MS / 1000
                    if duration >= 0.35:
                        raw = b"".join(usable)
                        input_ended_ns = time.monotonic_ns()
                        input_started_ns = utterance_started_ns or (
                            input_ended_ns - int(duration * 1_000_000_000)
                        )
                        turn_id = utterance_turn_id or f"voice-{uuid.uuid4().hex}"
                        _trace_event(
                            "input.speech.ended",
                            component="powerconf-vad",
                            turn_id=turn_id,
                            monotonic_ns=input_ended_ns,
                            duration_ns=input_ended_ns - input_started_ns,
                            details={
                                "audio_duration_ms": duration * 1000,
                                "pcm_bytes": len(raw),
                                "end_silence_frames": trailing,
                            },
                        )
                        threading.Thread(
                            target=generate_audio_reply,
                            args=(
                                raw,
                                duration,
                                input_started_ns,
                                input_ended_ns,
                                turn_id,
                            ),
                            daemon=True,
                        ).start()
                    in_speech = False
                    hot_frames = 0
                    silent_frames = 0
                    utterance = []
                    utterance_turn_id = None
                    utterance_started_ns = None
                    pre_roll.clear()
                    break


def trim_history() -> None:
    with STATE.lock:
        if len(STATE.history) > 6:
            STATE.history = STATE.history[-6:]


def canonical_sentence(text: str) -> str:
    """Normalize a spoken sentence for deterministic replay detection."""
    return " ".join(re.findall(r"\w+", text.casefold(), flags=re.UNICODE))


def replay_guard_sentences(state: VoiceState) -> set[str]:
    """Return recent assistant sentences that a new mic turn must not replay."""
    blocked = {
        canonical_sentence(sentence) for sentence in state.recent_spoken_sentences
    }
    for message in state.history:
        if message.get("role") != "assistant":
            continue
        content = message.get("content")
        if not isinstance(content, str):
            continue
        chunks, _ = extract_tts_chunks(content, final=True)
        blocked.update(canonical_sentence(chunk) for chunk in chunks)
    blocked.discard("")
    return blocked


VOICE_BLOCKED_FIRST_SENTENCES = {
    canonical_sentence("What do you need it for?"),
    canonical_sentence("What kind of implementation do you want to use it for?"),
    canonical_sentence(
        "Are you interested in the architecture, deployment workflow, or robot behavior?"
    ),
}


def record_unusable_voice_turn(
    turn_id: str,
    reason: str,
    *,
    now: float | None = None,
) -> bool:
    """Enter a self-clearing cooldown instead of an unbounded inference loop."""
    timestamp = time.monotonic() if now is None else now
    with STATE.lock:
        cutoff = timestamp - RUNAWAY_VOICE_WINDOW_SECONDS
        while STATE.unusable_voice_turns and STATE.unusable_voice_turns[0] < cutoff:
            STATE.unusable_voice_turns.popleft()
        STATE.unusable_voice_turns.append(timestamp)
        count = len(STATE.unusable_voice_turns)
        paused = count >= RUNAWAY_VOICE_LIMIT
        if paused:
            STATE.listening = False
            STATE.phase = "cooldown"
            STATE.listen_pause_reason = "repeated_unusable_voice_turns"
            STATE.auto_resume_pending = True
            STATE.auto_resume_not_before = (
                timestamp + AUTO_RESUME_COOLDOWN_SECONDS
            )
            STATE.auto_resume_force_at = timestamp + AUTO_RESUME_MAX_SECONDS
            STATE.auto_resume_quiet_since = None
            STATE.auto_resume_turn_id = turn_id
    if paused:
        _trace_event(
            "listening.auto_paused",
            component="speechllm",
            turn_id=turn_id,
            details={
                "reason": reason,
                "turn_count": count,
                "minimum_cooldown_seconds": AUTO_RESUME_COOLDOWN_SECONDS,
                "quiet_seconds": AUTO_RESUME_QUIET_SECONDS,
                "maximum_cooldown_seconds": AUTO_RESUME_MAX_SECONDS,
            },
        )
        STATE.wake.set()
        STATE.publish(
            "listening_auto_paused",
            {"reason": reason, "turn_count": count, "turn_id": turn_id},
        )
    return paused


def remember_spoken_sentence(sentence: str) -> None:
    if not canonical_sentence(sentence):
        return
    with STATE.lock:
        STATE.recent_spoken_sentences.append(sentence)


def accept_audio_turn(raw_pcm: bytes, duration: float) -> tuple[bool, str, float]:
    """Reject empty, silent, or byte-identical stale microphone submissions."""
    rms = normalized_rms(raw_pcm) if raw_pcm else 0.0
    if duration < 0.35 or len(raw_pcm) < FRAME_BYTES:
        return False, "too_short", rms
    if rms < MIN_AUDIO_RMS:
        return False, "silent", rms
    digest = hashlib.sha256(raw_pcm).hexdigest()
    with STATE.lock:
        if digest in STATE.recent_audio_digests:
            return False, "duplicate_pcm", rms
        STATE.recent_audio_digests.append(digest)
    return True, "accepted", rms


def retain_history_turn(
    history: list[dict], user_message: dict, reply: str, action: str | None
) -> None:
    """Retain context without ever replaying a prior microphone command."""
    if action is not None:
        # A completed action turn is intentionally non-conversational state.
        # Clear both its raw audio and any earlier action-biased history so a
        # later utterance can never inherit or replay the command.
        history.clear()
        return
    content = user_message.get("content")
    if isinstance(content, list) and any(
        isinstance(item, dict) and item.get("type") == "input_audio"
        for item in content
    ):
        # Ultravox does not expose an authenticated transcript. Keeping only the
        # assistant half of a voice exchange anchors later audio to Walter's own
        # topic. Voice turns stay stateless until both sides can be retained.
        return
    history.extend([user_message, {"role": "assistant", "content": reply}])


def extract_tts_chunks(buffer: str, final: bool = False) -> tuple[list[str], str]:
    """Split streamed model text into speakable sentence-sized chunks."""
    chunks: list[str] = []
    pending = buffer
    while pending:
        match = re.match(r'^(.+?[.!?]+["\']?)(?=\s|$)', pending, re.DOTALL)
        if match is None:
            break
        sentence = match.group(1).strip()
        pending = pending[match.end() :].lstrip()
        if sentence:
            if len(sentence) < TTS_CHUNK_MIN_CHARS and pending and chunks:
                chunks[-1] = f"{chunks[-1]} {sentence}"
            elif len(sentence) < TTS_CHUNK_MIN_CHARS and pending:
                next_match = re.match(
                    r'^(.+?[.!?]+["\']?)(?=\s|$)', pending, re.DOTALL
                )
                if next_match is None:
                    pending = f"{sentence} {pending}".strip()
                    break
                chunks.append(f"{sentence} {next_match.group(1).strip()}")
                pending = pending[next_match.end() :].lstrip()
            else:
                chunks.append(sentence)
    if final and pending.strip():
        chunks.append(pending.strip())
        pending = ""
    return chunks, pending


def stream_speech_worker(
    speech_queue: queue.Queue,
    errors: list[str],
    action_context: dict,
) -> None:
    chunk_index = 0
    TRACE_THREAD.turn_id = str(action_context["turn_id"])
    while True:
        chunk = speech_queue.get()
        if chunk is None:
            break
        if STATE.cancel_generation.is_set():
            continue
        chunk_index += 1
        TRACE_THREAD.chunk_index = chunk_index
        _trace_event(
            "speech.chunk.started",
            component="speech-worker",
            details={"chunk_index": chunk_index, "characters": len(chunk)},
        )
        with STATE.lock:
            STATE.speaking = True
            STATE.phase = "speaking"
        STATE.wake.set()
        STATE.publish(
            "speech_chunk_start",
            {"index": chunk_index, "text": chunk},
        )
        print(
            f"Streaming TTS chunk {chunk_index}: {len(chunk)} characters",
            flush=True,
        )
        try:
            before_playback = None
            if action_context.get("action") and not action_context.get("attempted"):
                action_context["attempted"] = True

                def schedule_action() -> None:
                    turn_id = str(action_context["turn_id"])
                    action = str(action_context["action"])
                    schedule_started_ns = time.monotonic_ns()
                    _trace_event(
                        "g1.schedule.started",
                        component="spark-orchestrator",
                        turn_id=turn_id,
                        monotonic_ns=schedule_started_ns,
                        details={"action": action},
                    )
                    plan = G1_CLIENT.schedule(turn_id, action)
                    schedule_ended_ns = time.monotonic_ns()
                    _trace_event(
                        "g1.schedule",
                        component="spark-orchestrator",
                        turn_id=turn_id,
                        monotonic_ns=schedule_ended_ns,
                        duration_ns=schedule_ended_ns - schedule_started_ns,
                        details={"action": action, "plan": plan},
                    )
                    action_context["plan"] = plan
                    prepare_wait_started_ns = time.monotonic_ns()
                    prepared = G1_CLIENT.wait_until_prepared(turn_id, plan)
                    prepare_wait_ended_ns = time.monotonic_ns()
                    _trace_event(
                        "g1.prepare_wait",
                        component="g1-sync-gateway",
                        turn_id=turn_id,
                        monotonic_ns=prepare_wait_ended_ns,
                        duration_ns=prepare_wait_ended_ns - prepare_wait_started_ns,
                        details={"action": action, "timing": prepared},
                    )
                    action_context["prepared"] = prepared
                    playback_wait_started_ns = time.monotonic_ns()
                    G1_CLIENT.wait_until_playback(plan)
                    playback_wait_ended_ns = time.monotonic_ns()
                    _trace_event(
                        "g1.playback_alignment_wait",
                        component="spark-orchestrator",
                        turn_id=turn_id,
                        monotonic_ns=playback_wait_ended_ns,
                        duration_ns=playback_wait_ended_ns - playback_wait_started_ns,
                        details={"action": action},
                    )
                    G1_CLIENT.record_event(turn_id, "output.speech_started")
                    action_context["scheduled"] = True
                    STATE.publish(
                        "g1_action_scheduled",
                        {
                            "turn_id": turn_id,
                            "action": action,
                            "g1_execute_at_ns": plan.get("g1_execute_at_ns"),
                        },
                    )

                before_playback = schedule_action
            if before_playback is None:
                speak_alsa(chunk)
            else:
                speak_alsa(chunk, before_playback=before_playback)
            # Record output even if barge-in terminated playback partway through.
            # The next mic turn waits for this worker to finish, so an acoustic
            # echo cannot race ahead of the replay guard.
            remember_spoken_sentence(chunk)
        except G1ToolError as exc:
            error = f"G1 action scheduling failed: {exc}"
            errors.append(error)
            action_context["error"] = str(exc)
            print(error, flush=True)
            turn_id = action_context.get("turn_id")
            if action_context.get("plan") and turn_id:
                try:
                    G1_CLIENT.cancel(str(turn_id))
                except G1ToolError:
                    pass
            try:
                failure = "I couldn't safely start that gesture. Please check the G1 status."
                wav = synthesize_speech(failure)
                play_wav_alsa(wav)
            except Exception as speech_error:
                print(f"G1 failure announcement failed: {speech_error}", flush=True)
            STATE.cancel_generation.set()
            stop_playback()
        except Exception as exc:
            error = f"Kokoro/ALSA output failed: {exc}"
            errors.append(error)
            print(error, flush=True)
            STATE.cancel_generation.set()
            stop_playback()

        _trace_event(
            "speech.chunk.finished",
            component="speech-worker",
            details={
                "chunk_index": chunk_index,
                "cancelled": STATE.cancel_generation.is_set(),
            },
        )

    with STATE.lock:
        STATE.speaking = False
        STATE.phase = (
            "thinking"
            if STATE.generating
            else (
                "cooldown"
                if STATE.auto_resume_pending
                else "paused"
                if STATE.listen_pause_reason
                else ("listening" if STATE.listening else "ready")
            )
        )
    STATE.wake.set()
    TRACE_THREAD.turn_id = None
    TRACE_THREAD.chunk_index = None


def generate_audio_reply(
    raw_pcm: bytes,
    duration: float,
    input_started_ns: int | None = None,
    input_ended_ns: int | None = None,
    turn_id: str | None = None,
) -> None:
    turn_id = turn_id or f"voice-{uuid.uuid4().hex}"
    accepted, reason, rms = accept_audio_turn(raw_pcm, duration)
    if not accepted:
        _trace_event(
            "input.audio.dropped",
            component="speechllm",
            turn_id=turn_id,
            details={
                "reason": reason,
                "duration_ms": duration * 1000,
                "pcm_bytes": len(raw_pcm),
                "rms": rms,
            },
        )
        STATE.publish(
            "input_ignored",
            {"kind": "audio", "reason": reason, "turn_id": turn_id},
        )
        return
    encode_started_ns = time.monotonic_ns()
    audio = base64.b64encode(wav_bytes(raw_pcm)).decode("ascii")
    encode_ended_ns = time.monotonic_ns()
    _trace_event(
        "input.audio.encoded",
        component="speechllm",
        turn_id=turn_id,
        monotonic_ns=encode_ended_ns,
        duration_ns=encode_ended_ns - encode_started_ns,
        details={"pcm_bytes": len(raw_pcm), "base64_bytes": len(audio)},
    )
    user_message = {
        "role": "user",
        "content": [
            {"type": "input_audio", "input_audio": {"data": audio, "format": "wav"}},
            {
                "type": "text",
                "text": (
                    "This is a live voice turn. Reply directly in one to three "
                    "short sentences. Never ask what the person needs it for or "
                    "ask a generic implementation question. For an explicit G1 "
                    "gesture request, call the matching tool before speaking."
                ),
            },
        ],
    }
    STATE.publish("user_turn", {"kind": "audio", "duration": round(duration, 1)})
    generate_reply(
        user_message,
        turn_id=turn_id,
        input_started_ns=input_started_ns,
        input_ended_ns=input_ended_ns,
        wait_for_idle=True,
    )


def generate_text_reply(text: str, turn_id: str | None = None) -> None:
    turn_id = turn_id or f"typed-{uuid.uuid4().hex}"
    _trace_event(
        "input.text.accepted",
        component="voice-http",
        turn_id=turn_id,
        details={"characters": len(text)},
    )
    STATE.publish("user_turn", {"kind": "text", "text": text})
    generate_reply(
        {"role": "user", "content": text},
        turn_id=turn_id,
    )


def _stream_completion(
    messages: list[dict],
    speech_queue: queue.Queue,
    *,
    allow_tools: bool,
    turn_id: str,
    pass_index: int,
    blocked_first_sentences: set[str] | None = None,
) -> tuple[str, str, dict | None]:
    payload: dict = {
        "model": MODEL_ALIAS,
        "messages": messages,
        "stream": True,
        "max_tokens": 192,
        "temperature": 0.45,
        "top_p": 0.9,
    }
    if allow_tools and G1_CLIENT.enabled:
        payload["tools"] = TOOL_SCHEMAS
        payload["tool_choice"] = "auto"
    request = urllib.request.Request(
        f"{LLAMA_URL}/v1/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    reply = ""
    speech_buffer = ""
    queued_chunks = 0
    tool_call: dict | None = None
    request_started_ns = time.monotonic_ns()
    connected_ns: int | None = None
    first_event_ns: int | None = None
    first_text_ns: int | None = None
    first_chunk_ns: int | None = None
    stream_events = 0
    outcome = "ok"
    first_sentence_checked = False
    first_text_published = False

    def enqueue_chunks(chunks: list[str]) -> None:
        nonlocal queued_chunks, first_chunk_ns, first_sentence_checked
        for chunk in chunks:
            if not first_sentence_checked:
                first_sentence_checked = True
                normalized = canonical_sentence(chunk)
                if normalized and normalized in (blocked_first_sentences or set()):
                    raise RepeatedFirstSentenceError(chunk)
            speech_queue.put(chunk)
            queued_chunks += 1
            if first_chunk_ns is None:
                first_chunk_ns = time.monotonic_ns()
                _trace_event(
                    "inference.first_speech_chunk",
                    component="llama.cpp",
                    turn_id=turn_id,
                    monotonic_ns=first_chunk_ns,
                    details={
                        "pass_index": pass_index,
                        "characters": len(chunk),
                    },
                )
    _trace_event(
        "inference.request.started",
        component="llama.cpp",
        turn_id=turn_id,
        monotonic_ns=request_started_ns,
        details={
            "pass_index": pass_index,
            "message_count": len(messages),
            "allow_tools": allow_tools,
            "model": MODEL_ALIAS,
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            connected_ns = time.monotonic_ns()
            for raw_line in response:
                if STATE.cancel_generation.is_set():
                    outcome = "cancelled"
                    break
                line = raw_line.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if data == "[DONE]":
                    break
                try:
                    event = json.loads(data)
                    delta_object = event["choices"][0].get("delta", {})
                except (KeyError, IndexError, json.JSONDecodeError):
                    continue
                stream_events += 1
                if first_event_ns is None:
                    first_event_ns = time.monotonic_ns()
                for fragment in delta_object.get("tool_calls") or []:
                    if queued_chunks or reply.strip():
                        raise G1ToolError("model emitted a gesture tool after spoken content")
                    if int(fragment.get("index", 0)) != 0:
                        raise G1ToolError("only one G1 tool call is allowed per turn")
                    if tool_call is None:
                        tool_call = {
                            "id": "",
                            "type": "function",
                            "function": {"name": "", "arguments": ""},
                        }
                    tool_call["id"] += str(fragment.get("id") or "")
                    function = fragment.get("function") or {}
                    tool_call["function"]["name"] += str(function.get("name") or "")
                    tool_call["function"]["arguments"] += str(
                        function.get("arguments") or ""
                    )
                delta = delta_object.get("content") or ""
                if delta:
                    now_ns = time.monotonic_ns()
                    if first_text_ns is None:
                        first_text_ns = now_ns
                    if tool_call is not None:
                        raise G1ToolError("model mixed a G1 tool call with spoken content")
                    reply += delta
                    speech_buffer += delta
                    chunks, speech_buffer = extract_tts_chunks(speech_buffer)
                    if chunks:
                        # Validate a complete first sentence before either the
                        # UI or Kokoro can replay it. Later deltas keep streaming.
                        enqueue_chunks(chunks)
                        if not first_text_published:
                            STATE.publish("assistant_delta", {"text": reply})
                            first_text_published = True
                    elif first_text_published:
                        STATE.publish("assistant_delta", {"text": delta})
            if tool_call is None and reply and not STATE.cancel_generation.is_set():
                chunks, speech_buffer = extract_tts_chunks(speech_buffer, final=True)
                if chunks:
                    enqueue_chunks(chunks)
                    if not first_text_published:
                        STATE.publish("assistant_delta", {"text": reply})
                        first_text_published = True
    except RepeatedFirstSentenceError:
        outcome = "replay_suppressed"
        raise
    except Exception:
        outcome = "error"
        raise
    finally:
        ended_ns = time.monotonic_ns()
        _trace_event(
            "inference.request",
            component="llama.cpp",
            turn_id=turn_id,
            monotonic_ns=ended_ns,
            duration_ns=ended_ns - request_started_ns,
            details={
                "pass_index": pass_index,
                "outcome": outcome,
                "http_connect_ms": (
                    None
                    if connected_ns is None
                    else (connected_ns - request_started_ns) / 1_000_000
                ),
                "time_to_first_event_ms": (
                    None
                    if first_event_ns is None
                    else (first_event_ns - request_started_ns) / 1_000_000
                ),
                "time_to_first_text_ms": (
                    None
                    if first_text_ns is None
                    else (first_text_ns - request_started_ns) / 1_000_000
                ),
                "time_to_first_speech_chunk_ms": (
                    None
                    if first_chunk_ns is None
                    else (first_chunk_ns - request_started_ns) / 1_000_000
                ),
                "stream_events": stream_events,
                "output_characters": len(reply),
                "queued_speech_chunks": queued_chunks,
                "tool_call": None
                if tool_call is None
                else tool_call.get("function", {}).get("name"),
            },
        )
    return reply, speech_buffer, tool_call


def _resolve_tool_call(
    tool_call: dict, user_message: dict
) -> tuple[dict, str | None]:
    function = tool_call.get("function") or {}
    name = str(function.get("name") or "")
    try:
        arguments = json.loads(function.get("arguments") or "{}")
        if not isinstance(arguments, dict):
            raise ValueError("tool arguments must be an object")
        if name == "g1_gesture":
            if set(arguments) != {"action", "command_text"}:
                raise ValueError(
                    "g1_gesture requires only action and current-turn command_text"
                )
            action = arguments.get("action")
            decision, gate_result = GESTURE_GATE.evaluate(
                user_message,
                action,
                command_text=arguments.get("command_text"),
            )
            if decision != "execute":
                result = gate_result
                action = None
            else:
                result, action = G1_CLIENT.admit_tool(name, {"action": action})
        else:
            result, action = G1_CLIENT.admit_tool(name, arguments)
    except (G1ToolError, ValueError, json.JSONDecodeError) as exc:
        result = {"success": False, "accepted": False, "error": str(exc)}
        action = None
    STATE.publish(
        "g1_tool_result",
        {"tool": name, "action": action, "result": result},
    )
    return result, action


def record_g1_action_completion(turn_id: str, action: str) -> None:
    """Append terminal G1 evidence without delaying Walter's next voice turn."""
    started_ns = time.monotonic_ns()
    deadline = time.monotonic() + 30.0
    last: dict = {}
    terminal_states = {
        "released",
        "released_by_action",
        "cancelled",
        "cancelled_before_dispatch",
        "release_failed",
        "failed",
        "prepare_failed",
    }
    while time.monotonic() < deadline and not STATE.stop.is_set():
        try:
            response = G1_CLIENT.timing(turn_id)
        except G1ToolError as exc:
            last = {"ok": False, "error": str(exc)}
            time.sleep(0.2)
            continue
        timing = response.get("timing") or {}
        if isinstance(timing, dict):
            last = timing
        if str(last.get("state") or "") in terminal_states:
            ended_ns = time.monotonic_ns()
            action_duration_ns = last.get("request_to_action_complete_ns")
            _trace_event(
                "g1.action.completed",
                component="g1-action-runtime",
                turn_id=turn_id,
                monotonic_ns=ended_ns,
                duration_ns=(
                    action_duration_ns
                    if isinstance(action_duration_ns, int)
                    else ended_ns - started_ns
                ),
                details={"action": action, "timing": last},
            )
            try:
                alignment = G1_CLIENT.alignment_timing(turn_id)
            except G1ToolError as exc:
                alignment = {"ok": False, "error": str(exc)}
            _trace_event(
                "g1.action.final_alignment",
                component="spark-orchestrator",
                turn_id=turn_id,
                details={"action": action, "timing": alignment},
            )
            return
        time.sleep(0.2)
    ended_ns = time.monotonic_ns()
    _trace_event(
        "g1.action.completion_timeout",
        component="g1-action-runtime",
        turn_id=turn_id,
        monotonic_ns=ended_ns,
        duration_ns=ended_ns - started_ns,
        details={"action": action, "last_timing": last},
    )


def generate_reply(
    user_message: dict,
    *,
    turn_id: str | None = None,
    input_started_ns: int | None = None,
    input_ended_ns: int | None = None,
    wait_for_idle: bool = False,
) -> None:
    turn_id = turn_id or f"turn-{uuid.uuid4().hex}"
    queued_ns = time.monotonic_ns()
    _trace_event(
        "turn.queued",
        component="speechllm",
        turn_id=turn_id,
        monotonic_ns=queued_ns,
        details={"wait_for_idle": wait_for_idle},
    )
    with STATE.turn_idle:
        while STATE.turn_active:
            if not wait_for_idle or STATE.stop.is_set():
                _trace_event(
                    "turn.dropped",
                    component="speechllm",
                    turn_id=turn_id,
                    details={"reason": "another turn is active"},
                )
                return
            STATE.turn_idle.wait(timeout=0.5)
        turn_started_ns = time.monotonic_ns()
        STATE.turn_active = True
        STATE.active_turn_id = turn_id
        STATE.generating = True
        STATE.phase = "thinking"
        STATE.cancel_generation.clear()
        messages = [
            {"role": "system", "content": runtime_system_prompt()},
            *STATE.history,
            user_message,
        ]
        is_audio_turn = isinstance(user_message.get("content"), list)
        blocked_first_sentences = (
            replay_guard_sentences(STATE) | VOICE_BLOCKED_FIRST_SENTENCES
            if is_audio_turn
            else set()
        )
    TRACE.activate(turn_id)
    _trace_event(
        "turn.started",
        component="speechllm",
        turn_id=turn_id,
        monotonic_ns=turn_started_ns,
        duration_ns=turn_started_ns - queued_ns,
        details={
            "queue_wait_ms": (turn_started_ns - queued_ns) / 1_000_000,
            "history_messages": max(0, len(messages) - 2),
            "input_kind": "audio"
            if isinstance(user_message.get("content"), list)
            else "text",
        },
    )
    STATE.wake.set()
    STATE.publish("assistant_start", {"turn_id": turn_id})

    if G1_CLIENT.enabled:
        for event_name, event_time in (
            ("input.speech_started", input_started_ns),
            ("input.speech_ended", input_ended_ns),
        ):
            if event_time is None:
                continue
            try:
                G1_CLIENT.record_event(
                    turn_id, event_name, monotonic_ns=event_time
                )
            except G1ToolError as exc:
                print(f"G1 timing event failed: {exc}", flush=True)

    speech_queue: queue.Queue = queue.Queue(maxsize=TTS_CHUNK_QUEUE_SIZE)
    with STATE.lock:
        STATE.active_speech_queue = speech_queue
    speech_errors: list[str] = []
    action_context = {
        "turn_id": turn_id,
        "action": None,
        "attempted": False,
        "scheduled": False,
    }
    speech_thread = threading.Thread(
        target=stream_speech_worker,
        args=(speech_queue, speech_errors, action_context),
        daemon=True,
    )
    speech_thread.start()

    reply = ""
    speech_buffer = ""
    error = ""
    replay_suppressed = False
    try:
        response_pass_index = 1
        while True:
            try:
                reply, speech_buffer, tool_call = _stream_completion(
                    messages,
                    speech_queue,
                    allow_tools=True,
                    turn_id=turn_id,
                    pass_index=response_pass_index,
                    blocked_first_sentences=blocked_first_sentences,
                )
                break
            except RepeatedFirstSentenceError as exc:
                if response_pass_index >= 2 or not is_audio_turn:
                    raise
                _trace_event(
                    "response.replay_retry",
                    component="speechllm",
                    turn_id=turn_id,
                    details={"sentence": exc.sentence},
                )
                # Reuse the same current-turn audio, but make the retry answer
                # its content instead of falling back to a generic phrase. The
                # normal tool gate and G1 preflight still apply unchanged.
                messages = [
                    *messages,
                    {
                        "role": "system",
                        "content": (
                            "Your previous attempt began with a recently spoken "
                            "sentence and was not delivered. Listen to the current "
                            "visitor audio again and answer its actual request. Do "
                            "not begin with a generic acknowledgment such as Okay "
                            "or Hello. If the audio is unclear, ask the visitor to "
                            "repeat it in one short sentence. If and only if this "
                            "audio contains an explicit addressed G1 gesture "
                            "command, call the matching tool before speaking."
                        ),
                    },
                ]
                response_pass_index += 1
        if tool_call is not None and not STATE.cancel_generation.is_set():
            result, action = _resolve_tool_call(tool_call, user_message)
            action_context["action"] = action
            if not tool_call.get("id"):
                tool_call["id"] = f"call-{uuid.uuid4().hex}"
            messages.extend(
                [
                    {"role": "assistant", "content": None, "tool_calls": [tool_call]},
                    {
                        "role": "tool",
                        "tool_call_id": tool_call["id"],
                        "name": tool_call["function"]["name"],
                        "content": json.dumps(result, separators=(",", ":")),
                    },
                ]
            )
            reply, speech_buffer, repeated_tool = _stream_completion(
                messages,
                speech_queue,
                allow_tools=False,
                turn_id=turn_id,
                pass_index=response_pass_index + 1,
                # Repeated short acknowledgements such as "Okay" are valid for
                # independently authorized action turns.
                blocked_first_sentences=set(),
            )
            if repeated_tool is not None:
                raise G1ToolError("model repeated a G1 tool call")
    except RepeatedFirstSentenceError as exc:
        replay_suppressed = True
        reply = ""
        speech_buffer = ""
        _trace_event(
            "response.replay_suppressed",
            component="speechllm",
            turn_id=turn_id,
            details={"sentence": exc.sentence},
        )
        if is_audio_turn:
            record_unusable_voice_turn(turn_id, "blocked_first_sentence")
    except (OSError, urllib.error.URLError, urllib.error.HTTPError, G1ToolError) as exc:
        error = str(exc)

    cancelled = STATE.cancel_generation.is_set()
    speech_queue.put(None)

    with STATE.lock:
        if reply and not error and not cancelled:
            retain_history_turn(
                STATE.history,
                user_message,
                reply,
                action_context.get("action"),
            )
            if is_audio_turn:
                STATE.unusable_voice_turns.clear()
        STATE.generating = False
        STATE.phase = (
            "speaking"
            if STATE.speaking
            else (
                "cooldown"
                if STATE.auto_resume_pending
                else "paused"
                if STATE.listen_pause_reason
                else ("listening" if STATE.listening else "ready")
            )
        )
        STATE.last_error = error
    trim_history()
    STATE.wake.set()
    if not error:
        STATE.publish(
            "assistant_done",
            {
                "text": reply,
                "cancelled": cancelled,
                "suppressed": replay_suppressed,
                "turn_id": turn_id,
                "action": action_context.get("action"),
            },
        )
    speech_thread.join()
    if speech_errors and not error:
        error = speech_errors[0]

    if action_context.get("scheduled"):
        try:
            timing = G1_CLIENT.timing(turn_id)
        except G1ToolError as exc:
            timing = {"ok": False, "error": str(exc)}
        STATE.publish(
            "g1_action_result",
            {
                "turn_id": turn_id,
                "action": action_context.get("action"),
                "timing": timing,
            },
        )
        _trace_event(
            "g1.action.runtime_timing",
            component="g1-action-runtime",
            turn_id=turn_id,
            details={"action": action_context.get("action"), "timing": timing},
        )
        try:
            alignment_timing = G1_CLIENT.alignment_timing(turn_id)
        except G1ToolError as exc:
            alignment_timing = {"ok": False, "error": str(exc)}
        _trace_event(
            "g1.action.alignment",
            component="spark-orchestrator",
            turn_id=turn_id,
            details={"action": action_context.get("action"), "timing": alignment_timing},
        )
        threading.Thread(
            target=record_g1_action_completion,
            args=(turn_id, str(action_context["action"])),
            daemon=True,
        ).start()

    with STATE.turn_idle:
        STATE.speaking = False
        STATE.turn_active = False
        STATE.active_turn_id = None
        if STATE.active_speech_queue is speech_queue:
            STATE.active_speech_queue = None
        STATE.phase = (
            "cooldown"
            if STATE.auto_resume_pending
            else "paused"
            if STATE.listen_pause_reason
            else ("listening" if STATE.listening else "ready")
        )
        STATE.last_error = error
        STATE.turn_idle.notify_all()
    turn_ended_ns = time.monotonic_ns()
    _trace_event(
        "turn.finished",
        component="speechllm",
        turn_id=turn_id,
        monotonic_ns=turn_ended_ns,
        duration_ns=turn_ended_ns - turn_started_ns,
        details={
            "cancelled": cancelled,
            "replay_suppressed": replay_suppressed,
            "error": error or None,
            "reply_characters": len(reply),
            "action": action_context.get("action"),
        },
    )
    TRACE.deactivate(turn_id)
    STATE.wake.set()
    if error:
        STATE.publish("error", {"message": error})
    else:
        STATE.publish("state")


class VoiceHandler(BaseHTTPRequestHandler):
    server_version = "UltravoxVoice/1.0"

    def log_message(self, fmt: str, *args: object) -> None:
        print(f"voice-http: {fmt % args}", flush=True)

    def json_response(self, payload: dict, status: int = 200) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def audio_response(self, body: bytes) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "audio/wav")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def read_json(self) -> dict:
        length = int(self.headers.get("Content-Length", "0"))
        if length <= 0:
            return {}
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        path = parsed.path
        if path == "/health":
            ready = api_ready()
            if ready:
                with STATE.lock:
                    STATE.model_ready = True
                    if STATE.phase == "loading":
                        STATE.phase = "ready"
                STATE.wake.set()
            self.json_response(
                {"status": "ok" if ready else "loading", **STATE.snapshot()},
                200 if ready else 503,
            )
            return
        if path == "/api/status":
            self.json_response(STATE.snapshot())
            return
        if path == "/api/events":
            self.send_events()
            return
        if path == "/api/traces":
            query = parse_qs(parsed.query)
            turn_id = (query.get("turn_id") or [None])[0]
            try:
                limit = int((query.get("limit") or ["1000"])[0])
            except ValueError:
                self.json_response({"error": "limit must be an integer"}, 400)
                return
            self.json_response(
                {"turn_id": turn_id, "events": TRACE.recent(turn_id=turn_id, limit=limit)}
            )
            return
        if path == "/api/trace-summary":
            query = parse_qs(parsed.query)
            turn_id = str((query.get("turn_id") or [""])[0])
            if not turn_id:
                self.json_response({"error": "turn_id is required"}, 400)
                return
            self.json_response(TRACE.summarize(turn_id))
            return
        self.send_static(path)

    def do_POST(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        try:
            payload = self.read_json()
        except (ValueError, json.JSONDecodeError):
            self.json_response({"error": "Invalid JSON"}, 400)
            return

        if path == "/api/listening":
            enabled = bool(payload.get("enabled"))
            with STATE.lock:
                STATE.listening = enabled
                STATE.phase = "listening" if enabled else "ready"
                STATE.unusable_voice_turns.clear()
                STATE.listen_pause_reason = ""
                STATE.auto_resume_pending = False
                STATE.auto_resume_not_before = 0.0
                STATE.auto_resume_force_at = 0.0
                STATE.auto_resume_quiet_since = None
                STATE.auto_resume_turn_id = None
            STATE.wake.set()
            STATE.publish("state")
            self.json_response(STATE.snapshot())
            return
        if path == "/api/speaking":
            self.json_response(
                {"error": "Speech output is managed directly through ALSA"}, 409
            )
            return
        if path == "/api/message":
            text = str(payload.get("text", "")).strip()
            if not text:
                self.json_response({"error": "Message is empty"}, 400)
                return
            turn_id = f"typed-{uuid.uuid4().hex}"
            threading.Thread(
                target=generate_text_reply, args=(text, turn_id), daemon=True
            ).start()
            self.json_response({"accepted": True, "turn_id": turn_id}, 202)
            return
        if path == "/api/tts":
            text = str(payload.get("text", "")).strip()
            if not text:
                self.json_response({"error": "Speech text is empty"}, 400)
                return
            if len(text) > 1200:
                self.json_response({"error": "Speech text is too long"}, 400)
                return
            try:
                self.audio_response(synthesize_speech(text))
            except Exception as exc:
                print(f"Kokoro TTS error: {exc}", flush=True)
                self.json_response({"error": f"Neural voice failed: {exc}"}, 500)
            return
        if path == "/api/interrupt":
            STATE.cancel_generation.set()
            GESTURE_GATE.clear()
            stop_playback()
            self.json_response({"interrupted": True})
            return
        if path == "/api/reset":
            STATE.cancel_generation.set()
            GESTURE_GATE.clear()
            stop_playback()
            with STATE.lock:
                STATE.history.clear()
                STATE.recent_spoken_sentences.clear()
                STATE.recent_audio_digests.clear()
                STATE.unusable_voice_turns.clear()
                STATE.listen_pause_reason = ""
                STATE.auto_resume_pending = False
                STATE.auto_resume_not_before = 0.0
                STATE.auto_resume_force_at = 0.0
                STATE.auto_resume_quiet_since = None
                STATE.auto_resume_turn_id = None
            STATE.publish("reset")
            self.json_response({"reset": True})
            return
        self.json_response({"error": "Not found"}, 404)

    def send_events(self) -> None:
        subscriber: queue.Queue = queue.Queue(maxsize=128)
        with STATE.lock:
            STATE.subscribers.append(subscriber)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        try:
            initial = {"event": "state", "data": {}, "state": STATE.snapshot()}
            self.wfile.write(f"data: {json.dumps(initial)}\n\n".encode())
            self.wfile.flush()
            while not STATE.stop.is_set():
                try:
                    event = subscriber.get(timeout=12)
                    data = json.dumps(event, separators=(",", ":"))
                    self.wfile.write(f"data: {data}\n\n".encode())
                except queue.Empty:
                    self.wfile.write(b": keepalive\n\n")
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            with STATE.lock:
                if subscriber in STATE.subscribers:
                    STATE.subscribers.remove(subscriber)

    def send_static(self, request_path: str) -> None:
        relative = request_path.lstrip("/") or "index.html"
        target = (PUBLIC_DIR / relative).resolve()
        if PUBLIC_DIR not in target.parents and target != PUBLIC_DIR:
            self.send_error(403)
            return
        if not target.is_file():
            target = PUBLIC_DIR / "index.html"
        try:
            body = target.read_bytes()
        except OSError:
            self.send_error(404)
            return
        mime = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(body)


def wait_for_model() -> None:
    while not STATE.stop.is_set() and not api_ready(1.0):
        time.sleep(1)
    if not STATE.stop.is_set():
        with STATE.lock:
            STATE.model_ready = True
            if STATE.phase == "loading":
                STATE.phase = "ready"
        STATE.wake.set()
        STATE.publish("state")
        print(
            "SpeechLLM -> Kokoro -> ALSA ready; "
            f"input={STATE.capture_source}, output={ALSA_PLAYBACK_DEVICE}, "
            f"auto-listen={'on' if AUTO_LISTEN else 'off'}",
            flush=True,
        )


def main() -> None:
    TRACE.start_gpu_sampler()
    threading.Thread(target=wait_for_model, daemon=True).start()
    threading.Thread(target=prewarm_tts, daemon=True).start()
    threading.Thread(target=capture_loop, daemon=True).start()
    server = ThreadingHTTPServer(("0.0.0.0", VOICE_PORT), VoiceHandler)
    print(f"Optional status/control API on 0.0.0.0:{VOICE_PORT}", flush=True)
    try:
        server.serve_forever(poll_interval=0.25)
    except KeyboardInterrupt:
        pass
    finally:
        STATE.stop.set()
        STATE.wake.set()
        TRACE.close()
        server.server_close()


if __name__ == "__main__":
    main()
