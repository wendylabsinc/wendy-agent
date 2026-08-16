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


if __name__ == "__main__":
    unittest.main()
