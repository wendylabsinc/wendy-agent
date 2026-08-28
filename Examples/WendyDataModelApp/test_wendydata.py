"""Unit tests for the pure parts of the data socket client.

Run with: python3 -m unittest discover Examples/WendyDataModelApp

No camera, model, or agent is required: these cover the record framing
and the uncertainty formula, the two pieces the agent-side smoke tests
cannot see.
"""

import json
import struct
import threading
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
            "wendy.agent.apps",
            "wendy.agent.apps.v1",
        ):
            module = sys.modules.setdefault(name, types.ModuleType(name))
            module.__path__ = []
        for name in (
            "wendy.agent.apps.v1.sensor_service_pb2",
            "wendy.agent.apps.v1.sensor_service_pb2_grpc",
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


class TestDecoderFollowsTheEncoding(unittest.TestCase):
    """The proto sets `encoding` per sample, not per stream. A decoder built
    once from the first sample decodes nothing at all after a mid-stream switch,
    which loses every frame from that point on without saying so."""

    _sensors = staticmethod(TestSampleAttribution._sensors)
    _Sample = TestSampleAttribution._Sample
    _Decoder = TestSampleAttribution._Decoder
    _Decoded = TestSampleAttribution._Decoded

    class _Buffering:
        """Turns no packet into a frame, but holds one back for the flush."""

        def parse(self, payload):
            return [payload]

        def decode(self, packet):
            if packet is None:
                return [TestSampleAttribution._Decoded()]
            return []

    def test_a_mid_stream_encoding_change_rebuilds_the_decoder(self):
        sensors = self._sensors()
        asked_for = []

        def make_decoder(encoding):
            asked_for.append(encoding)
            return self._Decoder(per_packet=1)

        frames = list(
            sensors.decode_samples(
                [
                    self._Sample(1, encoding="h264"),
                    self._Sample(2, encoding="h264"),
                    self._Sample(3, encoding="vp8"),
                ],
                make_decoder,
            )
        )
        # One decoder per encoding, not one per sample and not one per stream.
        self.assertEqual(asked_for, ["h264", "vp8"])
        self.assertEqual([f.sample_ids for f in frames], [[1], [2], [3]])

    def test_an_empty_first_encoding_does_not_pin_the_decoder(self):
        sensors = self._sensors()
        asked_for = []

        def make_decoder(encoding):
            asked_for.append(encoding)
            return self._Decoder(per_packet=1)

        frames = list(
            sensors.decode_samples(
                [
                    self._Sample(1, encoding=""),
                    self._Sample(2, encoding="h264"),
                    self._Sample(3, encoding="vp8"),
                ],
                make_decoder,
            )
        )
        # The empty encoding resolves to the documented default, so the explicit
        # "h264" that follows is the same codec and must not rebuild anything.
        self.assertEqual(asked_for, ["h264", "vp8"])
        self.assertEqual([f.sample_ids for f in frames], [[1], [2], [3]])

    def test_the_retired_decoder_is_flushed_with_its_own_attribution(self):
        sensors = self._sensors()
        built = []

        def make_decoder(encoding):
            decoder = self._Buffering() if encoding == "h264" else self._Decoder(per_packet=1)
            built.append(encoding)
            return decoder

        frames = list(
            sensors.decode_samples(
                [
                    self._Sample(1, encoding="h264", dropped_before=2),
                    self._Sample(2, encoding="h264"),
                    self._Sample(3, encoding="vp8"),
                ],
                make_decoder,
            )
        )
        self.assertEqual(built, ["h264", "vp8"])
        self.assertEqual(len(frames), 2)
        # The flushed frame belongs to the samples that fed the old decoder, and
        # carries their drop count and the timestamp of the last of them, not
        # those of the first sample under the new encoding.
        self.assertEqual(frames[0].sample_ids, [1, 2])
        self.assertEqual(frames[0].dropped_before, 2)
        self.assertEqual(frames[0].boottime_nanos, 2 * 1000)
        # The bytes buffered under the old codec are not credited to the new one.
        self.assertEqual(frames[1].sample_ids, [3])
        self.assertEqual(frames[1].dropped_before, 0)

    def test_a_decoder_that_cannot_be_flushed_does_not_end_the_stream(self):
        sensors = self._sensors()

        class Unflushable:
            def parse(self, payload):
                return [payload]

            def decode(self, packet):
                if packet is None:
                    raise RuntimeError("this codec cannot be flushed")
                return []

        def make_decoder(encoding):
            return Unflushable() if encoding == "h264" else self._Decoder(per_packet=1)

        with self.assertLogs("wendysensors", level="WARNING"):
            frames = list(
                sensors.decode_samples(
                    [self._Sample(1, encoding="h264"), self._Sample(2, encoding="vp8")],
                    make_decoder,
                )
            )
        self.assertEqual([f.sample_ids for f in frames], [[2]])


class TestSensorStreamReconnect(unittest.TestCase):
    """A stream that ends mid-life (an agent restart, a dropped socket) must be
    redialled, or the app exits while the campaign is still armed. Bounded, so a
    socket that is gone for good still exits with a diagnosis."""

    _sensors = staticmethod(TestSampleAttribution._sensors)

    class _Client:
        """Replays a scripted series of subscriptions, one per frames() call."""

        def __init__(self, streams):
            self.streams = list(streams)
            self.calls = 0
            self.closes = 0

        def frames(self):
            self.calls += 1
            if not self.streams:
                return
            stream = self.streams.pop(0)
            if isinstance(stream, Exception):
                raise stream
            yield from stream

        def close(self):
            self.closes += 1

    def test_frames_resume_after_the_stream_ends(self):
        sensors = self._sensors()
        client = self._Client([["a"], ["b", "c"]])
        slept = []
        with self.assertLogs("wendysensors", level="WARNING"):
            got = list(
                sensors.frames_with_reconnect(client, attempts=2, delay=0.5, sleep=slept.append)
            )
        # The frames from before and after the drop are one stream to the caller.
        self.assertEqual(got, ["a", "b", "c"])
        # Redialled after each of the two scripted streams ended, and once more
        # after the empty third: an end of stream is never assumed to be final.
        self.assertEqual(slept, [0.5, 0.5, 0.5])
        self.assertEqual(client.closes, 3)
        self.assertEqual(client.calls, 4)

    def test_a_failing_stream_is_retried_then_given_up_on(self):
        sensors = self._sensors()
        client = self._Client([RuntimeError("socket gone")] * 10)
        slept = []
        with self.assertLogs("wendysensors", level="WARNING"):
            got = list(
                sensors.frames_with_reconnect(client, attempts=3, delay=0.1, sleep=slept.append)
            )
        self.assertEqual(got, [])
        # Three reconnects, so four subscription attempts in total, and then it
        # stops rather than spinning on a socket that is not coming back.
        self.assertEqual(len(slept), 3)
        self.assertEqual(client.calls, 4)

    def test_the_budget_is_refilled_by_every_frame(self):
        sensors = self._sensors()
        # A budget of one, spent by every drop and refilled by every frame. Three
        # short-lived streams therefore all get through; a lifetime budget of one
        # would have stopped after the second.
        client = self._Client([["a"], ["b"], ["c"]])
        with self.assertLogs("wendysensors", level="WARNING"):
            got = list(sensors.frames_with_reconnect(client, attempts=1, delay=0, sleep=lambda _: None))
        self.assertEqual(got, ["a", "b", "c"])

    def test_consecutive_drops_exhaust_the_budget(self):
        sensors = self._sensors()
        # The same budget of one, but nothing arrives to refill it: the stream
        # that ends and the failure after it are two consecutive drops, so the
        # third subscription is never attempted.
        client = self._Client([["a"], RuntimeError("socket gone"), ["b"]])
        with self.assertLogs("wendysensors", level="WARNING"):
            got = list(sensors.frames_with_reconnect(client, attempts=1, delay=0, sleep=lambda _: None))
        self.assertEqual(got, ["a"])
        self.assertEqual(client.calls, 2)

    def test_zero_attempts_exits_on_the_first_end_of_stream(self):
        sensors = self._sensors()
        client = self._Client([["a"], ["b"]])
        with self.assertLogs("wendysensors", level="WARNING"):
            got = list(sensors.frames_with_reconnect(client, attempts=0, delay=0, sleep=lambda _: None))
        self.assertEqual(got, ["a"])
        self.assertEqual(client.calls, 1)


class TestPersonAppearance(unittest.TestCase):
    """`person_detected` is a campaign trigger, and the campaign starts one
    episode per trigger. So the event must fire once when a person arrives and
    stay quiet while they remain: level-triggering it would turn one person
    walking past into a stream of episodes."""

    @staticmethod
    def _app():
        """Import app.py with its model and imaging dependencies stubbed.

        The appearance rule needs neither a model nor OpenCV, and a unit test
        must not require onnxruntime to exercise it."""
        import sys
        import types

        TestSampleAttribution._sensors()
        for name in ("cv2", "numpy", "onnxruntime"):
            sys.modules.setdefault(name, types.ModuleType(name))
        import app

        return app

    @staticmethod
    def _person(confidence):
        return {"class_id": 0, "class_name": "person", "confidence": confidence}

    @staticmethod
    def _chair(confidence=0.7):
        return {"class_id": 56, "class_name": "chair", "confidence": confidence}

    def test_an_empty_frame_reports_nobody(self):
        app = self._app()
        self.assertEqual(app.person_appearance([], False), (False, None))

    def test_other_classes_are_not_people(self):
        app = self._app()
        # The scene this demo runs in is full of furniture; none of it may fire
        # the trigger.
        self.assertEqual(app.person_appearance([self._chair()], False), (False, None))

    def test_arrival_reports_the_best_confidence(self):
        app = self._app()
        present, confidence = app.person_appearance(
            [self._person(0.61), self._chair(), self._person(0.88)], False
        )
        self.assertTrue(present)
        self.assertAlmostEqual(confidence, 0.88)

    def test_staying_in_frame_fires_only_once(self):
        app = self._app()
        present, confidence = app.person_appearance([self._person(0.9)], False)
        self.assertEqual((present, confidence), (True, 0.9))
        # Every later frame of the same appearance stays silent, so the campaign
        # gets one episode covering the person rather than one per frame.
        for _ in range(20):
            present, confidence = app.person_appearance([self._person(0.9)], present)
            self.assertTrue(present)
            self.assertIsNone(confidence)

    def test_leaving_and_returning_fires_again(self):
        app = self._app()
        present, first = app.person_appearance([self._person(0.8)], False)
        self.assertEqual(first, 0.8)
        present, gone = app.person_appearance([], present)
        self.assertEqual((present, gone), (False, None))
        # A second, separate appearance is a second episode, which is correct:
        # it is a different moment worth recording.
        present, again = app.person_appearance([self._person(0.7)], present)
        self.assertEqual((present, again), (True, 0.7))


class TestFreshestFrame(unittest.TestCase):
    """Inference is far slower than capture, so a loop that scores every frame
    it is handed falls steadily behind and its predictions reference samples the
    episode has moved past. The backlog has to be discarded, and discarding it
    has to be counted: an episode that silently drops frames is not honest."""

    _sensors = staticmethod(TestSampleAttribution._sensors)

    def test_every_frame_is_yielded_when_the_consumer_keeps_up(self):
        sensors = self._sensors()
        # Nothing is dropped just for passing through: a consumer fast enough to
        # take each frame as it arrives must still see all of them. The producer
        # is gated so exactly one frame exists at a time, which makes "kept up"
        # a fact of the test rather than a race against the reader thread.
        gate = threading.Semaphore(0)

        def stream():
            for frame in ("a", "b", "c"):
                gate.acquire()
                yield frame

        frames = sensors.freshest_frames(stream())
        got = []
        for _ in range(3):
            gate.release()
            got.append(next(frames))
        self.assertEqual([f for f, _ in got], ["a", "b", "c"])
        self.assertEqual(sum(d for _, d in got), 0)

    def test_a_slow_consumer_gets_the_newest_frame_and_the_discard_count(self):
        sensors = self._sensors()
        took_first = threading.Event()
        produced_rest = threading.Event()

        def stream():
            yield "a"
            # Hold until the consumer has taken "a", then produce a burst it
            # never asked for: the reader running ahead while an inference is
            # in flight, which is the situation this whole helper exists for.
            took_first.wait(timeout=5)
            yield "b"
            yield "c"
            yield "d"
            produced_rest.set()

        frames = sensors.freshest_frames(stream())
        first = next(frames)
        self.assertEqual(first, ("a", 0))
        took_first.set()
        self.assertTrue(produced_rest.wait(timeout=5))
        # The consumer skips straight to the newest frame rather than working
        # through b and c, and is told exactly how many it passed over.
        self.assertEqual(next(frames), ("d", 2))
        # Four produced, two yielded, two counted: nothing vanishes unrecorded.
        self.assertEqual(list(frames), [])

    def test_the_last_frames_survive_the_end_of_the_stream(self):
        sensors = self._sensors()
        # A stream that ends immediately still has its frames delivered; the
        # end of stream must not race the final frame out of the slot.
        got = list(sensors.freshest_frames(iter(["only"])))
        self.assertEqual(got, [("only", 0)])

    def test_a_reader_failure_reaches_the_consumer(self):
        sensors = self._sensors()

        def stream():
            yield "a"
            raise RuntimeError("decoder exploded")

        frames = sensors.freshest_frames(stream())
        # The frame decoded before the failure is still delivered, and the
        # failure is then raised on the consumer's thread rather than vanishing
        # into the reader and stalling the app forever.
        self.assertEqual(next(frames), ("a", 0))
        with self.assertRaises(RuntimeError):
            next(frames)


if __name__ == "__main__":
    unittest.main()
