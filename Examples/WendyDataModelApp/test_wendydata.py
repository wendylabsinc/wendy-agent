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


if __name__ == "__main__":
    unittest.main()
