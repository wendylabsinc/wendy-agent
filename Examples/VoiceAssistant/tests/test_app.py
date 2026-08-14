import asyncio
import base64
import json
import os
import unittest
from unittest.mock import patch

import app
from app import (
    AlsaMixer,
    Config,
    PipewireAudio,
    PulseMixer,
    VoiceAssistant,
    card_from_device,
    describe_weather_code,
    extract_output_text,
    select_audio_backend,
    strip_citations,
    parse_alsa_cards,
    pick_alsa_device,
)

APLAY_L_PI = """\
**** List of PLAYBACK Hardware Devices ****
card 0: vc4hdmi0 [vc4-hdmi-0], device 0: MAI PCM i2s-hifi-0 [MAI PCM i2s-hifi-0]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 1: vc4hdmi1 [vc4-hdmi-1], device 0: MAI PCM i2s-hifi-0 [MAI PCM i2s-hifi-0]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
card 2: Device [USB Audio Device], device 0: USB Audio [USB Audio]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
"""

ARECORD_L_PI = """\
**** List of CAPTURE Hardware Devices ****
card 2: Device [USB Audio Device], device 0: USB Audio [USB Audio]
  Subdevices: 1/1
  Subdevice #0: subdevice #0
"""

SCONTROLS_USB = """\
Simple mixer control 'Mic',0
Simple mixer control 'Speaker',0
"""

SGET_SPEAKER_57 = """\
Simple mixer control 'Speaker',0
  Capabilities: pvolume pswitch
  Playback channels: Front Left - Front Right
  Limits: Playback 0 - 151
  Front Left: Playback 86 [57%] [-19.42dB] [on]
  Front Right: Playback 86 [57%] [-19.42dB] [on]
"""

SGET_MIC_CAPTURE_ONLY = """\
Simple mixer control 'Mic',0
  Capabilities: cvolume cswitch
  Capture channels: Mono
  Limits: Capture 0 - 16
  Mono: Capture 12 [75%] [30.00dB] [on]
"""

SSET_SPEAKER_29 = """\
Simple mixer control 'Speaker',0
  Capabilities: pvolume pswitch
  Playback channels: Front Left - Front Right
  Limits: Playback 0 - 151
  Front Left: Playback 44 [29%] [-32.34dB] [on]
  Front Right: Playback 44 [29%] [-32.34dB] [on]
"""


class FakeAmixer:
    """Injectable runner mimicking `amixer -c <card> <args...>`."""

    def __init__(self, responses):
        self.responses = responses  # first arg (subcommand) -> callable(args) or (rc, out)
        self.calls = []

    async def __call__(self, *args):
        self.calls.append(args)
        response = self.responses[args[0]]
        if callable(response):
            return response(args)
        return response


class FakeAudio:
    def __init__(self):
        self.writes = []
        self.interruptions = 0

    async def write(self, audio):
        self.writes.append(audio)

    async def interrupt_playback(self):
        self.interruptions += 1


class FakeWebSocket:
    def __init__(self):
        self.sent = []

    async def send(self, message):
        self.sent.append(json.loads(message))


class FakeFetch:
    """Injectable stand-in for app.http_json, keyed by URL prefix."""

    def __init__(self, responses):
        self.responses = responses  # url prefix -> dict, Exception, or callable
        self.calls = []

    async def __call__(self, url, **kwargs):
        self.calls.append((url, kwargs))
        for prefix, response in self.responses.items():
            if url.startswith(prefix):
                if isinstance(response, Exception):
                    raise response
                if callable(response):
                    return response(url, kwargs)
                return response
        raise AssertionError(f"unexpected fetch: {url}")


async def _drain_tools(assistant):
    while assistant.tool_tasks:
        await asyncio.gather(*list(assistant.tool_tasks), return_exceptions=True)


GEOCODE_BERLIN = {
    "results": [
        {
            "name": "Berlin",
            "latitude": 52.52,
            "longitude": 13.41,
            "country": "Germany",
            "admin1": "Berlin",
        }
    ]
}

FORECAST_BERLIN = {
    "current": {
        "temperature_2m": 18.3,
        "apparent_temperature": 17.1,
        "relative_humidity_2m": 65,
        "weather_code": 61,
        "wind_speed_10m": 12.5,
        "is_day": 1,
    },
    "daily": {
        "time": ["2026-08-05", "2026-08-06", "2026-08-07"],
        "weather_code": [61, 3, 0],
        "temperature_2m_max": [21.0, 24.5, 26.1],
        "temperature_2m_min": [14.2, 15.0, 16.3],
        "precipitation_probability_max": [80, 20, 5],
    },
}

RESPONSES_ANSWER = {
    "status": "completed",
    "output": [
        {"type": "web_search_call", "id": "ws_1", "status": "completed"},
        {
            "type": "message",
            "content": [
                {"type": "output_text", "text": "The Giants won 4-2 last night."}
            ],
        },
    ],
}


class OneChunkAudio:
    def __init__(self, chunk):
        self.chunk = chunk
        self.reads = 0

    async def read(self):
        self.reads += 1
        if self.reads == 1:
            return self.chunk
        raise asyncio.CancelledError


class ConfigTests(unittest.TestCase):
    def test_config_requires_api_key(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "OPENAI_API_KEY"):
                Config.from_env()

    def test_audio_chunk_is_100ms_of_pcm16_by_default(self):
        config = Config(api_key="test")
        self.assertEqual(config.chunk_bytes, 4_800)
        self.assertIsNone(config.input_device)
        self.assertIsNone(config.output_device)
        self.assertFalse(config.mute_input_during_playback)

    def test_startup_volume_defaults_to_70(self):
        with patch.dict(os.environ, {"OPENAI_API_KEY": "test"}, clear=True):
            self.assertEqual(Config.from_env().startup_volume_percent, 70)
        with patch.dict(
            os.environ,
            {"OPENAI_API_KEY": "test", "STARTUP_VOLUME_PERCENT": ""},
            clear=True,
        ):
            self.assertEqual(Config.from_env().startup_volume_percent, 70)

    def test_startup_volume_off_disables_normalization(self):
        for value in ("off", "OFF", "false", "no", "none", "disabled"):
            with patch.dict(
                os.environ,
                {"OPENAI_API_KEY": "test", "STARTUP_VOLUME_PERCENT": value},
                clear=True,
            ):
                self.assertIsNone(Config.from_env().startup_volume_percent)

    def test_startup_volume_out_of_range_is_rejected(self):
        with patch.dict(
            os.environ,
            {"OPENAI_API_KEY": "test", "STARTUP_VOLUME_PERCENT": "150"},
            clear=True,
        ):
            with self.assertRaisesRegex(ValueError, "STARTUP_VOLUME_PERCENT"):
                Config.from_env()

    def test_search_config_defaults(self):
        with patch.dict(os.environ, {"OPENAI_API_KEY": "test"}, clear=True):
            config = Config.from_env()
        self.assertEqual(config.search_model, "gpt-5-mini")
        self.assertTrue(config.web_search_enabled)
        self.assertIn("weather", config.instructions)
        self.assertIn("search the web", config.instructions)

    def test_search_config_overrides(self):
        with patch.dict(
            os.environ,
            {
                "OPENAI_API_KEY": "test",
                "OPENAI_SEARCH_MODEL": "gpt-4.1-mini",
                "WEB_SEARCH_ENABLED": "false",
            },
            clear=True,
        ):
            config = Config.from_env()
        self.assertEqual(config.search_model, "gpt-4.1-mini")
        self.assertFalse(config.web_search_enabled)

    def test_unset_audio_devices_mean_auto_detect(self):
        with patch.dict(
            os.environ,
            {"OPENAI_API_KEY": "test", "AUDIO_INPUT_DEVICE": ""},
            clear=True,
        ):
            config = Config.from_env()
        self.assertIsNone(config.input_device)
        self.assertIsNone(config.output_device)


class AlsaDetectionTests(unittest.TestCase):
    def test_parse_alsa_cards_reads_number_name_description_and_first_device(self):
        cards = parse_alsa_cards(APLAY_L_PI)
        self.assertEqual([card.number for card in cards], [0, 1, 2])
        self.assertEqual(cards[2].name, "Device")
        self.assertEqual(cards[2].device, 0)
        self.assertIn("USB Audio Device", cards[2].description)

    def test_parse_alsa_cards_keeps_first_device_per_card(self):
        listing = (
            "**** List of PLAYBACK Hardware Devices ****\n"
            "card 0: Headset [USB Headset], device 2: USB Audio [USB Audio]\n"
            "card 0: Headset [USB Headset], device 3: USB Audio [USB Audio #1]\n"
        )
        cards = parse_alsa_cards(listing)
        self.assertEqual(len(cards), 1)
        self.assertEqual(cards[0].device, 2)

    def test_parse_alsa_cards_of_empty_listing(self):
        self.assertEqual(parse_alsa_cards(""), [])
        self.assertEqual(parse_alsa_cards("no soundcards found...\n"), [])

    def test_pick_alsa_device_finds_usb_named_only_in_device_column(self):
        # snd-usb-audio always names the device "USB Audio", but the card
        # description bracket is the product string, which often lacks "usb".
        listing = (
            "**** List of PLAYBACK Hardware Devices ****\n"
            "card 0: vc4hdmi0 [vc4-hdmi-0], device 0: MAI PCM i2s-hifi-0 [MAI PCM]\n"
            "card 2: S330 [Anker PowerConf S330], device 0: USB Audio [USB Audio]\n"
        )
        self.assertEqual(
            pick_alsa_device(parse_alsa_cards(listing)),
            "plughw:CARD=S330,DEV=0",
        )
        yeti = (
            "**** List of CAPTURE Hardware Devices ****\n"
            "card 1: Microphone [Yeti Stereo Microphone], device 0: USB Audio [USB Audio]\n"
        )
        self.assertEqual(
            pick_alsa_device(parse_alsa_cards(yeti)),
            "plughw:CARD=Microphone,DEV=0",
        )

    def test_pick_alsa_device_prefers_first_usb_card_with_stable_name(self):
        self.assertEqual(
            pick_alsa_device(parse_alsa_cards(APLAY_L_PI)),
            "plughw:CARD=Device,DEV=0",
        )
        self.assertEqual(
            pick_alsa_device(parse_alsa_cards(ARECORD_L_PI)),
            "plughw:CARD=Device,DEV=0",
        )

    def test_pick_alsa_device_without_usb_card_returns_none(self):
        hdmi_only = (
            "**** List of PLAYBACK Hardware Devices ****\n"
            "card 0: vc4hdmi0 [vc4-hdmi-0], device 0: MAI PCM i2s-hifi-0 [MAI PCM]\n"
        )
        self.assertIsNone(pick_alsa_device(parse_alsa_cards(hdmi_only)))
        self.assertIsNone(pick_alsa_device([]))

    def test_detect_device_returns_usb_pick_or_default(self):
        async def check():
            async def usb_runner(*args):
                return 0, ARECORD_L_PI

            async def hdmi_runner(*args):
                return 0, "card 0: vc4hdmi0 [vc4-hdmi-0], device 0: MAI [MAI]\n"

            async def failing_runner(*args):
                return 1, "arecord: device_list:274: no soundcards found..."

            self.assertEqual(
                await app.detect_device("arecord", run=usb_runner),
                "plughw:CARD=Device,DEV=0",
            )
            self.assertEqual(await app.detect_device("aplay", run=hdmi_runner), "default")
            self.assertEqual(await app.detect_device("aplay", run=failing_runner), "default")

        asyncio.run(check())

    def test_card_from_device_forms(self):
        self.assertEqual(card_from_device("plughw:2,0"), "2")
        self.assertEqual(card_from_device("hw:2,0"), "2")
        self.assertEqual(card_from_device("plughw:CARD=Device,DEV=0"), "Device")
        self.assertIsNone(card_from_device("default"))

    def test_empty_mute_env_uses_interruptible_default(self):
        with patch.dict(
            os.environ,
            {"OPENAI_API_KEY": "test", "MUTE_INPUT_DURING_PLAYBACK": ""},
            clear=True,
        ):
            self.assertFalse(Config.from_env().mute_input_during_playback)

    def test_session_update_uses_current_ga_shape(self):
        event = VoiceAssistant(Config(api_key="secret")).session_update_event()
        session = event["session"]
        self.assertEqual(event["type"], "session.update")
        self.assertEqual(session["type"], "realtime")
        self.assertEqual(session["output_modalities"], ["audio"])
        self.assertEqual(session["audio"]["input"]["format"]["rate"], 24_000)
        self.assertEqual(session["audio"]["output"]["format"]["rate"], 24_000)
        turn_detection = session["audio"]["input"]["turn_detection"]
        self.assertEqual(turn_detection["type"], "semantic_vad")
        self.assertTrue(turn_detection["interrupt_response"])
        self.assertNotIn("secret", repr(event))


class AlsaMixerTests(unittest.IsolatedAsyncioTestCase):
    def test_parse_controls_reads_simple_mixer_control_names(self):
        self.assertEqual(AlsaMixer.parse_controls(SCONTROLS_USB), ["Mic", "Speaker"])
        self.assertEqual(AlsaMixer.parse_controls(""), [])

    def test_order_controls_puts_preferred_playback_controls_first(self):
        self.assertEqual(
            AlsaMixer.order_controls(["Mic", "Speaker", "PCM"]),
            ["PCM", "Speaker", "Mic"],
        )
        self.assertEqual(
            AlsaMixer.order_controls(["mic", "master"]),
            ["master", "mic"],
        )

    def test_parse_playback_volume_only_accepts_playback_lines(self):
        self.assertEqual(AlsaMixer.parse_playback_volume(SGET_SPEAKER_57), 57)
        self.assertIsNone(AlsaMixer.parse_playback_volume(SGET_MIC_CAPTURE_ONLY))
        self.assertIsNone(
            AlsaMixer.parse_playback_volume("Front Left: Playback 200 [101%] [on]")
        )

    async def test_resolve_control_skips_controls_without_playback_volume(self):
        # Non-preferred names keep the original order, so the capture-only
        # control is probed first and must be skipped.
        scontrols = (
            "Simple mixer control 'Mic',0\n"
            "Simple mixer control 'Custom',0\n"
        )
        runner = FakeAmixer(
            {
                "scontrols": (0, scontrols),
                "sget": lambda args: (0, SGET_MIC_CAPTURE_ONLY)
                if args[1] == "Mic"
                else (0, SGET_SPEAKER_57),
            }
        )
        mixer = AlsaMixer("Device", run=runner)
        self.assertEqual(await mixer.resolve_control(), ("Custom", 57))
        self.assertIn(("sget", "Mic"), runner.calls)

    async def test_resolve_control_skips_controls_whose_sget_fails(self):
        # Go parity: a failing sget on one control must not abort resolution
        # (audio_service.go continues to the next control).
        scontrols = (
            "Simple mixer control 'Master',0\n"
            "Simple mixer control 'Speaker',0\n"
        )
        runner = FakeAmixer(
            {
                "scontrols": (0, scontrols),
                "sget": lambda args: (1, "amixer: Mixer attach error")
                if args[1] == "Master"
                else (0, SGET_SPEAKER_57),
            }
        )
        mixer = AlsaMixer("Device", run=runner)
        self.assertEqual(await mixer.resolve_control(), ("Speaker", 57))

    async def test_set_volume_unmutes_and_returns_quantized_actual(self):
        runner = FakeAmixer(
            {
                "scontrols": (0, SCONTROLS_USB),
                "sget": (0, SGET_SPEAKER_57),
                "sset": (0, SSET_SPEAKER_29),
            }
        )
        mixer = AlsaMixer("2", run=runner)
        self.assertEqual(await mixer.set_volume(30), 29)
        self.assertIn(("sset", "Speaker", "30%", "unmute"), runner.calls)

    async def test_adjust_volume_steps_and_clamps(self):
        runner = FakeAmixer(
            {
                "scontrols": (0, SCONTROLS_USB),
                "sget": (0, SGET_SPEAKER_57),
                "sset": lambda args: (
                    0,
                    SSET_SPEAKER_29.replace("[29%]", f"[{args[2].rstrip('%')}%]"),
                ),
            }
        )
        mixer = AlsaMixer("2", run=runner)
        self.assertEqual(await mixer.adjust_volume("up"), 67)
        self.assertEqual(await mixer.adjust_volume("down", step=100), 0)

    async def test_mixer_errors_raise_runtime_error(self):
        runner = FakeAmixer({"scontrols": (1, "Invalid card number")})
        mixer = AlsaMixer("9", run=runner)
        with self.assertRaises(RuntimeError):
            await mixer.set_volume(50)


PACTL_GET_SINK_VOLUME_60 = """\
Volume: front-left: 39322 /  60% / -13.31 dB,   front-right: 39322 /  60% / -13.31 dB
        balance 0.00
"""


class FakePactl:
    """Injectable runner mimicking `pactl <subcommand> <args...>`."""

    def __init__(self, responses):
        self.responses = responses  # subcommand -> callable(args) or (rc, out)
        self.calls = []

    async def __call__(self, *args):
        self.calls.append(args)
        response = self.responses[args[0]]
        if callable(response):
            return response(args)
        return response


class BackendSelectionTests(unittest.TestCase):
    def test_without_a_pipewire_socket_alsa_is_used(self):
        with patch.dict(
            os.environ, {"PIPEWIRE_RUNTIME_DIR": "", "XDG_RUNTIME_DIR": ""}
        ):
            self.assertEqual(select_audio_backend(which=lambda _: "/usr/bin/x"), "alsa")

    def test_mounted_socket_with_tools_selects_pipewire(self):
        import tempfile

        with tempfile.TemporaryDirectory() as runtime_dir:
            open(os.path.join(runtime_dir, "pipewire-0"), "w").close()
            with patch.dict(os.environ, {"PIPEWIRE_RUNTIME_DIR": runtime_dir}):
                self.assertEqual(
                    select_audio_backend(which=lambda _: "/usr/bin/x"), "pipewire"
                )

    def test_mounted_socket_without_pw_tools_falls_back_to_alsa(self):
        # The HelloAudio failure mode: entitlement mounted the socket but the
        # image was built without pipewire-bin. Must not crash-loop on ENOENT.
        import tempfile

        with tempfile.TemporaryDirectory() as runtime_dir:
            open(os.path.join(runtime_dir, "pipewire-0"), "w").close()
            with patch.dict(os.environ, {"PIPEWIRE_RUNTIME_DIR": runtime_dir}):
                self.assertEqual(select_audio_backend(which=lambda _: None), "alsa")

    def test_env_set_but_socket_missing_falls_back_to_alsa(self):
        import tempfile

        with tempfile.TemporaryDirectory() as runtime_dir:
            with patch.dict(
                os.environ,
                {"PIPEWIRE_RUNTIME_DIR": runtime_dir, "XDG_RUNTIME_DIR": ""},
            ):
                self.assertEqual(
                    select_audio_backend(which=lambda _: "/usr/bin/x"), "alsa"
                )


class PipewireAudioTests(unittest.TestCase):
    def test_default_targets_use_wireplumber_routing(self):
        bridge = PipewireAudio(Config(api_key="k"), None, None)
        self.assertEqual(
            bridge.capture_command(),
            [
                "pw-record",
                "--rate", "24000",
                "--channels", "1",
                "--format", "s16",
                "--raw",
                "-",
            ],
        )
        self.assertEqual(bridge.playback_command()[0], "pw-play")
        self.assertNotIn("--target", bridge.playback_command())

    def test_explicit_devices_become_pipewire_targets(self):
        bridge = PipewireAudio(
            Config(api_key="k"), "bluez_input.00_7F_1D_51_A9_6E", "bluez_output.speaker"
        )
        capture = bridge.capture_command()
        playback = bridge.playback_command()
        self.assertIn("bluez_input.00_7F_1D_51_A9_6E", capture)
        self.assertEqual(capture[capture.index("--target") + 1], "bluez_input.00_7F_1D_51_A9_6E")
        self.assertEqual(playback[playback.index("--target") + 1], "bluez_output.speaker")
        # Raw streams on stdio, matching the agent's own pw-record invocation.
        self.assertEqual(capture[-1], "-")
        self.assertIn("--raw", playback)


class PulseMixerTests(unittest.IsolatedAsyncioTestCase):
    def test_parse_volume_reads_the_first_percentage(self):
        self.assertEqual(PulseMixer.parse_volume(PACTL_GET_SINK_VOLUME_60), 60)
        self.assertIsNone(PulseMixer.parse_volume("balance 0.00"))
        self.assertIsNone(PulseMixer.parse_volume(""))

    async def test_set_volume_clamps_unmutes_and_rereads(self):
        runner = FakePactl(
            {
                "get-sink-volume": (0, PACTL_GET_SINK_VOLUME_60),
                "set-sink-volume": (0, ""),
                "set-sink-mute": (0, ""),
            }
        )
        mixer = PulseMixer(run=runner)
        self.assertEqual(await mixer.set_volume(150), 60)
        self.assertIn(("set-sink-volume", "@DEFAULT_SINK@", "100%"), runner.calls)
        self.assertIn(("set-sink-mute", "@DEFAULT_SINK@", "0"), runner.calls)

    async def test_adjust_volume_steps_from_current(self):
        runner = FakePactl(
            {
                "get-sink-volume": (0, PACTL_GET_SINK_VOLUME_60),
                "set-sink-volume": (0, ""),
                "set-sink-mute": (0, ""),
            }
        )
        mixer = PulseMixer(run=runner)
        await mixer.adjust_volume("down", step=25)
        self.assertIn(("set-sink-volume", "@DEFAULT_SINK@", "35%"), runner.calls)

    async def test_pactl_failure_raises_runtime_error(self):
        runner = FakePactl({"get-sink-volume": (1, "Connection refused")})
        mixer = PulseMixer(run=runner)
        with self.assertRaises(RuntimeError):
            await mixer.get_volume()


class FakeMixer:
    def __init__(self, fail=False, volume=57):
        self.fail = fail
        self.volume = volume
        self.set_calls = []
        self.adjust_calls = []

    async def set_volume(self, percent):
        if self.fail:
            raise RuntimeError("no playback volume control found")
        self.set_calls.append(percent)
        self.volume = percent
        return percent

    async def get_volume(self):
        if self.fail:
            raise RuntimeError("no playback volume control found")
        return self.volume

    async def adjust_volume(self, direction, step=10):
        self.adjust_calls.append((direction, step))
        self.volume += step if direction == "up" else -step
        return self.volume

    async def resolve_control(self):
        return ("Speaker", self.volume)


class StartupVolumeTests(unittest.IsolatedAsyncioTestCase):
    async def test_startup_volume_applied_once_per_process(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        mixer = FakeMixer()
        await assistant.apply_startup_volume(mixer)
        await assistant.apply_startup_volume(mixer)
        self.assertEqual(mixer.set_calls, [70])

    async def test_startup_volume_disabled_never_touches_mixer(self):
        assistant = VoiceAssistant(
            Config(api_key="test", startup_volume_percent=None)
        )
        mixer = FakeMixer()
        await assistant.apply_startup_volume(mixer)
        self.assertEqual(mixer.set_calls, [])

    async def test_startup_volume_failure_is_nonfatal(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        with self.assertLogs("voice-assistant", level="WARNING"):
            await assistant.apply_startup_volume(FakeMixer(fail=True))

    async def test_startup_volume_retries_after_failure(self):
        # USB devices can enumerate late; only a successful set should latch.
        assistant = VoiceAssistant(Config(api_key="test"))
        with self.assertLogs("voice-assistant", level="WARNING"):
            await assistant.apply_startup_volume(FakeMixer(fail=True))
        mixer = FakeMixer()
        await assistant.apply_startup_volume(mixer)
        self.assertEqual(mixer.set_calls, [70])


class WebHelperTests(unittest.TestCase):
    def test_describe_weather_code_maps_known_and_unknown_codes(self):
        self.assertEqual(describe_weather_code(0), "clear sky")
        self.assertEqual(describe_weather_code(61), "light rain")
        self.assertEqual(describe_weather_code(999), "unknown conditions")
        self.assertEqual(describe_weather_code(None), "unknown conditions")

    def test_extract_output_text_concatenates_message_output_text(self):
        payload = {
            "status": "completed",
            "output": [
                {"type": "web_search_call", "id": "ws_1", "status": "completed"},
                {
                    "type": "message",
                    "content": [
                        {"type": "output_text", "text": "It is sunny."},
                        {"type": "output_text", "text": " High of 20."},
                    ],
                },
            ],
        }
        self.assertEqual(extract_output_text(payload), "It is sunny. High of 20.")

    def test_extract_output_text_of_empty_or_tool_only_payloads(self):
        self.assertEqual(extract_output_text({}), "")
        self.assertEqual(
            extract_output_text({"output": [{"type": "web_search_call"}]}), ""
        )

    def test_strip_citations_removes_web_search_annotations(self):
        # Live web_search answers embed citations despite the no-URLs
        # instruction; they must not reach the spoken reply.
        annotated = (
            "The rate is 3.50% to 3.75%. "
            "([federalreserve.gov](https://www.federalreserve.gov/a?utm_source=openai))"
            "\n\nReserves pay 3.65%. "
            "([a.com](https://a.com/x), [b.org](https://b.org/y))"
        )
        self.assertEqual(
            strip_citations(annotated),
            "The rate is 3.50% to 3.75%.\n\nReserves pay 3.65%.",
        )

    def test_strip_citations_unwraps_inline_links_and_keeps_plain_text(self):
        self.assertEqual(
            strip_citations("See [the Fed](https://fed.gov) for details."),
            "See the Fed for details.",
        )
        self.assertEqual(
            strip_citations("Plain text (with an aside) stays."),
            "Plain text (with an aside) stays.",
        )


def _function_call_done(name, arguments, call_id="call_1"):
    return {
        "type": "response.output_item.done",
        "item": {
            "type": "function_call",
            "name": name,
            "call_id": call_id,
            "arguments": arguments,
        },
    }


class ToolTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.assistant = VoiceAssistant(Config(api_key="test"))
        self.assistant.mixer = FakeMixer()
        self.audio = FakeAudio()
        self.websocket = FakeWebSocket()

    def test_session_update_registers_tools(self):
        session = self.assistant.session_update_event()["session"]
        self.assertEqual(session["tool_choice"], "auto")
        names = [tool["name"] for tool in session["tools"]]
        self.assertEqual(
            names,
            ["set_volume", "adjust_volume", "get_volume", "get_weather", "web_search"],
        )
        for tool in session["tools"]:
            self.assertEqual(tool["type"], "function")
            self.assertIn("parameters", tool)

    def test_web_search_tool_omitted_when_disabled(self):
        assistant = VoiceAssistant(Config(api_key="test", web_search_enabled=False))
        names = [
            tool["name"]
            for tool in assistant.session_update_event()["session"]["tools"]
        ]
        self.assertNotIn("web_search", names)
        self.assertIn("get_weather", names)

    async def test_set_volume_tool_roundtrip(self):
        await self.assistant.handle_event(
            _function_call_done("set_volume", json.dumps({"percent": 25})),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        self.assertEqual(self.assistant.mixer.set_calls, [25])
        output_event, response_event = self.websocket.sent
        self.assertEqual(output_event["type"], "conversation.item.create")
        item = output_event["item"]
        self.assertEqual(item["type"], "function_call_output")
        self.assertEqual(item["call_id"], "call_1")
        self.assertEqual(json.loads(item["output"]), {"volume_percent": 25})
        self.assertEqual(response_event, {"type": "response.create"})

    async def test_adjust_and_get_volume_tools(self):
        await self.assistant.handle_event(
            _function_call_done("adjust_volume", json.dumps({"direction": "up"})),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        self.assertEqual(self.assistant.mixer.adjust_calls, [("up", 10)])
        await self.assistant.handle_event(
            _function_call_done("get_volume", "{}", call_id="call_2"),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        outputs = [
            json.loads(event["item"]["output"])
            for event in self.websocket.sent
            if event["type"] == "conversation.item.create"
        ]
        self.assertEqual(outputs, [{"volume_percent": 67}, {"volume_percent": 67}])

    async def test_tool_reply_suppressed_while_user_speaking(self):
        self.assistant.user_speaking = True
        await self.assistant.handle_event(
            _function_call_done("set_volume", json.dumps({"percent": 25})),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        types = [event["type"] for event in self.websocket.sent]
        self.assertEqual(types, ["conversation.item.create"])

    async def test_speech_events_track_user_speaking(self):
        await self.assistant.handle_event(
            {"type": "input_audio_buffer.speech_started"}, self.audio, self.websocket
        )
        self.assertTrue(self.assistant.user_speaking)
        await self.assistant.handle_event(
            {"type": "input_audio_buffer.speech_stopped"}, self.audio, self.websocket
        )
        self.assertFalse(self.assistant.user_speaking)

    async def test_adjust_volume_rejects_invalid_direction(self):
        await self.assistant.handle_event(
            _function_call_done("adjust_volume", json.dumps({"direction": "sideways"})),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        self.assertEqual(self.assistant.mixer.adjust_calls, [])
        output = json.loads(self.websocket.sent[0]["item"]["output"])
        self.assertIn("error", output)

    async def test_tool_errors_become_error_payloads(self):
        self.assistant.mixer = FakeMixer(fail=True)
        await self.assistant.handle_event(
            _function_call_done("set_volume", json.dumps({"percent": 25})),
            self.audio,
            self.websocket,
        )
        self.assistant.mixer = None
        await self.assistant.handle_event(
            _function_call_done("get_volume", "{}", call_id="call_2"),
            self.audio,
            self.websocket,
        )
        await self.assistant.handle_event(
            _function_call_done("set_volume", "not json", call_id="call_3"),
            self.audio,
            self.websocket,
        )
        await self.assistant.handle_event(
            _function_call_done("unknown_tool", "{}", call_id="call_4"),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        outputs = [
            json.loads(event["item"]["output"])
            for event in self.websocket.sent
            if event["type"] == "conversation.item.create"
        ]
        self.assertEqual(len(outputs), 4)
        for output in outputs:
            self.assertIn("error", output)


class WebToolTestCase(unittest.IsolatedAsyncioTestCase):
    """Shared harness driving tools through handle_event like the live app."""

    def setUp(self):
        self.assistant = VoiceAssistant(Config(api_key="test"))
        self.assistant.mixer = FakeMixer()
        self.audio = FakeAudio()
        self.websocket = FakeWebSocket()

    async def call_tool(self, name, arguments):
        await self.assistant.handle_event(
            _function_call_done(name, json.dumps(arguments)),
            self.audio,
            self.websocket,
        )
        await _drain_tools(self.assistant)
        outputs = [
            json.loads(event["item"]["output"])
            for event in self.websocket.sent
            if event["type"] == "conversation.item.create"
        ]
        self.assertEqual(len(outputs), 1)
        return outputs[0]


class WeatherToolTests(WebToolTestCase):
    def use_fetch(self, geocode=GEOCODE_BERLIN, forecast=FORECAST_BERLIN):
        fetch = FakeFetch({app.GEOCODING_URL: geocode, app.FORECAST_URL: forecast})
        self.assistant.http_json = fetch
        return fetch

    async def test_get_weather_reports_current_and_daily_conditions(self):
        fetch = self.use_fetch()
        output = await self.call_tool("get_weather", {"location": "Berlin"})
        self.assertEqual(output["location"], "Berlin, Germany")
        self.assertEqual(output["units"], "celsius")
        self.assertEqual(output["current"]["temperature"], 18.3)
        self.assertEqual(output["current"]["conditions"], "light rain")
        self.assertEqual(len(output["daily"]), 3)
        self.assertEqual(output["daily"][0]["date"], "2026-08-05")
        self.assertEqual(output["daily"][0]["high"], 21.0)
        self.assertEqual(output["daily"][0]["precipitation_chance_percent"], 80)
        geocode_url, forecast_url = (call[0] for call in fetch.calls)
        self.assertIn("name=Berlin", geocode_url)
        self.assertIn("latitude=52.52", forecast_url)
        self.assertNotIn("temperature_unit", forecast_url)

    async def test_get_weather_fahrenheit_switches_units(self):
        fetch = self.use_fetch()
        output = await self.call_tool(
            "get_weather", {"location": "Berlin", "units": "fahrenheit"}
        )
        self.assertEqual(output["units"], "fahrenheit")
        forecast_url = fetch.calls[1][0]
        self.assertIn("temperature_unit=fahrenheit", forecast_url)
        self.assertIn("wind_speed_unit=mph", forecast_url)

    async def test_get_weather_unknown_location_is_a_soft_error(self):
        fetch = self.use_fetch(geocode={"results": []})
        output = await self.call_tool("get_weather", {"location": "Atlantis"})
        self.assertIn("Atlantis", output["error"])
        self.assertEqual(len(fetch.calls), 1)  # forecast never requested

    async def test_get_weather_http_failure_becomes_error_payload(self):
        self.assistant.http_json = FakeFetch(
            {app.GEOCODING_URL: RuntimeError("HTTP 500 from geocoding-api.open-meteo.com")}
        )
        output = await self.call_tool("get_weather", {"location": "Berlin"})
        self.assertIn("HTTP 500", output["error"])

    async def test_get_weather_works_without_a_mixer(self):
        # Network tools must not be blocked by the volume-control guard.
        self.assistant.mixer = None
        self.use_fetch()
        output = await self.call_tool("get_weather", {"location": "Berlin"})
        self.assertNotIn("error", output)

    async def test_get_weather_rejects_bad_arguments(self):
        fetch = self.use_fetch()
        output = await self.call_tool(
            "get_weather", {"location": "Berlin", "units": "kelvin"}
        )
        self.assertIn("units", output["error"])
        self.websocket.sent.clear()
        output = await self.call_tool("get_weather", {})
        self.assertIn("location", output["error"])
        self.assertEqual(fetch.calls, [])


class WebSearchToolTests(WebToolTestCase):
    async def test_web_search_posts_query_and_returns_answer(self):
        fetch = FakeFetch({app.OPENAI_RESPONSES_URL: RESPONSES_ANSWER})
        self.assistant.http_json = fetch
        output = await self.call_tool("web_search", {"query": "giants score"})
        self.assertEqual(output, {"answer": "The Giants won 4-2 last night."})
        url, kwargs = fetch.calls[0]
        self.assertEqual(url, app.OPENAI_RESPONSES_URL)
        self.assertEqual(kwargs["method"], "POST")
        self.assertEqual(kwargs["headers"], {"Authorization": "Bearer test"})
        body = kwargs["body"]
        self.assertEqual(body["model"], "gpt-5-mini")
        self.assertEqual(body["input"], "giants score")
        self.assertEqual(body["tools"], [{"type": "web_search"}])
        self.assertEqual(body["reasoning"], {"effort": "low"})
        # Live findings: without these the model answers broad queries with
        # clarifying questions instead of searching (looping the user), and
        # opens with acknowledgements that double up on the realtime model's
        # own spoken lead-in.
        self.assertIn("Never ask clarifying questions", body["instructions"])
        self.assertIn("Start directly with the facts", body["instructions"])

    async def test_web_search_omits_reasoning_for_non_gpt5_models(self):
        self.assistant = VoiceAssistant(
            Config(api_key="test", search_model="gpt-4.1-mini")
        )
        fetch = FakeFetch({app.OPENAI_RESPONSES_URL: RESPONSES_ANSWER})
        self.assistant.http_json = fetch
        await self.call_tool("web_search", {"query": "giants score"})
        body = fetch.calls[0][1]["body"]
        self.assertEqual(body["model"], "gpt-4.1-mini")
        self.assertNotIn("reasoning", body)

    async def test_web_search_answer_is_stripped_of_citations(self):
        annotated = {
            "status": "completed",
            "output": [
                {
                    "type": "message",
                    "content": [
                        {
                            "type": "output_text",
                            "text": "It is 3.65%. ([fed.gov](https://fed.gov/x))",
                        }
                    ],
                }
            ],
        }
        self.assistant.http_json = FakeFetch({app.OPENAI_RESPONSES_URL: annotated})
        output = await self.call_tool("web_search", {"query": "rate"})
        self.assertEqual(output, {"answer": "It is 3.65%."})

    async def test_web_search_empty_answer_becomes_error_payload(self):
        self.assistant.http_json = FakeFetch(
            {app.OPENAI_RESPONSES_URL: {"status": "incomplete", "output": []}}
        )
        output = await self.call_tool("web_search", {"query": "giants score"})
        self.assertIn("incomplete", output["error"])

    async def test_web_search_http_failure_becomes_error_payload(self):
        self.assistant.http_json = FakeFetch(
            {app.OPENAI_RESPONSES_URL: RuntimeError("HTTP 429 from api.openai.com")}
        )
        output = await self.call_tool("web_search", {"query": "giants score"})
        self.assertIn("HTTP 429", output["error"])

    async def test_web_search_disabled_never_fetches(self):
        self.assistant = VoiceAssistant(
            Config(api_key="test", web_search_enabled=False)
        )
        fetch = FakeFetch({app.OPENAI_RESPONSES_URL: RESPONSES_ANSWER})
        self.assistant.http_json = fetch
        output = await self.call_tool("web_search", {"query": "giants score"})
        self.assertIn("disabled", output["error"])
        self.assertEqual(fetch.calls, [])


class ToolConcurrencyTests(unittest.IsolatedAsyncioTestCase):
    async def test_slow_tool_call_does_not_block_events_and_defers_reply(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        audio = FakeAudio()
        websocket = FakeWebSocket()
        gate = asyncio.Event()

        async def slow_fetch(url, **kwargs):
            await gate.wait()
            return GEOCODE_BERLIN if url.startswith(app.GEOCODING_URL) else FORECAST_BERLIN

        assistant.http_json = slow_fetch
        await asyncio.wait_for(
            assistant.handle_event(
                _function_call_done("get_weather", json.dumps({"location": "Berlin"})),
                audio,
                websocket,
            ),
            timeout=1.0,
        )
        # The fetch is still in flight: nothing sent, one task pending.
        self.assertEqual(websocket.sent, [])
        self.assertEqual(len(assistant.tool_tasks), 1)
        # The user barges in while the tool is running...
        await assistant.handle_event(
            {"type": "input_audio_buffer.speech_started"}, audio, websocket
        )
        gate.set()
        await _drain_tools(assistant)
        # ...so the output lands in the conversation without forcing a response.
        types = [event["type"] for event in websocket.sent]
        self.assertEqual(types, ["conversation.item.create"])
        self.assertEqual(assistant.tool_tasks, set())


class EventTests(unittest.IsolatedAsyncioTestCase):
    async def test_microphone_stays_live_while_assistant_speaks(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        assistant.assistant_speaking.set()
        websocket = FakeWebSocket()

        with self.assertRaises(asyncio.CancelledError):
            await assistant.send_microphone(websocket, OneChunkAudio(b"\x01\x02"))

        self.assertEqual(websocket.sent[0]["type"], "input_audio_buffer.append")
        self.assertEqual(base64.b64decode(websocket.sent[0]["audio"]), b"\x01\x02")

    async def test_audio_delta_is_decoded_and_played(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        audio = FakeAudio()
        payload = b"\x01\x02\x03\x04"

        await assistant.handle_event(
            {
                "type": "response.output_audio.delta",
                "item_id": "item_1",
                "content_index": 0,
                "delta": base64.b64encode(payload).decode("ascii"),
            },
            audio,
        )

        self.assertEqual(audio.writes, [payload])
        self.assertTrue(assistant.assistant_speaking.is_set())

    async def test_speech_started_interrupts_playback_and_truncates_response(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        assistant.assistant_speaking.set()
        assistant.current_item_id = "item_1"
        assistant.current_content_index = 0
        assistant.playback_started_at = 9.25
        assistant.playback_audio_seconds = 2.0
        audio = FakeAudio()
        websocket = FakeWebSocket()

        with patch("app.time.monotonic", return_value=10.0):
            await assistant.handle_event(
                {"type": "input_audio_buffer.speech_started"}, audio, websocket
            )

        self.assertEqual(audio.interruptions, 1)
        self.assertFalse(assistant.assistant_speaking.is_set())
        self.assertEqual(
            websocket.sent,
            [
                {
                    "type": "conversation.item.truncate",
                    "item_id": "item_1",
                    "content_index": 0,
                    "audio_end_ms": 750,
                }
            ],
        )

    async def test_response_done_reopens_microphone(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        assistant.assistant_speaking.set()
        assistant.playback_deadline = 0

        await assistant.handle_event(
            {"type": "response.done", "response": {"status": "completed"}},
            FakeAudio(),
        )

        self.assertFalse(assistant.assistant_speaking.is_set())

    async def test_response_done_does_not_block_event_receiver_during_playback(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        assistant.assistant_speaking.set()
        assistant.current_item_id = "item_1"
        assistant.playback_deadline = 20.0

        with patch("app.time.monotonic", return_value=10.0):
            await assistant.handle_event(
                {"type": "response.done", "response": {"status": "completed"}},
                FakeAudio(),
            )

        self.assertTrue(assistant.assistant_speaking.is_set())
        self.assertIsNotNone(assistant.playback_done_task)
        assistant.playback_done_task.cancel()
        await asyncio.gather(assistant.playback_done_task, return_exceptions=True)

    async def test_api_error_is_raised(self):
        assistant = VoiceAssistant(Config(api_key="test"))
        with self.assertRaisesRegex(RuntimeError, "bad request"):
            await assistant.handle_event(
                {
                    "type": "error",
                    "error": {"code": "invalid_request_error", "message": "bad request"},
                },
                FakeAudio(),
            )


if __name__ == "__main__":
    unittest.main()
