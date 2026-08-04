#!/usr/bin/env python3
"""Low-latency ALSA bridge for OpenAI's Realtime speech-to-speech API."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import time
from dataclasses import dataclass
from typing import Any
from urllib.parse import quote

from websockets.asyncio.client import connect


LOG = logging.getLogger("voice-assistant")
BYTES_PER_SAMPLE = 2  # signed PCM16


def _env_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    if value is None or not value.strip():
        return default
    return value.strip().lower() not in {"0", "false", "no", "off"}


@dataclass(frozen=True)
class Config:
    api_key: str
    model: str = "gpt-realtime-2.1"
    voice: str = "marin"
    instructions: str = (
        "You are a friendly voice assistant running on an edge device. "
        "Be conversational, helpful, and concise. Reply in the language the user speaks."
    )
    input_device: str = "plughw:2,0"
    output_device: str = "plughw:2,0"
    sample_rate: int = 24_000
    chunk_ms: int = 100
    mute_input_during_playback: bool = False

    @classmethod
    def from_env(cls) -> "Config":
        api_key = os.getenv("OPENAI_API_KEY", "").strip()
        if not api_key:
            raise ValueError(
                "OPENAI_API_KEY is missing. Export it before running or deploying the app."
            )

        sample_rate = int(os.getenv("AUDIO_SAMPLE_RATE", "24000"))
        chunk_ms = int(os.getenv("AUDIO_CHUNK_MS", "100"))
        if sample_rate <= 0 or chunk_ms <= 0:
            raise ValueError("AUDIO_SAMPLE_RATE and AUDIO_CHUNK_MS must be positive integers")

        return cls(
            api_key=api_key,
            model=os.getenv("OPENAI_REALTIME_MODEL", "gpt-realtime-2.1"),
            voice=os.getenv("OPENAI_VOICE", "marin"),
            instructions=os.getenv("ASSISTANT_INSTRUCTIONS", cls.instructions),
            input_device=os.getenv("AUDIO_INPUT_DEVICE", "plughw:2,0"),
            output_device=os.getenv("AUDIO_OUTPUT_DEVICE", "plughw:2,0"),
            sample_rate=sample_rate,
            chunk_ms=chunk_ms,
            mute_input_during_playback=_env_bool("MUTE_INPUT_DURING_PLAYBACK", False),
        )

    @property
    def chunk_bytes(self) -> int:
        samples = self.sample_rate * self.chunk_ms // 1000
        return samples * BYTES_PER_SAMPLE


class AlsaAudio:
    """Owns one arecord process and one aplay process."""

    def __init__(self, config: Config) -> None:
        self.config = config
        self.capture: asyncio.subprocess.Process | None = None
        self.playback: asyncio.subprocess.Process | None = None

    async def __aenter__(self) -> "AlsaAudio":
        common = [
            "-q",
            "-D",
            self.config.input_device,
            "-t",
            "raw",
            "-f",
            "S16_LE",
            "-c",
            "1",
            "-r",
            str(self.config.sample_rate),
        ]
        self.capture = await asyncio.create_subprocess_exec(
            "arecord",
            *common,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

        await self._start_playback()
        return self

    async def _start_playback(self) -> None:
        playback_args = [
            "-q",
            "-D",
            self.config.output_device,
            "-t",
            "raw",
            "-f",
            "S16_LE",
            "-c",
            "1",
            "-r",
            str(self.config.sample_rate),
        ]
        self.playback = await asyncio.create_subprocess_exec(
            "aplay",
            *playback_args,
            stdin=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )

    async def interrupt_playback(self) -> None:
        """Drop buffered speaker audio and reopen playback for the next turn."""
        playback = self.playback
        self.playback = None
        await self._stop_process(playback)
        await self._start_playback()

    async def __aexit__(self, *_: object) -> None:
        await asyncio.gather(
            self._stop_process(self.capture),
            self._stop_process(self.playback),
            return_exceptions=True,
        )

    @staticmethod
    async def _stop_process(process: asyncio.subprocess.Process | None) -> None:
        if process is None or process.returncode is not None:
            return
        process.terminate()
        try:
            await asyncio.wait_for(process.wait(), timeout=2)
        except asyncio.TimeoutError:
            process.kill()
            await process.wait()

    async def read(self) -> bytes:
        assert self.capture is not None and self.capture.stdout is not None
        chunk = await self.capture.stdout.read(self.config.chunk_bytes)
        if chunk:
            # PCM16 messages must contain complete samples.
            return chunk[: len(chunk) - (len(chunk) % BYTES_PER_SAMPLE)]
        error = await self._stderr(self.capture)
        raise RuntimeError(f"microphone capture stopped: {error or 'arecord exited'}")

    async def write(self, audio: bytes) -> None:
        assert self.playback is not None and self.playback.stdin is not None
        try:
            self.playback.stdin.write(audio)
            await self.playback.stdin.drain()
        except (BrokenPipeError, ConnectionResetError) as exc:
            error = await self._stderr(self.playback)
            raise RuntimeError(f"speaker playback stopped: {error or exc}") from exc

    @staticmethod
    async def _stderr(process: asyncio.subprocess.Process) -> str:
        if process.stderr is None:
            return ""
        return (await process.stderr.read()).decode(errors="replace").strip()


class VoiceAssistant:
    def __init__(self, config: Config) -> None:
        self.config = config
        self.assistant_speaking = asyncio.Event()
        self.playback_deadline = 0.0
        self.playback_started_at = 0.0
        self.playback_audio_seconds = 0.0
        self.current_item_id: str | None = None
        self.current_content_index = 0
        self.interrupted_item_id: str | None = None
        self.playback_done_task: asyncio.Task[None] | None = None
        self.transcript_parts: list[str] = []

    def session_update_event(self) -> dict[str, Any]:
        return {
            "type": "session.update",
            "session": {
                "type": "realtime",
                "model": self.config.model,
                "output_modalities": ["audio"],
                "instructions": self.config.instructions,
                "audio": {
                    "input": {
                        "format": {
                            "type": "audio/pcm",
                            "rate": self.config.sample_rate,
                        },
                        "turn_detection": {
                            "type": "semantic_vad",
                            "create_response": True,
                            "interrupt_response": True,
                        },
                    },
                    "output": {
                        "format": {
                            "type": "audio/pcm",
                            "rate": self.config.sample_rate,
                        },
                        "voice": self.config.voice,
                    },
                },
            },
        }

    async def run_session(self) -> None:
        # A dropped connection can happen mid-response. Never carry playback
        # gating or transcript fragments into the replacement session.
        if self.playback_done_task is not None:
            self.playback_done_task.cancel()
            self.playback_done_task = None
        self.assistant_speaking.clear()
        self._reset_playback_state()
        self.interrupted_item_id = None
        self.transcript_parts.clear()
        url = (
            "wss://api.openai.com/v1/realtime?model="
            f"{quote(self.config.model, safe='')}"
        )
        headers = {
            "Authorization": f"Bearer {self.config.api_key}",
            "OpenAI-Safety-Identifier": "wendy-voice-assistant-demo",
        }

        LOG.info(
            "Opening Realtime session (%s); mic=%s speaker=%s",
            self.config.model,
            self.config.input_device,
            self.config.output_device,
        )
        async with AlsaAudio(self.config) as audio:
            async with connect(
                url,
                additional_headers=headers,
                max_size=None,
                ping_interval=20,
                ping_timeout=20,
            ) as websocket:
                await websocket.send(json.dumps(self.session_update_event()))
                LOG.info("Connected. Speak naturally after the ready message.")
                await asyncio.gather(
                    self.send_microphone(websocket, audio),
                    self.receive_events(websocket, audio),
                )

    async def send_microphone(self, websocket: Any, audio: AlsaAudio) -> None:
        while True:
            chunk = await audio.read()
            if not chunk:
                continue
            if self.config.mute_input_during_playback and self.assistant_speaking.is_set():
                continue
            await websocket.send(
                json.dumps(
                    {
                        "type": "input_audio_buffer.append",
                        "audio": base64.b64encode(chunk).decode("ascii"),
                    }
                )
            )

    async def receive_events(self, websocket: Any, audio: AlsaAudio) -> None:
        async for message in websocket:
            event = json.loads(message)
            await self.handle_event(event, audio, websocket)

    async def handle_event(
        self, event: dict[str, Any], audio: AlsaAudio, websocket: Any | None = None
    ) -> None:
        event_type = event.get("type", "")

        if event_type == "session.updated":
            LOG.info("Ready — listening for your voice.")
        elif event_type == "input_audio_buffer.speech_started":
            if self.assistant_speaking.is_set():
                await self.interrupt_response(websocket, audio)
            else:
                LOG.info("Listening…")
        elif event_type == "input_audio_buffer.speech_stopped":
            LOG.info("Thinking…")
        elif event_type == "response.output_audio.delta":
            item_id = event.get("item_id")
            if item_id and item_id == self.interrupted_item_id:
                return
            if item_id and item_id != self.current_item_id:
                self.current_item_id = item_id
                self.current_content_index = int(event.get("content_index", 0))
                self.playback_started_at = 0.0
                self.playback_audio_seconds = 0.0
                self.playback_deadline = 0.0
                self.interrupted_item_id = None
            chunk = base64.b64decode(event["delta"])
            self.assistant_speaking.set()
            # Track when the final queued sample should reach the speaker. This
            # prevents opening the microphone while ALSA still has buffered audio.
            now = time.monotonic()
            if not self.playback_started_at:
                self.playback_started_at = now
            duration = len(chunk) / (self.config.sample_rate * BYTES_PER_SAMPLE)
            self.playback_audio_seconds += duration
            self.playback_deadline = max(now, self.playback_deadline) + duration
            await audio.write(chunk)
        elif event_type == "response.output_audio_transcript.delta":
            self.transcript_parts.append(event.get("delta", ""))
        elif event_type == "response.done":
            response = event.get("response", {})
            status = response.get("status", "unknown")
            if status != "completed":
                LOG.warning(
                    "Response finished with status %s: %s",
                    status,
                    response.get("status_details"),
                )
            transcript = "".join(self.transcript_parts).strip()
            if transcript:
                LOG.info("Assistant: %s", transcript)
            self.transcript_parts.clear()
            self._schedule_playback_done()
        elif event_type == "error":
            error = event.get("error", event)
            raise RuntimeError(
                f"Realtime API error ({error.get('code', 'unknown')}): "
                f"{error.get('message', error)}"
            )

    async def interrupt_response(self, websocket: Any | None, audio: AlsaAudio) -> None:
        now = time.monotonic()
        played_seconds = min(
            self.playback_audio_seconds,
            max(0.0, now - self.playback_started_at),
        )
        item_id = self.current_item_id
        content_index = self.current_content_index

        if self.playback_done_task is not None:
            self.playback_done_task.cancel()
            self.playback_done_task = None
        await audio.interrupt_playback()
        self.assistant_speaking.clear()

        if websocket is not None and item_id is not None:
            await websocket.send(
                json.dumps(
                    {
                        "type": "conversation.item.truncate",
                        "item_id": item_id,
                        "content_index": content_index,
                        "audio_end_ms": int(played_seconds * 1000),
                    }
                )
            )
        LOG.info("Interrupted — listening…")
        self.interrupted_item_id = item_id
        self._reset_playback_state()

    def _schedule_playback_done(self) -> None:
        if self.playback_done_task is not None:
            self.playback_done_task.cancel()
        delay = max(0.0, self.playback_deadline - time.monotonic())
        if delay == 0:
            self.playback_done_task = None
            self.assistant_speaking.clear()
            return
        item_id = self.current_item_id
        self.playback_done_task = asyncio.create_task(
            self._clear_speaking_after(delay, item_id)
        )

    async def _clear_speaking_after(self, delay: float, item_id: str | None) -> None:
        task = asyncio.current_task()
        try:
            await asyncio.sleep(delay)
            if self.current_item_id == item_id:
                self.assistant_speaking.clear()
        finally:
            if self.playback_done_task is task:
                self.playback_done_task = None

    def _reset_playback_state(self) -> None:
        self.playback_deadline = 0.0
        self.playback_started_at = 0.0
        self.playback_audio_seconds = 0.0
        self.current_item_id = None
        self.current_content_index = 0


async def run() -> None:
    config = Config.from_env()
    assistant = VoiceAssistant(config)
    delay = 1
    while True:
        try:
            await assistant.run_session()
            delay = 1
        except asyncio.CancelledError:
            raise
        except Exception:
            LOG.exception("Voice session stopped; reconnecting in %d seconds", delay)
            await asyncio.sleep(delay)
            delay = min(delay * 2, 30)


def main() -> None:
    logging.basicConfig(
        level=os.getenv("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(message)s",
    )
    try:
        asyncio.run(run())
    except KeyboardInterrupt:
        LOG.info("Stopped")
    except ValueError as exc:
        LOG.error("%s", exc)
        raise SystemExit(2) from exc


if __name__ == "__main__":
    main()
