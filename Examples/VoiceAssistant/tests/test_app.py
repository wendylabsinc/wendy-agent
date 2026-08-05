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
    VoiceAssistant,
    card_from_device,
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
        runner = FakeAmixer(
            {
                "scontrols": (0, SCONTROLS_USB),
                "sget": lambda args: (0, SGET_MIC_CAPTURE_ONLY)
                if args[1] == "Mic"
                else (0, SGET_SPEAKER_57),
            }
        )
        mixer = AlsaMixer("Device", run=runner)
        self.assertEqual(await mixer.resolve_control(), ("Speaker", 57))
        # Mic sorts after Speaker (preferred), so only Speaker is probed.
        self.assertEqual(await mixer.get_volume(), 57)

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

    def test_session_update_registers_volume_tools(self):
        session = self.assistant.session_update_event()["session"]
        self.assertEqual(session["tool_choice"], "auto")
        names = [tool["name"] for tool in session["tools"]]
        self.assertEqual(names, ["set_volume", "adjust_volume", "get_volume"])
        for tool in session["tools"]:
            self.assertEqual(tool["type"], "function")
            self.assertIn("parameters", tool)

    async def test_set_volume_tool_roundtrip(self):
        await self.assistant.handle_event(
            _function_call_done("set_volume", json.dumps({"percent": 25})),
            self.audio,
            self.websocket,
        )
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
        self.assertEqual(self.assistant.mixer.adjust_calls, [("up", 10)])
        await self.assistant.handle_event(
            _function_call_done("get_volume", "{}", call_id="call_2"),
            self.audio,
            self.websocket,
        )
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
        outputs = [
            json.loads(event["item"]["output"])
            for event in self.websocket.sent
            if event["type"] == "conversation.item.create"
        ]
        self.assertEqual(len(outputs), 4)
        for output in outputs:
            self.assertIn("error", output)


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
