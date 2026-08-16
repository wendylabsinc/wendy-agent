import json
import os
import queue
import subprocess
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

os.environ.setdefault(
    "TRACE_LOG_PATH", str(Path(tempfile.gettempdir()) / "speechllm-unit-trace.jsonl")
)

import voice_server
from g1_tools import G1ControlClient, G1ToolError, TOOL_SCHEMAS
from trace_recorder import TraceRecorder


class TraceRecorderTests(unittest.TestCase):
    def test_records_wall_monotonic_duration_and_summary(self):
        with tempfile.TemporaryDirectory() as root:
            recorder = TraceRecorder(
                str(Path(root) / "trace.jsonl"),
                monotonic_clock=lambda: 2_000_000_000,
                wall_clock=lambda: 1_750_000_000_000_000_000,
            )
            recorder.record(
                "turn-1",
                "inference.request",
                component="llama.cpp",
                duration_ns=1_500_000_000,
                details={"time_to_first_text_ms": 900.0},
            )
            recorder.record(
                "turn-1",
                "gpu.sample",
                component="nvidia-smi",
                kind="sample",
                details={"gpu_utilization_percent": 80.0},
            )
            events = recorder.recent(turn_id="turn-1")
            summary = recorder.summarize("turn-1")

        self.assertEqual(events[0]["duration_ms"], 1500.0)
        self.assertEqual(events[0]["monotonic_ns"], 2_000_000_000)
        self.assertEqual(events[0]["wall_unix_ns"], 1_750_000_000_000_000_000)
        self.assertTrue(events[0]["wall_time_utc"].endswith("Z"))
        self.assertEqual(summary["phase_totals_ms"]["inference.request"], 1500.0)
        self.assertEqual(summary["gpu"]["gpu_utilization_percent"]["peak"], 80.0)


class SystemPromptTests(unittest.TestCase):
    def test_loads_soul_and_sorted_knowledge_files(self):
        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            soul = root_path / "SOUL.md"
            knowledge = root_path / "knowledge"
            knowledge.mkdir()
            soul.write_text("I am Walter.", encoding="utf-8")
            (knowledge / "b.md").write_text("Second reference.", encoding="utf-8")
            (knowledge / "a.md").write_text("First reference.", encoding="utf-8")

            prompt = voice_server.load_system_prompt(soul, knowledge)

        self.assertIn("I am Walter.", prompt)
        self.assertLess(prompt.index("First reference."), prompt.index("Second reference."))

    def test_falls_back_when_prompt_files_are_absent(self):
        missing = Path("/definitely/not/present")
        prompt = voice_server.load_system_prompt(missing / "SOUL.md", missing)
        self.assertEqual(prompt, voice_server.DEFAULT_SYSTEM_PROMPT)

    def test_runtime_prompt_disables_repeated_g1_tasks_when_robot_is_offline(self):
        prompt = voice_server.runtime_system_prompt(
            "Walter base prompt", g1_tools_enabled=False
        )
        self.assertIn("G1 control is offline", prompt)
        self.assertIn("Do not call, retry, or suggest a G1 task", prompt)

    def test_runtime_prompt_is_unchanged_when_g1_tools_are_enabled(self):
        self.assertEqual(
            voice_server.runtime_system_prompt(
                "Walter base prompt", g1_tools_enabled=True
            ),
            "Walter base prompt",
        )

    def test_deployed_soul_clarifies_broad_implementation_requests(self):
        soul = Path(__file__).with_name("SOUL.md").read_text(encoding="utf-8")
        normalized = " ".join(soul.split())
        self.assertIn("ask exactly one concise question", normalized)
        self.assertIn("What kind of implementation do you want to use it for?", normalized)
        self.assertIn("Never repeat an assistant sentence", normalized)
        self.assertIn("The words “explain,” “how does it work,”", normalized)
        self.assertIn("Never begin a numbered list", normalized)

    def test_voice_prompt_does_not_use_model_generated_silence_marker(self):
        soul = Path(__file__).with_name("SOUL.md").read_text(encoding="utf-8")
        source = Path(voice_server.__file__).read_text(encoding="utf-8")

        self.assertNotIn("NO_SPEECH", soul)
        self.assertNotIn("NO_SPEECH", source)

    def test_deployment_auto_listens_after_restart(self):
        stagefile = Path(__file__).with_name("build.stagefile.yaml").read_text(
            encoding="utf-8"
        )

        self.assertIn('AUTO_LISTEN: "true"', stagefile)


class CaptureRescanTests(unittest.TestCase):
    def test_capture_remains_active_while_walter_thinks_and_speaks(self):
        state = voice_server.VoiceState()
        state.model_ready = True
        state.listening = True
        state.generating = True
        state.speaking = True
        with mock.patch.object(voice_server, "STATE", state):
            self.assertTrue(voice_server.should_capture())

    def test_new_audio_turn_waits_while_inference_is_thinking(self):
        state = voice_server.VoiceState()
        state.generating = True
        state.speaking = False
        with mock.patch.object(voice_server, "STATE", state):
            self.assertFalse(voice_server.should_begin_audio_turn())
            state.speaking = True
            self.assertTrue(voice_server.should_begin_audio_turn())

    @mock.patch.object(voice_server.subprocess, "run")
    def test_rescan_includes_new_hardware_without_duplicates(self, run):
        run.return_value = subprocess.CompletedProcess(
            ["arecord", "-l"],
            0,
            stdout=(
                "**** List of CAPTURE Hardware Devices ****\n"
                "card 2: Camera [USB Camera], device 0: USB Audio [USB Audio]\n"
                "card 3: Mic [New USB Mic], device 1: USB Audio [USB Audio]\n"
            ),
        )

        with mock.patch.dict(
            voice_server.os.environ, {"ALSA_CAPTURE_DEVICE": "plughw:2,0"}
        ):
            devices = voice_server.rescan_capture_devices()

        self.assertEqual(devices, ["plughw:2,0", "plughw:3,1"])

    @mock.patch.object(voice_server.time, "sleep")
    @mock.patch.object(voice_server.shutil, "which", return_value="/usr/bin/arecord")
    @mock.patch.object(
        voice_server,
        "rescan_capture_devices",
        return_value=["plughw:2,0", "plughw:3,0"],
    )
    @mock.patch.object(voice_server.subprocess, "Popen")
    def test_start_capture_falls_back_to_newly_scanned_device(
        self, popen, _rescan, _which, _sleep
    ):
        original_source = voice_server.STATE.capture_source
        self.addCleanup(
            setattr, voice_server.STATE, "capture_source", original_source
        )
        stale = mock.Mock()
        stale.poll.return_value = 1
        fresh = mock.Mock()
        fresh.poll.return_value = None
        popen.side_effect = [stale, fresh]

        process = voice_server.start_capture()

        self.assertIs(process, fresh)
        self.assertEqual(voice_server.STATE.capture_source, "plughw:3,0")
        self.assertEqual(popen.call_count, 2)


class AlsaPlaybackTests(unittest.TestCase):
    @mock.patch.object(voice_server.shutil, "which", return_value="/usr/bin/aplay")
    @mock.patch.object(voice_server.subprocess, "Popen")
    def test_wav_is_piped_directly_to_configured_alsa_device(self, popen, _which):
        process = mock.Mock()
        process.communicate.return_value = (b"", b"")
        process.returncode = 0
        popen.return_value = process
        original_device = voice_server.ALSA_PLAYBACK_DEVICE
        self.addCleanup(
            setattr, voice_server, "ALSA_PLAYBACK_DEVICE", original_device
        )
        voice_server.ALSA_PLAYBACK_DEVICE = "plughw:4,0"

        voice_server.play_wav_alsa(b"RIFF wav data")

        popen.assert_called_once_with(
            ["aplay", "-q", "-D", "plughw:4,0", "-t", "wav"],
            stdin=subprocess.PIPE,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        process.communicate.assert_called_once_with(input=b"RIFF wav data")

    @mock.patch.object(voice_server.shutil, "which", return_value=None)
    def test_missing_aplay_is_reported(self, _which):
        with self.assertRaisesRegex(RuntimeError, "aplay is not installed"):
            voice_server.play_wav_alsa(b"RIFF wav data")

    def test_stop_playback_terminates_active_aplay(self):
        process = mock.Mock()
        process.poll.return_value = None
        with mock.patch.object(voice_server, "PLAYBACK_PROCESS", process):
            voice_server.stop_playback()
        process.terminate.assert_called_once_with()


class BargeInTests(unittest.TestCase):
    @mock.patch.object(voice_server, "stop_playback")
    def test_confirmed_onset_cancels_and_discards_before_endpoint(self, stop):
        state = voice_server.VoiceState()
        state.turn_active = True
        state.generating = True
        state.speaking = True
        state.active_speech_queue = queue.Queue()
        state.active_speech_queue.put("later sentence one")
        state.active_speech_queue.put("later sentence two")
        subscriber = queue.Queue()
        state.subscribers.append(subscriber)

        with mock.patch.object(voice_server, "STATE", state):
            interrupted = voice_server.interrupt_for_barge_in(monotonic_ns=1234)

        self.assertTrue(interrupted)
        self.assertTrue(state.cancel_generation.is_set())
        stop.assert_called_once_with()
        self.assertTrue(state.active_speech_queue.empty())
        event = subscriber.get_nowait()
        self.assertEqual(event["event"], "barge_in")
        self.assertEqual(event["data"]["monotonic_ns"], 1234)
        self.assertEqual(event["data"]["queued_chunks_discarded"], 2)

    def test_queue_discard_preserves_worker_sentinel(self):
        speech_queue = queue.Queue()
        speech_queue.put("later sentence")
        speech_queue.put(None)

        dropped = voice_server.discard_queued_speech(speech_queue)

        self.assertEqual(dropped, 1)
        self.assertIsNone(speech_queue.get_nowait())
        self.assertTrue(speech_queue.empty())

    @mock.patch.object(voice_server, "generate_reply")
    def test_interrupting_audio_is_submitted_as_waiting_next_turn(self, generate):
        state = voice_server.VoiceState()
        with mock.patch.object(voice_server, "STATE", state):
            voice_server.generate_audio_reply(
                b"\x00\x20" * 320,
                0.5,
                input_started_ns=100,
                input_ended_ns=200,
            )

        self.assertEqual(generate.call_count, 1)
        kwargs = generate.call_args.kwargs
        self.assertTrue(kwargs["wait_for_idle"])
        self.assertEqual(kwargs["input_started_ns"], 100)
        self.assertEqual(kwargs["input_ended_ns"], 200)

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server, "_stream_completion")
    def test_waiting_next_turn_is_not_dropped(self, stream, speak):
        stream.return_value = ("I heard the interruption.", "", None)
        state = voice_server.VoiceState()
        state.model_ready = True
        state.turn_active = True
        client = mock.Mock()
        client.enabled = False

        worker = threading.Thread(
            target=voice_server.generate_reply,
            args=({"role": "user", "content": "interruption"},),
            kwargs={"turn_id": "turn-after-barge-in", "wait_for_idle": True},
        )
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            worker.start()
            time.sleep(0.02)
            stream.assert_not_called()
            with state.turn_idle:
                state.turn_active = False
                state.turn_idle.notify_all()
            worker.join(timeout=1)

        self.assertFalse(worker.is_alive())
        stream.assert_called_once()
        self.assertFalse(state.turn_active)
        speak.assert_not_called()


class G1CompletionTraceTests(unittest.TestCase):
    @mock.patch.object(voice_server, "_trace_event")
    def test_terminal_release_is_appended_after_speech_turn_finishes(self, trace):
        client = mock.Mock()
        client.timing.return_value = {
            "ok": True,
            "timing": {
                "state": "released",
                "request_to_action_complete_ns": 10_000_000_000,
            },
        }
        client.alignment_timing.return_value = {"alignment": {"ok": True}}
        state = voice_server.VoiceState()
        with (
            mock.patch.object(voice_server, "G1_CLIENT", client),
            mock.patch.object(voice_server, "STATE", state),
        ):
            voice_server.record_g1_action_completion("turn-action", "raise_hand")

        self.assertEqual(trace.call_args_list[0].args[0], "g1.action.completed")
        self.assertEqual(trace.call_args_list[0].kwargs["duration_ns"], 10_000_000_000)
        self.assertEqual(trace.call_args_list[1].args[0], "g1.action.final_alignment")


class ReplyPipelineTests(unittest.TestCase):
    def setUp(self):
        voice_server.GESTURE_GATE.clear()

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_speechllm_reply_is_sent_to_kokoro_alsa_path(self, urlopen, speak):
        event = {"choices": [{"delta": {"content": "Hello from Ultravox."}}]}
        response = mock.MagicMock()
        response.__enter__.return_value = [
            f"data: {json.dumps(event)}\n".encode(),
            b"data: [DONE]\n",
        ]
        urlopen.return_value = response
        state = voice_server.VoiceState()
        state.model_ready = True

        with mock.patch.object(voice_server, "STATE", state):
            voice_server.generate_reply({"role": "user", "content": "Hello"})

        speak.assert_called_once_with("Hello from Ultravox.")
        self.assertFalse(state.generating)
        self.assertFalse(state.speaking)
        self.assertEqual(state.phase, "listening")

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_tool_call_is_validated_then_scheduled_at_first_playback(
        self, urlopen, speak
    ):
        tool_events = [
            {
                "choices": [
                    {
                        "delta": {
                            "tool_calls": [
                                {
                                    "index": 0,
                                    "id": "call-1",
                                    "type": "function",
                                    "function": {
                                        "name": "g1_gesture",
                                        "arguments": (
                                            '{"action":"raise_hand",'
                                            '"command_text":"Walter, raise your hand"}'
                                        ),
                                    },
                                }
                            ]
                        }
                    }
                ]
            }
        ]
        reply_events = [
            {"choices": [{"delta": {"content": "Okay, I'll raise my hand."}}]}
        ]

        def response(events):
            value = mock.MagicMock()
            value.__enter__.return_value = [
                *[f"data: {json.dumps(event)}\n".encode() for event in events],
                b"data: [DONE]\n",
            ]
            return value

        urlopen.side_effect = [response(tool_events), response(reply_events)]
        client = mock.Mock()
        client.enabled = True
        client.admit_tool.return_value = (
            {"success": True, "accepted": True, "action": "raise_hand"},
            "raise_hand",
        )
        client.schedule.return_value = {
            "action_scheduled": True,
            "playback_at_ns": 10,
            "g1_execute_at_ns": 20,
        }
        client.wait_until_prepared.return_value = {"state": "prepared"}
        client.timing.return_value = {"ok": True, "timing": {"state": "accepted"}}

        def play(_text, before_playback=None):
            self.assertIsNotNone(before_playback)
            before_playback()

        speak.side_effect = play
        state = voice_server.VoiceState()
        state.model_ready = True
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            voice_server.generate_reply(
                {"role": "user", "content": "Raise your hand"}, turn_id="turn-tool"
            )

        client.admit_tool.assert_called_once_with(
            "g1_gesture", {"action": "raise_hand"}
        )
        client.schedule.assert_called_once_with("turn-tool", "raise_hand")
        client.wait_until_prepared.assert_called_once()
        client.wait_until_playback.assert_called_once()
        client.record_event.assert_called_once_with(
            "turn-tool", "output.speech_started"
        )
        self.assertEqual(state.history, [])

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_hypothetical_text_tool_call_never_reaches_scheduler(
        self, urlopen, speak
    ):
        tool_events = [
            {
                "choices": [
                    {
                        "delta": {
                            "tool_calls": [
                                {
                                    "index": 0,
                                    "id": "call-hypothetical",
                                    "type": "function",
                                    "function": {
                                        "name": "g1_gesture",
                                        "arguments": (
                                            '{"action":"wave_hand",'
                                            '"command_text":"Explain what would happen '
                                            'if someone asked you to wave, but do not move"}'
                                        ),
                                    },
                                }
                            ]
                        }
                    }
                ]
            }
        ]
        reply_events = [
            {"choices": [{"delta": {"content": "I will not move."}}]}
        ]

        def response(events):
            value = mock.MagicMock()
            value.__enter__.return_value = [
                *[f"data: {json.dumps(event)}\n".encode() for event in events],
                b"data: [DONE]\n",
            ]
            return value

        urlopen.side_effect = [response(tool_events), response(reply_events)]
        client = mock.Mock()
        client.enabled = True
        client.admit_tool.return_value = (
            {"success": True, "accepted": True, "action": "wave_hand"},
            "wave_hand",
        )
        state = voice_server.VoiceState()
        state.model_ready = True
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            voice_server.generate_reply(
                {
                    "role": "user",
                    "content": "Explain what would happen if someone asked you to wave, but do not move.",
                },
                turn_id="turn-hypothetical",
            )

        client.schedule.assert_not_called()
        speak.assert_called_once_with("I will not move.")

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_unrelated_audio_tool_call_is_rejected_before_g1_admission(
        self, urlopen, speak
    ):
        tool_event = {
            "choices": [
                {
                    "delta": {
                        "tool_calls": [
                            {
                                "index": 0,
                                "id": "call-unrelated-audio",
                                "type": "function",
                                "function": {
                                    "name": "g1_gesture",
                                    "arguments": (
                                        '{"action":"shake_hand",'
                                        '"command_text":"Walter, tell me about WendyOS"}'
                                    ),
                                },
                            }
                        ]
                    }
                }
            ]
        }
        reply_event = {
            "choices": [{"delta": {"content": "I won't start a gesture."}}]
        }

        def response(events):
            value = mock.MagicMock()
            value.__enter__.return_value = [
                *[f"data: {json.dumps(event)}\n".encode() for event in events],
                b"data: [DONE]\n",
            ]
            return value

        urlopen.side_effect = [response([tool_event]), response([reply_event])]
        client = mock.Mock()
        client.enabled = True
        state = voice_server.VoiceState()
        state.model_ready = True
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            voice_server.generate_reply(audio_message, turn_id="turn-unrelated-audio")

        client.admit_tool.assert_not_called()
        client.schedule.assert_not_called()
        speak.assert_called_once_with("I won't start a gesture.")

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_multiple_sentences_are_spoken_as_streaming_chunks(self, urlopen, speak):
        deltas = ["The first sentence is ready. ", "The second one follows."]
        response = mock.MagicMock()
        response.__enter__.return_value = [
            *[
                f"data: {json.dumps({'choices': [{'delta': {'content': delta}}]})}\n".encode()
                for delta in deltas
            ],
            b"data: [DONE]\n",
        ]
        urlopen.return_value = response
        state = voice_server.VoiceState()
        state.model_ready = True

        with mock.patch.object(voice_server, "STATE", state):
            voice_server.generate_reply({"role": "user", "content": "Explain"})

        self.assertEqual(
            speak.call_args_list,
            [
                mock.call("The first sentence is ready."),
                mock.call("The second one follows."),
            ],
        )

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_generic_voice_clarification_is_blocked_on_first_turn(
        self, urlopen, speak
    ):
        event = {
            "choices": [{"delta": {"content": "What do you need it for?"}}]
        }
        response = mock.MagicMock()
        response.__enter__.return_value = [
            f"data: {json.dumps(event)}\n".encode(),
            b"data: [DONE]\n",
        ]
        urlopen.return_value = response
        state = voice_server.VoiceState()
        state.model_ready = True
        client = mock.Mock()
        client.enabled = False
        message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }

        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            voice_server.generate_reply(message, turn_id="voice-generic-question")

        speak.assert_not_called()
        self.assertEqual(state.history, [])
        self.assertEqual(len(state.unusable_voice_turns), 1)

    @mock.patch.object(voice_server, "speak_alsa")
    @mock.patch.object(voice_server.urllib.request, "urlopen")
    def test_distinct_mic_turn_cannot_replay_previous_first_sentence(
        self, urlopen, speak
    ):
        repeated = {"choices": [{"delta": {"content": "I can help with that."}}]}

        def response():
            value = mock.MagicMock()
            value.__enter__.return_value = [
                f"data: {json.dumps(repeated)}\n".encode(),
                b"data: [DONE]\n",
            ]
            return value

        urlopen.side_effect = [response(), response()]
        state = voice_server.VoiceState()
        state.model_ready = True
        subscriber = queue.Queue()
        state.subscribers.append(subscriber)
        client = mock.Mock()
        client.enabled = False

        def audio_message(data):
            return {
                "role": "user",
                "content": [
                    {
                        "type": "input_audio",
                        "input_audio": {"data": data, "format": "wav"},
                    }
                ],
            }

        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "G1_CLIENT", client),
        ):
            voice_server.generate_reply(audio_message("first"), turn_id="voice-first")
            voice_server.generate_reply(audio_message("second"), turn_id="voice-second")

        speak.assert_called_once_with("I can help with that.")
        events = []
        while not subscriber.empty():
            events.append(subscriber.get_nowait())
        deltas = [event for event in events if event["event"] == "assistant_delta"]
        completions = [event for event in events if event["event"] == "assistant_done"]
        self.assertEqual(len(deltas), 1)
        self.assertEqual(deltas[0]["data"]["text"], "I can help with that.")
        self.assertTrue(completions[-1]["data"]["suppressed"])
        self.assertEqual(state.history, [])


class AudioTurnAdmissionTests(unittest.TestCase):
    @mock.patch.object(voice_server, "generate_reply")
    def test_silent_turn_never_reaches_model_or_reuses_context(self, generate):
        state = voice_server.VoiceState()
        state.history = [
            {"role": "assistant", "content": "What do you need it for?"}
        ]
        with mock.patch.object(voice_server, "STATE", state):
            voice_server.generate_audio_reply(
                b"\x00\x00" * (voice_server.FRAME_BYTES // 2),
                0.5,
                turn_id="voice-silent",
            )

        generate.assert_not_called()
        self.assertEqual(
            state.history,
            [{"role": "assistant", "content": "What do you need it for?"}],
        )

    @mock.patch.object(voice_server, "generate_reply")
    def test_byte_identical_stale_pcm_is_submitted_only_once(self, generate):
        state = voice_server.VoiceState()
        raw = b"\x00\x20" * (voice_server.FRAME_BYTES // 2)
        with mock.patch.object(voice_server, "STATE", state):
            voice_server.generate_audio_reply(raw, 0.5, turn_id="voice-one")
            voice_server.generate_audio_reply(raw, 0.5, turn_id="voice-two")

        generate.assert_called_once()
        self.assertEqual(generate.call_args.kwargs["turn_id"], "voice-one")


class GestureCommandGateTests(unittest.TestCase):
    def setUp(self):
        self.gate = voice_server.GestureCommandGate()

    def test_direct_typed_command_executes(self):
        decision, _ = self.gate.evaluate(
            {"role": "user", "content": "Walter, please raise your hand."},
            "raise_hand",
        )
        self.assertEqual(decision, "execute")

    def test_hypothetical_typed_command_is_rejected(self):
        decision, result = self.gate.evaluate(
            {
                "role": "user",
                "content": "Explain what would happen if someone asked you to wave.",
            },
            "wave_hand",
        )
        self.assertEqual(decision, "reject")
        self.assertFalse(result["accepted"])

    def test_spoken_command_executes_without_confirmation(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(
            audio_message,
            "wave_hand",
            command_text="Walter, wave to the audience now.",
        )
        self.assertEqual(decision, "execute")
        self.assertEqual(result, {})

    def test_spoken_tool_call_without_current_turn_words_is_rejected(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(audio_message, "shake_hand")
        self.assertEqual(decision, "reject")
        self.assertFalse(result["accepted"])

    def test_walters_declarative_tts_cannot_authorize_a_gesture(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(
            audio_message,
            "shake_hand",
            command_text="Okay, I'll shake your hand now.",
        )
        self.assertEqual(decision, "reject")
        self.assertFalse(result["accepted"])

    def test_unrelated_spoken_turn_cannot_authorize_model_selected_gesture(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(
            audio_message,
            "shake_hand",
            command_text="Walter, tell me how WendyOS works.",
        )
        self.assertEqual(decision, "reject")
        self.assertFalse(result["accepted"])

    def test_spoken_command_must_agree_with_selected_action(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(
            audio_message,
            "shake_hand",
            command_text="Walter, raise your hand.",
        )
        self.assertEqual(decision, "reject")
        self.assertFalse(result["accepted"])

    def test_urgent_spoken_stop_does_not_require_wake_word(self):
        audio_message = {
            "role": "user",
            "content": [{"type": "input_audio", "input_audio": {}}],
        }
        decision, result = self.gate.evaluate(
            audio_message,
            "stop",
            command_text="Stop now!",
        )
        self.assertEqual(decision, "execute")
        self.assertEqual(result, {})

    def test_gesture_tool_requires_current_turn_command_words(self):
        gesture = next(
            item for item in TOOL_SCHEMAS if item["function"]["name"] == "g1_gesture"
        )
        parameters = gesture["function"]["parameters"]
        self.assertEqual(set(parameters["required"]), {"action", "command_text"})
        self.assertFalse(parameters["additionalProperties"])


class ConversationHistoryTests(unittest.TestCase):
    def test_voice_turn_retains_neither_audio_nor_assistant_only_context(self):
        history = []
        voice_server.retain_history_turn(
            history,
            {
                "role": "user",
                "content": [
                    {
                        "type": "input_audio",
                        "input_audio": {"data": "old-command-bytes", "format": "wav"},
                    }
                ],
            },
            "I answered that turn.",
            None,
        )
        serialized = json.dumps(history)
        self.assertNotIn("old-command-bytes", serialized)
        self.assertEqual(history, [])

    def test_typed_turn_still_retains_both_sides(self):
        history = []
        voice_server.retain_history_turn(
            history,
            {"role": "user", "content": "Explain WendyOS."},
            "WendyOS deploys applications to edge devices.",
            None,
        )
        self.assertEqual(len(history), 2)

    def test_runaway_unusable_voice_turns_enter_self_clearing_cooldown(self):
        state = voice_server.VoiceState()
        state.model_ready = True
        state.listening = True
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "_trace_event"),
        ):
            self.assertFalse(
                voice_server.record_unusable_voice_turn("v1", "blocked", now=1.0)
            )
            self.assertFalse(
                voice_server.record_unusable_voice_turn("v2", "blocked", now=2.0)
            )
            self.assertTrue(
                voice_server.record_unusable_voice_turn("v3", "blocked", now=3.0)
            )
        self.assertFalse(state.listening)
        self.assertEqual(state.phase, "cooldown")
        self.assertEqual(state.listen_pause_reason, "repeated_unusable_voice_turns")
        self.assertTrue(state.auto_resume_pending)
        self.assertEqual(
            state.auto_resume_not_before,
            3.0 + voice_server.AUTO_RESUME_COOLDOWN_SECONDS,
        )
        self.assertEqual(
            state.auto_resume_force_at,
            3.0 + voice_server.AUTO_RESUME_MAX_SECONDS,
        )
        with mock.patch.object(voice_server, "STATE", state):
            self.assertTrue(voice_server.should_capture())
            self.assertFalse(voice_server.should_begin_audio_turn())

    def test_runaway_cooldown_resumes_after_minimum_and_sustained_quiet(self):
        state = voice_server.VoiceState()
        state.model_ready = True
        state.listening = True
        subscriber = queue.Queue()
        state.subscribers.append(subscriber)
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "_trace_event") as trace,
        ):
            for index in range(3):
                voice_server.record_unusable_voice_turn(
                    f"v{index}", "blocked", now=float(index + 1)
                )
            self.assertFalse(
                voice_server.observe_auto_resume(0.001, 0.012, now=4.0)
            )
            self.assertTrue(
                voice_server.observe_auto_resume(0.001, 0.012, now=6.1)
            )

        self.assertTrue(state.listening)
        self.assertFalse(state.auto_resume_pending)
        self.assertEqual(state.phase, "listening")
        self.assertEqual(state.listen_pause_reason, "")
        self.assertEqual(list(state.unusable_voice_turns), [])
        trace.assert_any_call(
            "listening.auto_resumed",
            component="speechllm",
            turn_id="v2",
            details={"reason": "quiet", "level": 0.001, "threshold": 0.012},
        )
        events = []
        while not subscriber.empty():
            events.append(subscriber.get_nowait())
        self.assertEqual(events[-1]["event"], "listening_auto_resumed")

    def test_runaway_cooldown_forces_bounded_retry_in_continuous_noise(self):
        state = voice_server.VoiceState()
        state.model_ready = True
        state.listening = True
        with (
            mock.patch.object(voice_server, "STATE", state),
            mock.patch.object(voice_server, "_trace_event") as trace,
        ):
            for index in range(3):
                voice_server.record_unusable_voice_turn(
                    f"v{index}", "blocked", now=float(index + 1)
                )
            self.assertFalse(
                voice_server.observe_auto_resume(0.1, 0.012, now=14.9)
            )
            self.assertTrue(
                voice_server.observe_auto_resume(0.1, 0.012, now=15.0)
            )

        self.assertTrue(state.listening)
        self.assertFalse(state.auto_resume_pending)
        trace.assert_any_call(
            "listening.auto_resumed",
            component="speechllm",
            turn_id="v2",
            details={
                "reason": "maximum_cooldown",
                "level": 0.1,
                "threshold": 0.012,
            },
        )

    def test_action_turn_clears_all_conversation_history(self):
        history = [
            {"role": "user", "content": "Shake my hand"},
            {"role": "assistant", "content": "I will shake your hand."},
        ]
        voice_server.retain_history_turn(
            history,
            {"role": "user", "content": "another action"},
            "Acting now.",
            "shake_hand",
        )
        self.assertEqual(history, [])


class TtsChunkingTests(unittest.TestCase):
    def test_keeps_incomplete_sentence_buffered(self):
        chunks, remainder = voice_server.extract_tts_chunks(
            "A complete first sentence. The next is still"
        )
        self.assertEqual(chunks, ["A complete first sentence."])
        self.assertEqual(remainder, "The next is still")

    def test_flushes_unpunctuated_final_text(self):
        chunks, remainder = voice_server.extract_tts_chunks(
            "A final answer without punctuation", final=True
        )
        self.assertEqual(chunks, ["A final answer without punctuation"])
        self.assertEqual(remainder, "")


class G1ToolPolicyTests(unittest.TestCase):
    def client(self):
        return G1ControlClient(
            enabled=True,
            g1_url="http://g1:8094",
            g1_token="g1-token",
            orchestrator_url="http://spark:8093",
            orchestrator_token="spark-token",
        )

    def test_exact_allowlist_action_is_admitted_only_when_idle(self):
        client = self.client()
        with mock.patch.object(
            client,
            "status",
            return_value={
                "ready": True,
                "hardware_connected": True,
                "lowstate_ready": True,
                "pending": 0,
                "active_turn": None,
                "prepared_turn": None,
            },
        ):
            result, action = client.admit_tool(
                "g1_gesture", {"action": "wave_hand"}
            )
        self.assertTrue(result["accepted"])
        self.assertEqual(action, "wave_hand")

    def test_arbitrary_motion_field_is_rejected(self):
        client = self.client()
        with self.assertRaisesRegex(G1ToolError, "unsupported G1 gesture"):
            client.admit_tool("g1_gesture", {"action": "walk_forward"})

if __name__ == "__main__":
    unittest.main()
