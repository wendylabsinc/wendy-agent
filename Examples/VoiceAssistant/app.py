#!/usr/bin/env python3
"""Low-latency ALSA bridge for OpenAI's Realtime speech-to-speech API."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import re
import time
from dataclasses import dataclass
from typing import Any
from urllib.parse import quote

from websockets.asyncio.client import connect


LOG = logging.getLogger("voice-assistant")
BYTES_PER_SAMPLE = 2  # signed PCM16

# "card 2: Device [USB Audio Device], device 0: USB Audio [USB Audio]"
_ALSA_CARD_LINE = re.compile(
    r"^card (\d+): (\S+) \[(.+?)\], device (\d+): (.*)$", re.MULTILINE
)


def _env_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    if value is None or not value.strip():
        return default
    return value.strip().lower() not in {"0", "false", "no", "off"}


@dataclass(frozen=True)
class AlsaCard:
    number: int
    name: str
    description: str
    device: int
    device_description: str = ""


def parse_alsa_cards(listing: str) -> list[AlsaCard]:
    """Parse `arecord -l` / `aplay -l` output, keeping the first device per card."""
    cards: list[AlsaCard] = []
    seen: set[int] = set()
    for match in _ALSA_CARD_LINE.finditer(listing):
        number = int(match.group(1))
        if number in seen:
            continue
        seen.add(number)
        cards.append(
            AlsaCard(
                number=number,
                name=match.group(2),
                description=match.group(3),
                device=int(match.group(4)),
                device_description=match.group(5),
            )
        )
    return cards


def pick_alsa_device(cards: list[AlsaCard]) -> str | None:
    """Pick the first USB card, addressed by CARD name (stable across reboots).

    The card description is the product string and often lacks "usb"
    (e.g. "Anker PowerConf S330"), but snd-usb-audio always names the device
    "USB Audio" — so match against both columns."""
    for card in cards:
        if "usb" in f"{card.description} {card.device_description}".lower():
            return f"plughw:CARD={card.name},DEV={card.device}"
    return None


async def _run_command(*args: str) -> tuple[int, str]:
    process = await asyncio.create_subprocess_exec(
        *args,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
    )
    output, _ = await process.communicate()
    return process.returncode or 0, output.decode(errors="replace")


async def detect_device(binary: str, run: Any = None) -> str:
    """Auto-select an ALSA PCM by listing hardware with `arecord -l`/`aplay -l`."""
    run = run or _run_command
    returncode, output = await run(binary, "-l")
    device = pick_alsa_device(parse_alsa_cards(output)) if returncode == 0 else None
    if device is None:
        LOG.info("No USB card found via `%s -l`; using ALSA default", binary)
        return "default"
    LOG.info("Auto-detected %s device %s", "capture" if binary == "arecord" else "playback", device)
    return device


def card_from_device(device: str) -> str | None:
    """Extract the amixer -c argument (card index or id) from an ALSA PCM name."""
    _, _, rest = device.partition(":")
    if rest.startswith("CARD="):
        return rest[len("CARD=") :].split(",")[0] or None
    digits = rest.split(",")[0]
    return digits if digits.isdigit() else None


@dataclass(frozen=True)
class Config:
    api_key: str
    model: str = "gpt-realtime-2.1"
    voice: str = "marin"
    instructions: str = (
        "You are a friendly voice assistant running on an edge device. "
        "Be conversational, helpful, and concise. Reply in the language the user speaks. "
        "You can change your own speaker volume with the provided tools when asked."
    )
    input_device: str | None = None  # None -> auto-detect
    output_device: str | None = None  # None -> auto-detect
    sample_rate: int = 24_000
    chunk_ms: int = 100
    mute_input_during_playback: bool = False
    startup_volume_percent: int | None = 70

    @staticmethod
    def _parse_startup_volume(raw: str | None) -> int | None:
        raw = (raw or "").strip()
        if not raw:
            return 70
        if raw.lower() in {"off", "false", "no", "none", "disabled"}:
            return None
        try:
            percent = int(raw)
        except ValueError as exc:
            raise ValueError(
                "STARTUP_VOLUME_PERCENT must be an integer 0-100 or 'off'"
            ) from exc
        if not 0 <= percent <= 100:
            raise ValueError("STARTUP_VOLUME_PERCENT must be between 0 and 100")
        return percent

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
            input_device=os.getenv("AUDIO_INPUT_DEVICE", "").strip() or None,
            output_device=os.getenv("AUDIO_OUTPUT_DEVICE", "").strip() or None,
            sample_rate=sample_rate,
            chunk_ms=chunk_ms,
            mute_input_during_playback=_env_bool("MUTE_INPUT_DURING_PLAYBACK", False),
            startup_volume_percent=cls._parse_startup_volume(
                os.getenv("STARTUP_VOLUME_PERCENT")
            ),
        )

    @property
    def chunk_bytes(self) -> int:
        samples = self.sample_rate * self.chunk_ms // 1000
        return samples * BYTES_PER_SAMPLE


class AlsaMixer:
    """Playback volume control via `amixer`, mirroring the wendy-agent logic:
    order simple controls by preference, use the first one that reports a
    playback percentage, and always re-parse amixer's own output so callers
    see the actual (quantized) volume ALSA applied."""

    PREFERRED = ("Master", "PCM", "Speaker", "Headphone")
    _CONTROL = re.compile(r"^Simple mixer control '(.+)',\d+$", re.MULTILINE)
    _PLAYBACK_VOLUME = re.compile(r"\[(\d{1,3})%\]")

    def __init__(self, card: str, run: Any = None) -> None:
        self.card = card
        # The runner receives only the amixer subcommand and its arguments,
        # matching the agent's injectable amixerRun test seam.
        self._run = run or (lambda *args: _run_command("amixer", "-c", card, *args))
        self._control: str | None = None

    @staticmethod
    def parse_controls(output: str) -> list[str]:
        return AlsaMixer._CONTROL.findall(output)

    @classmethod
    def order_controls(cls, controls: list[str]) -> list[str]:
        preferred_rank = {name.lower(): i for i, name in enumerate(cls.PREFERRED)}
        fallback = len(preferred_rank)
        return sorted(
            controls,
            key=lambda name: (preferred_rank.get(name.lower(), fallback),),
        )

    @classmethod
    def parse_playback_volume(cls, output: str) -> int | None:
        for line in output.splitlines():
            if "Playback" not in line:
                continue
            match = cls._PLAYBACK_VOLUME.search(line)
            if match and int(match.group(1)) <= 100:
                return int(match.group(1))
        return None

    async def _amixer(self, *args: str) -> str:
        returncode, output = await self._run(*args)
        if returncode != 0:
            raise RuntimeError(
                f"amixer -c {self.card} {' '.join(args)} failed: {output.strip()}"
            )
        return output

    async def resolve_control(self) -> tuple[str, int]:
        controls = self.order_controls(
            self.parse_controls(await self._amixer("scontrols"))
        )
        for control in controls:
            try:
                output = await self._amixer("sget", control)
            except RuntimeError:
                # A control that cannot be queried is skipped, not fatal —
                # keeps USB and board-specific codecs usable (Go agent parity).
                continue
            volume = self.parse_playback_volume(output)
            if volume is not None:
                self._control = control
                return control, volume
        raise RuntimeError(
            f"no playback volume control found on ALSA card {self.card}"
        )

    async def get_volume(self) -> int:
        if self._control is None:
            return (await self.resolve_control())[1]
        volume = self.parse_playback_volume(await self._amixer("sget", self._control))
        if volume is None:
            raise RuntimeError(f"control '{self._control}' lost its playback volume")
        return volume

    async def set_volume(self, percent: int) -> int:
        percent = max(0, min(100, percent))
        if self._control is None:
            await self.resolve_control()
        assert self._control is not None
        output = await self._amixer("sset", self._control, f"{percent}%", "unmute")
        applied = self.parse_playback_volume(output)
        return applied if applied is not None else percent

    async def adjust_volume(self, direction: str, step: int = 10) -> int:
        current = await self.get_volume()
        delta = step if direction == "up" else -step
        return await self.set_volume(current + delta)


class AlsaAudio:
    """Owns one arecord process and one aplay process."""

    def __init__(self, config: Config, input_device: str, output_device: str) -> None:
        self.config = config
        self.input_device = input_device
        self.output_device = output_device
        self.capture: asyncio.subprocess.Process | None = None
        self.playback: asyncio.subprocess.Process | None = None

    async def __aenter__(self) -> "AlsaAudio":
        common = [
            "-q",
            "-D",
            self.input_device,
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
            self.output_device,
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
        self._startup_volume_done = False
        self.mixer: Any | None = None
        self.user_speaking = False

    @staticmethod
    def tool_specs() -> list[dict[str, Any]]:
        return [
            {
                "type": "function",
                "name": "set_volume",
                "description": (
                    "Set the speaker output volume to an absolute percentage. "
                    "Use for requests like 'set the volume to 30 percent'."
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "percent": {"type": "integer", "minimum": 0, "maximum": 100}
                    },
                    "required": ["percent"],
                },
            },
            {
                "type": "function",
                "name": "adjust_volume",
                "description": (
                    "Nudge the speaker volume up or down. Use for relative "
                    "requests like 'turn it up a bit' or 'quieter please'."
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "direction": {"type": "string", "enum": ["up", "down"]},
                        "step": {
                            "type": "integer",
                            "minimum": 1,
                            "maximum": 100,
                            "default": 10,
                        },
                    },
                    "required": ["direction"],
                },
            },
            {
                "type": "function",
                "name": "get_volume",
                "description": "Report the current speaker output volume percentage.",
                "parameters": {"type": "object", "properties": {}},
            },
        ]

    def session_update_event(self) -> dict[str, Any]:
        return {
            "type": "session.update",
            "session": {
                "type": "realtime",
                "model": self.config.model,
                "output_modalities": ["audio"],
                "instructions": self.config.instructions,
                "tools": self.tool_specs(),
                "tool_choice": "auto",
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

    async def apply_startup_volume(self, mixer: Any) -> None:
        """Set a known output volume once per process; WendyOS has no boot-time
        volume init, so a fresh device may otherwise be silent or very quiet."""
        percent = self.config.startup_volume_percent
        if percent is None or self._startup_volume_done:
            return
        try:
            applied = await mixer.set_volume(percent)
        except Exception as exc:
            # Don't latch: USB devices can enumerate late, so retry next session.
            LOG.warning("Could not set startup volume: %s", exc)
            return
        self._startup_volume_done = True
        LOG.info(
            "Startup volume set to %d%% (card %s)",
            applied,
            getattr(mixer, "card", "unknown"),
        )

    async def run_session(self) -> None:
        # A dropped connection can happen mid-response. Never carry playback
        # gating or transcript fragments into the replacement session.
        if self.playback_done_task is not None:
            self.playback_done_task.cancel()
            self.playback_done_task = None
        self.assistant_speaking.clear()
        self._reset_playback_state()
        self.interrupted_item_id = None
        self.user_speaking = False
        self.transcript_parts.clear()
        url = (
            "wss://api.openai.com/v1/realtime?model="
            f"{quote(self.config.model, safe='')}"
        )
        headers = {
            "Authorization": f"Bearer {self.config.api_key}",
            "OpenAI-Safety-Identifier": "wendy-voice-assistant-demo",
        }

        # Devices can enumerate late or renumber across reconnects, so resolve
        # them at the start of every session rather than once at startup.
        input_device = self.config.input_device or await detect_device("arecord")
        output_device = self.config.output_device or await detect_device("aplay")

        card = card_from_device(output_device)
        if card is None and self.config.output_device:
            # An explicit but unparsable device (e.g. "default" via asound.conf):
            # bind the mixer to whatever playback card auto-detection can name.
            # Note this card may differ from the one actually playing.
            card = card_from_device(await detect_device("aplay"))
        if card is None:
            self.mixer = None
            LOG.warning(
                "No ALSA card resolved from %s; volume control disabled", output_device
            )
        else:
            self.mixer = AlsaMixer(card)
            await self.apply_startup_volume(self.mixer)

        LOG.info(
            "Opening Realtime session (%s); mic=%s%s speaker=%s%s",
            self.config.model,
            input_device,
            "" if self.config.input_device else " (auto)",
            output_device,
            "" if self.config.output_device else " (auto)",
        )
        async with AlsaAudio(self.config, input_device, output_device) as audio:
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
            self.user_speaking = True
            if self.assistant_speaking.is_set():
                await self.interrupt_response(websocket, audio)
            else:
                LOG.info("Listening…")
        elif event_type == "input_audio_buffer.speech_stopped":
            self.user_speaking = False
            LOG.info("Thinking…")
        elif event_type == "response.output_item.done":
            # Function-call arguments arrive complete on the finished item, so
            # there is no need to assemble response.function_call_arguments.*
            # deltas.
            item = event.get("item", {})
            if item.get("type") == "function_call" and websocket is not None:
                await self.handle_tool_call(item, websocket)
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

    async def execute_tool(self, name: str, arguments_json: str) -> dict[str, Any]:
        """Run one volume tool; always returns a JSON-safe payload, never raises —
        a hardware failure must not kill the session."""
        if self.mixer is None:
            return {"error": "volume control is unavailable on this audio device"}
        try:
            arguments = json.loads(arguments_json) if arguments_json.strip() else {}
            if not isinstance(arguments, dict):
                raise ValueError("arguments must be a JSON object")
            if name == "set_volume":
                applied = await self.mixer.set_volume(int(arguments["percent"]))
            elif name == "adjust_volume":
                direction = arguments["direction"]
                if direction not in ("up", "down"):
                    raise ValueError(f"direction must be 'up' or 'down', got {direction!r}")
                applied = await self.mixer.adjust_volume(
                    direction, step=int(arguments.get("step", 10))
                )
            elif name == "get_volume":
                applied = await self.mixer.get_volume()
            else:
                return {"error": f"unknown tool: {name}"}
            return {"volume_percent": applied}
        except Exception as exc:
            return {"error": str(exc)}

    async def handle_tool_call(self, item: dict[str, Any], websocket: Any) -> None:
        name = item.get("name", "")
        result = await self.execute_tool(name, item.get("arguments", ""))
        LOG.info("Tool %s(%s) -> %s", name, item.get("arguments", ""), result)
        await websocket.send(
            json.dumps(
                {
                    "type": "conversation.item.create",
                    "item": {
                        "type": "function_call_output",
                        "call_id": item.get("call_id"),
                        "output": json.dumps(result),
                    },
                }
            )
        )
        if self.user_speaking:
            # The output is in the conversation; the model's next turn will see
            # it. Forcing a response now would race semantic VAD's turn taking.
            LOG.info("User is speaking; deferring the spoken tool result")
            return
        await websocket.send(json.dumps({"type": "response.create"}))

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
