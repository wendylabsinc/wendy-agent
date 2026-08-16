#!/usr/bin/env python3
"""DGX Spark ALSA microphone voice loop and static UI for Ultravox."""

from __future__ import annotations

import array
import base64
import collections
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
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse


PUBLIC_DIR = Path(os.environ.get("VOICE_WEB_ROOT", "/app/web")).resolve()
VOICE_PORT = int(os.environ.get("VOICE_PORT", "8080"))
LLAMA_PORT = int(os.environ.get("LLAMA_PORT", "8081"))
LLAMA_URL = f"http://127.0.0.1:{LLAMA_PORT}"
MODEL_ALIAS = os.environ.get("MODEL_ALIAS", "ultravox-v0.5-llama-3.3-70b-q6-k")
SAMPLE_RATE = 16_000
FRAME_MS = 20
FRAME_BYTES = SAMPLE_RATE * 2 * FRAME_MS // 1000
TTS_MODEL_DIR = Path(os.environ.get("TTS_MODEL_DIR", "/models/kokoro-multi-lang-v1_0"))
TTS_SPEAKER_ID = int(os.environ.get("TTS_SPEAKER_ID", "3"))
TTS_SPEED = float(os.environ.get("TTS_SPEED", "1.04"))
TTS_THREADS = int(os.environ.get("TTS_THREADS", "8"))

SYSTEM_PROMPT = (
    "You are a warm, quick voice assistant in a live conversation. Listen "
    "carefully to each audio turn and reply naturally in one to three short "
    "sentences. Do not describe your process, mention transcription, or use "
    "markdown unless the user asks for it."
)


class VoiceState:
    def __init__(self) -> None:
        self.lock = threading.RLock()
        self.listening = False
        self.browser_speaking = False
        self.generating = False
        self.phase = "loading"
        self.capture_backend = "detecting"
        self.capture_source = os.environ.get("AUDIO_SOURCE", "default")
        self.level = 0.0
        self.last_error = ""
        self.history: list[dict] = []
        self.subscribers: list[queue.Queue] = []
        self.stop = threading.Event()
        self.wake = threading.Event()
        self.cancel_generation = threading.Event()

    def snapshot(self) -> dict:
        with self.lock:
            return {
                "listening": self.listening,
                "speaking": self.browser_speaking,
                "generating": self.generating,
                "phase": self.phase,
                "capture_backend": self.capture_backend,
                "capture_source": self.capture_source,
                "level": self.level,
                "model": MODEL_ALIAS,
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
    started = time.monotonic()
    with TTS_GENERATE_LOCK:
        audio = engine.generate(text, generation)
    if len(audio.samples) == 0:
        raise RuntimeError("Kokoro produced no audio")
    duration = len(audio.samples) / audio.sample_rate
    elapsed = time.monotonic() - started
    print(
        f"Kokoro synthesized {duration:.2f}s in {elapsed:.2f}s "
        f"(RTF {elapsed / duration:.2f})",
        flush=True,
    )
    return samples_to_wav(audio.samples, audio.sample_rate)


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
        return STATE.listening and not STATE.browser_speaking and not STATE.generating


def capture_loop() -> None:
    pre_roll: collections.deque[bytes] = collections.deque(maxlen=15)
    process: subprocess.Popen | None = None
    buffer = bytearray()
    utterance: list[bytes] = []
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
            STATE.set_phase("listening")

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
                    STATE.set_phase("calibrating")
                if calibration_frames == 0:
                    STATE.set_phase("listening")
                pre_roll.clear()
                continue

            threshold = max(0.012, noise_floor * 1.8)
            if not in_speech:
                if level < threshold:
                    noise_floor = noise_floor * 0.96 + min(level, 0.025) * 0.04
                pre_roll.append(frame)
                hot_frames = hot_frames + 1 if level >= threshold else 0
                if hot_frames >= 3:
                    in_speech = True
                    silent_frames = 0
                    utterance = list(pre_roll)
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
                        threading.Thread(
                            target=generate_audio_reply,
                            args=(raw, duration),
                            daemon=True,
                        ).start()
                    in_speech = False
                    hot_frames = 0
                    silent_frames = 0
                    utterance = []
                    pre_roll.clear()
                    break


def trim_history() -> None:
    with STATE.lock:
        if len(STATE.history) > 6:
            STATE.history = STATE.history[-6:]


def generate_audio_reply(raw_pcm: bytes, duration: float) -> None:
    audio = base64.b64encode(wav_bytes(raw_pcm)).decode("ascii")
    user_message = {
        "role": "user",
        "content": [
            {"type": "input_audio", "input_audio": {"data": audio, "format": "wav"}},
            {"type": "text", "text": "Reply directly and conversationally to this voice message."},
        ],
    }
    STATE.publish("user_turn", {"kind": "audio", "duration": round(duration, 1)})
    generate_reply(user_message)


def generate_text_reply(text: str) -> None:
    STATE.publish("user_turn", {"kind": "text", "text": text})
    generate_reply({"role": "user", "content": text})


def generate_reply(user_message: dict) -> None:
    with STATE.lock:
        if STATE.generating:
            return
        STATE.generating = True
        STATE.phase = "thinking"
        STATE.cancel_generation.clear()
        messages = [{"role": "system", "content": SYSTEM_PROMPT}, *STATE.history, user_message]
    STATE.wake.set()
    STATE.publish("assistant_start")

    body = json.dumps(
        {
            "model": MODEL_ALIAS,
            "messages": messages,
            "stream": True,
            "max_tokens": 192,
            "temperature": 0.45,
            "top_p": 0.9,
        }
    ).encode("utf-8")
    request = urllib.request.Request(
        f"{LLAMA_URL}/v1/chat/completions",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    reply = ""
    error = ""
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            for raw_line in response:
                if STATE.cancel_generation.is_set():
                    break
                line = raw_line.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if data == "[DONE]":
                    break
                try:
                    event = json.loads(data)
                    delta = event["choices"][0].get("delta", {}).get("content") or ""
                except (KeyError, IndexError, json.JSONDecodeError):
                    continue
                if delta:
                    reply += delta
                    STATE.publish("assistant_delta", {"text": delta})
    except (OSError, urllib.error.URLError, urllib.error.HTTPError) as exc:
        error = str(exc)

    cancelled = STATE.cancel_generation.is_set()
    with STATE.lock:
        if reply and not cancelled:
            STATE.history.extend([user_message, {"role": "assistant", "content": reply}])
        STATE.generating = False
        STATE.phase = "listening" if STATE.listening else "ready"
        STATE.last_error = error
    trim_history()
    STATE.wake.set()
    if error:
        STATE.publish("error", {"message": error})
    else:
        STATE.publish("assistant_done", {"text": reply, "cancelled": cancelled})


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
        path = urlparse(self.path).path
        if path == "/health":
            ready = api_ready()
            if ready and STATE.phase == "loading":
                STATE.set_phase("ready")
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
            STATE.wake.set()
            STATE.publish("state")
            self.json_response(STATE.snapshot())
            return
        if path == "/api/speaking":
            speaking = bool(payload.get("speaking"))
            with STATE.lock:
                STATE.browser_speaking = speaking
                if speaking:
                    STATE.phase = "speaking"
                elif not STATE.generating:
                    STATE.phase = "listening" if STATE.listening else "ready"
            STATE.wake.set()
            STATE.publish("state")
            self.json_response(STATE.snapshot())
            return
        if path == "/api/message":
            text = str(payload.get("text", "")).strip()
            if not text:
                self.json_response({"error": "Message is empty"}, 400)
                return
            threading.Thread(target=generate_text_reply, args=(text,), daemon=True).start()
            self.json_response({"accepted": True}, 202)
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
            self.json_response({"interrupted": True})
            return
        if path == "/api/reset":
            STATE.cancel_generation.set()
            with STATE.lock:
                STATE.history.clear()
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
        STATE.set_phase("ready")


def main() -> None:
    threading.Thread(target=wait_for_model, daemon=True).start()
    threading.Thread(target=capture_loop, daemon=True).start()
    server = ThreadingHTTPServer(("0.0.0.0", VOICE_PORT), VoiceHandler)
    print(f"Ultravox Live listening on http://0.0.0.0:{VOICE_PORT}", flush=True)
    try:
        server.serve_forever(poll_interval=0.25)
    except KeyboardInterrupt:
        pass
    finally:
        STATE.stop.set()
        STATE.wake.set()
        server.server_close()


if __name__ == "__main__":
    main()
