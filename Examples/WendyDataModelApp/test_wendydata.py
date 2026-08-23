"""Unit tests for the pure parts of the data socket client.

Run with: python3 -m unittest discover Examples/WendyDataModelApp

No camera, model, or agent is required: these cover the record framing
and the uncertainty formula, the two pieces the agent-side smoke tests
cannot see.
"""

import json
import struct
import unittest

import wendydata


class TestUncertaintyScore(unittest.TestCase):
    def test_no_detections_is_fully_uncertain(self):
        self.assertEqual(wendydata.uncertainty_score([], 0.25), 1.0)

    def test_below_threshold_detections_are_ignored(self):
        self.assertEqual(wendydata.uncertainty_score([0.1, 0.2], 0.25), 1.0)

    def test_uses_max_confidence(self):
        self.assertAlmostEqual(wendydata.uncertainty_score([0.3, 0.9, 0.5], 0.25), 0.1)

    def test_certain_detection_scores_zero(self):
        self.assertEqual(wendydata.uncertainty_score([1.0], 0.25), 0.0)

    def test_clamped_to_unit_interval(self):
        # A confidence above 1.0 (a broken model) must not go negative;
        # campaign model.uncertainty thresholds are defined on 0..1.
        self.assertEqual(wendydata.uncertainty_score([1.5], 0.25), 0.0)

    def test_accepts_generators(self):
        self.assertAlmostEqual(wendydata.uncertainty_score((c for c in [0.75]), 0.25), 0.25)


class TestFraming(unittest.TestCase):
    def test_length_prefix_is_big_endian(self):
        frame = wendydata.frame_record({"version": 1})
        (length,) = struct.unpack(">I", frame[:4])
        self.assertEqual(length, len(frame) - 4)
        self.assertEqual(json.loads(frame[4:]), {"version": 1})

    def test_oversized_record_is_refused(self):
        with self.assertRaises(ValueError):
            wendydata.frame_record({"blob": "x" * wendydata.MAX_RECORD_BYTES})


class TestRecords(unittest.TestCase):
    def test_prediction_record_shape(self):
        record = wendydata.build_prediction(
            "yolov8n", "8.3.63", 0.35, [{"class_name": "person", "confidence": 0.65}]
        )
        self.assertEqual(record["version"], 1)
        self.assertEqual(record["type"], "prediction")
        self.assertEqual(record["model"], "yolov8n")
        self.assertEqual(record["attributes"]["model_version"], "8.3.63")
        # Campaign model.uncertainty triggers read attributes["uncertainty"].
        self.assertAlmostEqual(record["attributes"]["uncertainty"], 0.35)
        self.assertEqual(len(record["attributes"]["detections"]), 1)
        self.assertIn("client_boottime_nanos", record)
        self.assertIn("boot_id", record)

    def test_event_record_shape(self):
        record = wendydata.build_event("person_detected", {"confidence": 0.9})
        self.assertEqual(record["version"], 1)
        self.assertEqual(record["type"], "event")
        self.assertEqual(record["name"], "person_detected")
        self.assertEqual(record["attributes"]["confidence"], 0.9)

    def test_records_fit_the_frame_limit(self):
        record = wendydata.build_prediction("yolov8n", "8.3.63", 1.0, [])
        self.assertLessEqual(len(wendydata.frame_record(record)), wendydata.MAX_RECORD_BYTES)


class TestInputReferences(unittest.TestCase):
    """A prediction must be able to name the harness samples it came from;
    that binding is what lets an episode be replayed as (input, outcome)."""

    def test_prediction_without_inputs_omits_the_field(self):
        record = wendydata.build_prediction("yolov8n", "8.3.63", 1.0, [])
        self.assertNotIn("inputs", record)

    def test_prediction_carries_input_references(self):
        record = wendydata.build_prediction(
            "yolov8n", "8.3.63", 0.4, [], inputs=[{"source_id": "v4l2:/dev/video0", "sample_id": 17}]
        )
        self.assertEqual(record["inputs"], [{"source_id": "v4l2:/dev/video0", "sample_id": 17}])

    def test_input_references_are_normalized(self):
        record = wendydata.build_prediction(
            "yolov8n", "8.3.63", 0.4, [], inputs=[{"source_id": "cam", "sample_id": "5"}]
        )
        self.assertEqual(record["inputs"], [{"source_id": "cam", "sample_id": 5}])

    def test_input_references_are_capped_to_the_agent_limit(self):
        refs = [{"source_id": "cam", "sample_id": i} for i in range(100)]
        record = wendydata.build_prediction("yolov8n", "8.3.63", 0.4, [], inputs=refs)
        self.assertEqual(len(record["inputs"]), wendydata.MAX_INPUT_REFS)
        # The newest references are the ones worth keeping.
        self.assertEqual(record["inputs"][-1]["sample_id"], 99)


class TestSampleAttribution(unittest.TestCase):
    """Every decoded frame must keep the sample identifiers it was computed
    from. A frame that loses them produces a prediction with no `inputs` field,
    which is the silent failure this whole change exists to prevent."""

    @staticmethod
    def _sensors():
        """Import wendysensors with its transport dependencies stubbed.

        The module needs grpc and the generated stubs only to make the call;
        the attribution rule under test needs neither, and a unit test must not
        require a gRPC toolchain to exercise it."""
        import sys
        import types

        for name in ("grpc",):
            sys.modules.setdefault(name, types.ModuleType(name))
        for name in (
            "wendy",
            "wendy.agent",
            "wendy.agent.services",
            "wendy.agent.services.v2",
        ):
            module = sys.modules.setdefault(name, types.ModuleType(name))
            module.__path__ = []
        for name in (
            "wendy.agent.services.v2.sensor_service_pb2",
            "wendy.agent.services.v2.sensor_service_pb2_grpc",
        ):
            sys.modules.setdefault(name, types.ModuleType(name))
        import wendysensors

        return wendysensors

    class _Sample:
        def __init__(self, sample_id, dropped_before=0, payload=b"x", encoding="h264"):
            self.sample_id = sample_id
            self.dropped_before = dropped_before
            self.payload = payload
            self.encoding = encoding
            self.source_id = "v4l2:/dev/video0"
            self.boottime_nanos = sample_id * 1000
            self.timestamp_uncertainty_nanos = 10

    class _Decoded:
        def to_ndarray(self, format=None):
            return format

    class _Decoder:
        """A decoder that yields `per_packet` frames out of every payload."""

        def __init__(self, per_packet):
            self.per_packet = per_packet

        def parse(self, payload):
            return [payload]

        def decode(self, packet):
            return [TestSampleAttribution._Decoded() for _ in range(self.per_packet)]

    def test_every_frame_from_one_packet_keeps_its_sample_ids(self):
        sensors = self._sensors()
        frames = list(
            sensors.decode_samples(
                [self._Sample(1), self._Sample(2)],
                lambda encoding: self._Decoder(per_packet=2),
            )
        )
        self.assertEqual(len(frames), 4)
        for frame in frames:
            self.assertTrue(
                frame.sample_ids,
                "a decoded frame lost the sample identifiers it was computed from",
            )
            self.assertTrue(frame.input_refs())
        self.assertEqual([f.sample_ids for f in frames], [[1], [1], [2], [2]])

    def test_samples_that_decode_to_nothing_accumulate(self):
        sensors = self._sensors()
        decoders = [self._Decoder(per_packet=0), self._Decoder(per_packet=1)]

        class Growing:
            """No frame until the third sample, then one frame from all three."""

            def __init__(self):
                self.calls = 0

            def parse(self, payload):
                self.calls += 1
                return [payload]

            def decode(self, packet):
                if self.calls < 3:
                    return []
                return [TestSampleAttribution._Decoded()]

        del decoders
        frames = list(
            sensors.decode_samples(
                [self._Sample(1), self._Sample(2), self._Sample(3, dropped_before=4)],
                lambda encoding: Growing(),
            )
        )
        self.assertEqual(len(frames), 1)
        self.assertEqual(frames[0].sample_ids, [1, 2, 3])
        self.assertEqual(frames[0].dropped_before, 4)

    def test_drops_are_counted_once_per_byte_run(self):
        sensors = self._sensors()
        frames = list(
            sensors.decode_samples(
                [self._Sample(1, dropped_before=3)],
                lambda encoding: self._Decoder(per_packet=3),
            )
        )
        self.assertEqual([f.dropped_before for f in frames], [3, 0, 0])


if __name__ == "__main__":
    unittest.main()
