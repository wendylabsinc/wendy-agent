#!/usr/bin/env python3
"""Low-latency audio bridge for OpenAI's Realtime speech-to-speech API.

Audio goes through PipeWire (pw-record/pw-play) whenever the host exposes a
session socket — the only route that reaches Bluetooth devices — and falls
back to raw ALSA (arecord/aplay) on hosts that only provide /dev/snd."""

from __future__ import annotations

import asyncio
import base64
import json
import logging
import os
import re
import shutil
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any
from urllib.parse import quote, urlencode, urlsplit

from websockets.asyncio.client import connect


LOG = logging.getLogger("voice-assistant")
BYTES_PER_SAMPLE = 2  # signed PCM16

GEOCODING_URL = "https://geocoding-api.open-meteo.com/v1/search"
FORECAST_URL = "https://api.open-meteo.com/v1/forecast"
OPENAI_RESPONSES_URL = "https://api.openai.com/v1/responses"

# WMO weather interpretation codes as reported by Open-Meteo.
_WMO_WEATHER_CODES = {
    0: "clear sky",
    1: "mainly clear",
    2: "partly cloudy",
    3: "overcast",
    45: "fog",
    48: "depositing rime fog",
    51: "light drizzle",
    53: "moderate drizzle",
    55: "dense drizzle",
    56: "light freezing drizzle",
    57: "dense freezing drizzle",
    61: "light rain",
    63: "moderate rain",
    65: "heavy rain",
    66: "light freezing rain",
    67: "heavy freezing rain",
    71: "light snow",
    73: "moderate snow",
    75: "heavy snow",
    77: "snow grains",
    80: "light rain showers",
    81: "moderate rain showers",
    82: "violent rain showers",
    85: "light snow showers",
    86: "heavy snow showers",
    95: "thunderstorm",
    96: "thunderstorm with light hail",
    99: "thunderstorm with heavy hail",
}


def describe_weather_code(code: int | None) -> str:
    """Spoken-friendly text for a WMO weather interpretation code."""
    return _WMO_WEATHER_CODES.get(code, "unknown conditions")


# "(...[site](url)...)" citation groups, then any leftover inline [text](url).
_CITATION_GROUP = re.compile(r"\s*\((?:\[[^\]]+\]\([^)\s]+\)(?:,\s*)?)+\)")
_MARKDOWN_LINK = re.compile(r"\[([^\]]*)\]\([^)\s]*\)")


def strip_citations(text: str) -> str:
    """Drop web-search citation annotations — URLs are noise when spoken."""
    return _MARKDOWN_LINK.sub(r"\1", _CITATION_GROUP.sub("", text))


def extract_output_text(data: dict[str, Any]) -> str:
    """Concatenate the output_text content of an OpenAI Responses API payload."""
    parts: list[str] = []
    for item in data.get("output") or []:
        if not isinstance(item, dict) or item.get("type") != "message":
            continue
        for content in item.get("content") or []:
            if isinstance(content, dict) and content.get("type") == "output_text":
                parts.append(content.get("text", ""))
    return "".join(parts)

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


def _http_json_sync(
    url: str,
    *,
    method: str = "GET",
    headers: dict[str, str] | None = None,
    body: dict[str, Any] | None = None,
    timeout: float = 10.0,
) -> dict[str, Any]:
    """Blocking HTTPS JSON call. Raises RuntimeError with a concise message on
    failure — never echoing request headers, which carry the API key."""
    host = urlsplit(url).hostname or url
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Content-Type", "application/json")
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = response.read()
    except urllib.error.HTTPError as exc:
        detail = exc.read(200).decode(errors="replace")
        raise RuntimeError(f"HTTP {exc.code} from {host}: {detail}") from exc
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise RuntimeError(f"could not reach {host}: {exc}") from exc
    try:
        parsed = json.loads(payload)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{host} returned invalid JSON") from exc
    if not isinstance(parsed, dict):
        raise RuntimeError(f"{host} returned unexpected {type(parsed).__name__} JSON")
    return parsed


async def http_json(url: str, **kwargs: Any) -> dict[str, Any]:
    """Run the blocking HTTP call on a worker thread so the event loop (audio
    pump, barge-in) never stalls. Cancellation cannot stop the thread mid-
    request; a cancelled tool task simply discards the result."""
    return await asyncio.to_thread(_http_json_sync, url, **kwargs)


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


def pipewire_socket() -> str | None:
    """Path of the PipeWire session socket, or None when no session is exposed.

    The wendy-agent audio entitlement mounts the host's user-session socket
    into the container and sets PIPEWIRE_RUNTIME_DIR; XDG_RUNTIME_DIR covers
    running uncontainerized on a desktop."""
    for env in ("PIPEWIRE_RUNTIME_DIR", "XDG_RUNTIME_DIR"):
        runtime_dir = (os.getenv(env) or "").strip()
        if not runtime_dir:
            continue
        path = os.path.join(runtime_dir, "pipewire-0")
        if os.path.exists(path):
            return path
    return None


def select_audio_backend(which: Any = shutil.which) -> str:
    """Prefer PipeWire whenever the host exposes a session socket: it is the
    only route to Bluetooth audio, and raw ALSA on such a host would race
    WirePlumber for the very devices PipeWire has already claimed."""
    socket_path = pipewire_socket()
    if socket_path is None:
        return "alsa"
    if which("pw-record") is None or which("pw-play") is None:
        LOG.error(
            "PipeWire socket %s is mounted but pw-record/pw-play are missing — "
            "this image was built without pipewire-bin; falling back to raw "
            "ALSA, which cannot reach Bluetooth audio",
            socket_path,
        )
        return "alsa"
    return "pipewire"


@dataclass(frozen=True)
class Config:
    api_key: str
    model: str = "gpt-realtime-2.1"
    voice: str = "marin"
    instructions: str = (
        "You are a friendly voice assistant running on an edge device. "
        "Be conversational, helpful, and concise. Reply in the language the user speaks. "
        "You can change your own speaker volume, look up the current weather and "
        "forecast anywhere, and search the web for up-to-date information with "
        "the provided tools."
    )
    input_device: str | None = None  # None -> auto-detect
    output_device: str | None = None  # None -> auto-detect
    sample_rate: int = 24_000
    chunk_ms: int = 100
    mute_input_during_playback: bool = False
    startup_volume_percent: int | None = 70
    search_model: str = "gpt-5-mini"
    web_search_enabled: bool = True

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
            search_model=os.getenv("OPENAI_SEARCH_MODEL", "").strip() or "gpt-5-mini",
            web_search_enabled=_env_bool("WEB_SEARCH_ENABLED", True),
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


class PulseMixer:
    """Playback volume control for the PipeWire backend, via `pactl` over the
    PULSE_SERVER socket the audio entitlement provides. Targets the default
    sink — the node WirePlumber routes playback to, which is the Bluetooth
    speaker once one is connected and default. Duck-types AlsaMixer."""

    SINK = "@DEFAULT_SINK@"
    _PERCENT = re.compile(r"(\d{1,3})%")

    def __init__(self, run: Any = None) -> None:
        # apply_startup_volume logs .card; the sink alias is the honest name.
        self.card = self.SINK
        self._run = run or (lambda *args: _run_command("pactl", *args))

    @classmethod
    def parse_volume(cls, output: str) -> int | None:
        for line in output.splitlines():
            if "Volume:" not in line:
                continue
            match = cls._PERCENT.search(line)
            if match:
                return min(int(match.group(1)), 100)
        return None

    async def _pactl(self, *args: str) -> str:
        returncode, output = await self._run(*args)
        if returncode != 0:
            raise RuntimeError(f"pactl {' '.join(args)} failed: {output.strip()}")
        return output

    async def get_volume(self) -> int:
        volume = self.parse_volume(await self._pactl("get-sink-volume", self.SINK))
        if volume is None:
            raise RuntimeError("pactl reported no volume for the default sink")
        return volume

    async def set_volume(self, percent: int) -> int:
        percent = max(0, min(100, percent))
        await self._pactl("set-sink-volume", self.SINK, f"{percent}%")
        await self._pactl("set-sink-mute", self.SINK, "0")
        # Re-read so callers see what the server actually applied.
        return await self.get_volume()

    async def adjust_volume(self, direction: str, step: int = 10) -> int:
        current = await self.get_volume()
        delta = step if direction == "up" else -step
        return await self.set_volume(current + delta)


class PcmBridge:
    """Owns one capture process and one playback process bridging raw PCM16.

    Subclasses supply the command lines; the lifecycle — startup, barge-in
    playback restart, teardown, and error surfacing — is shared."""

    def __init__(
        self, config: Config, input_device: str | None, output_device: str | None
    ) -> None:
        self.config = config
        self.input_device = input_device
        self.output_device = output_device
        self.capture: asyncio.subprocess.Process | None = None
        self.playback: asyncio.subprocess.Process | None = None

    def capture_command(self) -> list[str]:
        raise NotImplementedError

    def playback_command(self) -> list[str]:
        raise NotImplementedError

    async def __aenter__(self) -> "PcmBridge":
        self.capture = await asyncio.create_subprocess_exec(
            *self.capture_command(),
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        await self._start_playback()
        return self

    async def _start_playback(self) -> None:
        self.playback = await asyncio.create_subprocess_exec(
            *self.playback_command(),
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
        raise RuntimeError(
            f"microphone capture stopped: {error or self.capture_command()[0] + ' exited'}"
        )

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


class AlsaAudio(PcmBridge):
    """arecord/aplay bridge for hosts that expose only /dev/snd."""

    def _common_args(self, device: str | None) -> list[str]:
        return [
            "-q",
            "-D",
            device or "default",
            "-t",
            "raw",
            "-f",
            "S16_LE",
            "-c",
            "1",
            "-r",
            str(self.config.sample_rate),
        ]

    def capture_command(self) -> list[str]:
        return ["arecord", *self._common_args(self.input_device)]

    def playback_command(self) -> list[str]:
        return ["aplay", *self._common_args(self.output_device)]


class PipewireAudio(PcmBridge):
    """pw-record/pw-play bridge over the session socket the audio entitlement
    mounts. Targets default to WirePlumber's default source/sink — which is
    how Bluetooth devices are reached; an explicit AUDIO_*_DEVICE names a
    PipeWire node (object serial or node.name) instead of an ALSA PCM.

    The flag set matches the agent's own capture invocation
    (audio_service.go startCapture): raw s16 mono on stdio."""

    def _common_args(self, device: str | None) -> list[str]:
        args = [
            "--rate",
            str(self.config.sample_rate),
            "--channels",
            "1",
            "--format",
            "s16",
            "--raw",
        ]
        if device:
            args += ["--target", device]
        return args + ["-"]

    def capture_command(self) -> list[str]:
        return ["pw-record", *self._common_args(self.input_device)]

    def playback_command(self) -> list[str]:
        return ["pw-play", *self._common_args(self.output_device)]


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
        self.http_json = http_json
        self.tool_tasks: set[asyncio.Task[None]] = set()

    def tool_specs(self) -> list[dict[str, Any]]:
        specs: list[dict[str, Any]] = [
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
            {
                "type": "function",
                "name": "get_weather",
                "description": (
                    "Get the current weather and a 3-day forecast for a named "
                    "place. Fast and free — always use this for weather "
                    "questions instead of web_search."
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "location": {
                            "type": "string",
                            "description": (
                                "City or place name, e.g. 'Berlin' or "
                                "'Portland, Oregon'"
                            ),
                        },
                        "units": {
                            "type": "string",
                            "enum": ["celsius", "fahrenheit"],
                            "description": (
                                "Temperature unit the user expects (fahrenheit "
                                "for US locations). Default celsius."
                            ),
                        },
                    },
                    "required": ["location"],
                },
            },
        ]
        if self.config.web_search_enabled:
            specs.append(
                {
                    "type": "function",
                    "name": "web_search",
                    "description": (
                        "Search the live web for current information: news, "
                        "sports scores, prices, or facts you are not sure "
                        "about. Slower and costs money — never use it for "
                        "weather (use get_weather) or for things you already "
                        "know."
                    ),
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "query": {
                                "type": "string",
                                "description": "A concise search query",
                            }
                        },
                        "required": ["query"],
                    },
                }
            )
        return specs

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
        # gating, transcript fragments, or in-flight tool calls into the
        # replacement session.
        if self.playback_done_task is not None:
            self.playback_done_task.cancel()
            self.playback_done_task = None
        for task in list(self.tool_tasks):
            task.cancel()
        self.tool_tasks.clear()
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

        backend = select_audio_backend()
        if backend == "pipewire":
            # WirePlumber routes the default source/sink, so no card scan:
            # empty devices mean "default", explicit ones name PipeWire nodes.
            input_device = self.config.input_device
            output_device = self.config.output_device
            if shutil.which("pactl"):
                self.mixer = PulseMixer()
                await self.apply_startup_volume(self.mixer)
            else:
                self.mixer = None
                LOG.warning("pactl not found; volume control disabled")
            bridge: PcmBridge = PipewireAudio(self.config, input_device, output_device)
        else:
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
            bridge = AlsaAudio(self.config, input_device, output_device)

        LOG.info(
            "Opening Realtime session (%s) via %s; mic=%s%s speaker=%s%s",
            self.config.model,
            backend,
            input_device or "default",
            "" if self.config.input_device else " (auto)",
            output_device or "default",
            "" if self.config.output_device else " (auto)",
        )
        async with bridge as audio:
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

    async def send_microphone(self, websocket: Any, audio: PcmBridge) -> None:
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

    async def receive_events(self, websocket: Any, audio: PcmBridge) -> None:
        async for message in websocket:
            event = json.loads(message)
            await self.handle_event(event, audio, websocket)

    async def handle_event(
        self, event: dict[str, Any], audio: PcmBridge, websocket: Any | None = None
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
                # Run the tool concurrently: a slow network fetch must not stall
                # this receive loop, or barge-in (speech_started) would go
                # unnoticed until the fetch finished.
                task = asyncio.create_task(self.handle_tool_call(item, websocket))
                self.tool_tasks.add(task)
                task.add_done_callback(self._finish_tool_task)
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
            # prevents opening the microphone while the audio server still has
            # buffered audio.
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

    async def get_weather(
        self, location: str, units: str = "celsius"
    ) -> dict[str, Any]:
        location = location.strip()
        if not location:
            raise ValueError("location is required")
        if units not in ("celsius", "fahrenheit"):
            raise ValueError(f"units must be 'celsius' or 'fahrenheit', got {units!r}")
        query = urlencode(
            {"name": location, "count": 1, "language": "en", "format": "json"}
        )
        geocoded = await self.http_json(f"{GEOCODING_URL}?{query}")
        places = geocoded.get("results") or []
        if not places:
            return {"error": f"no location found matching {location!r}"}
        place = places[0]
        params: dict[str, Any] = {
            "latitude": place.get("latitude"),
            "longitude": place.get("longitude"),
            "current": (
                "temperature_2m,apparent_temperature,relative_humidity_2m,"
                "weather_code,wind_speed_10m,is_day"
            ),
            "daily": (
                "weather_code,temperature_2m_max,temperature_2m_min,"
                "precipitation_probability_max"
            ),
            "forecast_days": 3,
            "timezone": "auto",
        }
        if units == "fahrenheit":
            params["temperature_unit"] = "fahrenheit"
            params["wind_speed_unit"] = "mph"
        forecast = await self.http_json(f"{FORECAST_URL}?{urlencode(params)}")
        current = forecast.get("current") or {}
        daily = forecast.get("daily") or {}

        def column(key: str, index: int) -> Any:
            values = daily.get(key) or []
            return values[index] if index < len(values) else None

        # "Berlin, Berlin, Germany" reads badly aloud: city-states repeat the
        # name in admin1, so keep only distinct parts.
        location_parts: list[str] = []
        for part in (place.get("name"), place.get("admin1"), place.get("country")):
            if part and str(part) not in location_parts:
                location_parts.append(str(part))
        return {
            "location": ", ".join(location_parts),
            "units": units,
            "current": {
                "temperature": current.get("temperature_2m"),
                "feels_like": current.get("apparent_temperature"),
                "humidity_percent": current.get("relative_humidity_2m"),
                "wind_speed": current.get("wind_speed_10m"),
                "conditions": describe_weather_code(current.get("weather_code")),
            },
            "daily": [
                {
                    "date": date,
                    "high": column("temperature_2m_max", i),
                    "low": column("temperature_2m_min", i),
                    "conditions": describe_weather_code(column("weather_code", i)),
                    "precipitation_chance_percent": column(
                        "precipitation_probability_max", i
                    ),
                }
                for i, date in enumerate(daily.get("time") or [])
            ],
        }

    async def web_search(self, query: str) -> dict[str, Any]:
        query = query.strip()
        if not query:
            raise ValueError("query is required")
        LOG.info("Searching the web: %s", query)
        body: dict[str, Any] = {
            "model": self.config.search_model,
            "input": query,
            "instructions": (
                "You are a silent search backend for a voice assistant; the "
                "assistant has already acknowledged the user. Reply with 1-3 "
                "short spoken-style sentences of concrete facts, current as "
                "of today. Start directly with the facts — no greetings, "
                "acknowledgements, or lead-ins like 'Got it' or 'Here's "
                "what's going on'. No markdown or URLs. Never ask clarifying "
                "questions — this is a one-shot search, so pick the most "
                "likely interpretation of the query, search, and answer."
            ),
            "tools": [{"type": "web_search"}],
        }
        if self.config.search_model.startswith("gpt-5"):
            body["reasoning"] = {"effort": "low"}
        data = await self.http_json(
            OPENAI_RESPONSES_URL,
            method="POST",
            headers={"Authorization": f"Bearer {self.config.api_key}"},
            body=body,
            timeout=45,
        )
        answer = strip_citations(extract_output_text(data)).strip()
        if not answer:
            raise RuntimeError(
                f"web search returned no answer (status: {data.get('status', 'unknown')})"
            )
        return {"answer": answer}

    async def execute_tool(self, name: str, arguments_json: str) -> dict[str, Any]:
        """Run one tool; always returns a JSON-safe payload, never raises — a
        hardware or network failure must not kill the session."""
        try:
            arguments = json.loads(arguments_json) if arguments_json.strip() else {}
            if not isinstance(arguments, dict):
                raise ValueError("arguments must be a JSON object")
            if name == "get_weather":
                return await self.get_weather(
                    str(arguments.get("location", "")),
                    units=str(arguments.get("units") or "celsius"),
                )
            if name == "web_search":
                if not self.config.web_search_enabled:
                    return {"error": "web search is disabled on this device"}
                return await self.web_search(str(arguments.get("query", "")))
            if name not in ("set_volume", "adjust_volume", "get_volume"):
                return {"error": f"unknown tool: {name}"}
            if self.mixer is None:
                return {"error": "volume control is unavailable on this audio device"}
            if name == "set_volume":
                applied = await self.mixer.set_volume(int(arguments["percent"]))
            elif name == "adjust_volume":
                direction = arguments["direction"]
                if direction not in ("up", "down"):
                    raise ValueError(f"direction must be 'up' or 'down', got {direction!r}")
                applied = await self.mixer.adjust_volume(
                    direction, step=int(arguments.get("step", 10))
                )
            else:
                applied = await self.mixer.get_volume()
            return {"volume_percent": applied}
        except Exception as exc:
            return {"error": str(exc)}

    def _finish_tool_task(self, task: asyncio.Task[None]) -> None:
        self.tool_tasks.discard(task)
        if not task.cancelled() and task.exception() is not None:
            # execute_tool never raises; this is a websocket send failure.
            LOG.warning("Tool result was not delivered: %s", task.exception())

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

    async def interrupt_response(self, websocket: Any | None, audio: PcmBridge) -> None:
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
