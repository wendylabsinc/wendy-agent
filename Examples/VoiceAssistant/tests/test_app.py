import asyncio
import base64
import json
import os
import unittest
from unittest.mock import patch

from app import Config, VoiceAssistant


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
        self.assertEqual(config.input_device, "plughw:2,0")
        self.assertEqual(config.output_device, "plughw:2,0")
        self.assertFalse(config.mute_input_during_playback)

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
