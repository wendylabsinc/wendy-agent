import json
import subprocess
import unittest
from unittest import mock

import voice_server


class CaptureRescanTests(unittest.TestCase):
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


class ReplyPipelineTests(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
