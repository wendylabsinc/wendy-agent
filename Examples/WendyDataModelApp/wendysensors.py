"""Wendy sensor client: model input through the harness.

The harness feeds the model instead of the model opening a device. This
module wraps `wendy.agent.apps.v1.SensorService` on the app-private
socket the `sensors` entitlement mounts, and turns the identified encoded
samples it streams into decoded frames while keeping track of which
sample identifiers produced each frame.

Why this replaces `cv2.VideoCapture("/dev/video0")`:

  - Video4Linux2 admits one holder of a capture device. An app that opens
    the node itself takes it away from the episode capture adapter (and
    from any other reader), which is why this example used to need a
    telemetry-only campaign variant.
  - Subscribing makes the app one more consumer of the producer episode
    capture already consumes, so both get the same frames.
  - Every sample carries (source_id, sample_id). The episode records the
    same identifiers, so a prediction that names them can be paired with
    the exact bytes the model consumed. That pairing is what turns an
    episode into training data.

Samples arrive as encoded video (H.264 access units on the native V4L2
path, arbitrary byte-stream chunks on the GStreamer and network-camera
paths — `payload_self_contained` says which). Decoding is done here with
PyAV's parser, which handles both shapes, so a decoded frame may be the
product of more than one sample; `SensorFrame.sample_ids` lists every
sample that contributed to it, which is exactly what the prediction
record's `inputs` list is for.

`encoding` is per sample, not per stream, so the decoder is rebuilt
whenever it changes rather than being pinned by whichever sample happened
to arrive first.

Reading the stream and consuming it run at different speeds. Decoding is
cheap and inference is not, so a consumer that scores every frame it is
handed falls steadily behind the producer and ends up predicting on stale
samples. `freshest_frames` is the fix: it reads and decodes on its own
thread and keeps only the newest frame, so a prediction always references
samples the episode still holds.
"""

from __future__ import annotations

import logging
import os
import threading
import time
from dataclasses import dataclass, field

import grpc

from wendy.agent.apps.v1 import sensor_service_pb2 as sensorpb
from wendy.agent.apps.v1 import sensor_service_pb2_grpc as sensorgrpc

log = logging.getLogger("wendysensors")

DEFAULT_SOCKET_PATH = "/run/wendy/sensors/sensors.sock"

# The encoding assumed when a sample leaves the field empty. Normalizing here
# rather than in the decoder factory keeps the value the decoder was built for
# comparable to the value the next sample declares; if the two disagreed, an
# empty encoding followed by an explicit "h264" would rebuild the decoder for
# no reason and throw away the bytes it had buffered.
DEFAULT_ENCODING = "h264"

# How many consecutive times a dropped subscription is redialled before the
# stream is treated as gone for good, and how long to wait between attempts.
DEFAULT_RECONNECT_ATTEMPTS = 5
DEFAULT_RECONNECT_DELAY_SECONDS = 2.0


def socket_path() -> str:
    """The agent-injected sensor socket path, with the documented default."""
    return os.environ.get("WENDY_SENSOR_SOCKET", DEFAULT_SOCKET_PATH)


def channel_target(path: str | None = None) -> str:
    """A gRPC target for the app-private unix socket."""
    return "unix:" + (path or socket_path())


@dataclass
class SensorFrame:
    """One decoded frame plus the harness identity of its input samples."""

    source_id: str
    # Every sample identifier that contributed to this decoded frame, in
    # arrival order. Usually one; more when the transport delivers a byte
    # stream rather than whole access units.
    sample_ids: list[int]
    # CLOCK_BOOTTIME nanoseconds of the last contributing sample, with the
    # agent's bracket half-width.
    boottime_nanos: int
    uncertainty_nanos: int
    # Samples the harness produced but this subscriber never saw, summed
    # over the contributing samples. Non-zero means the model is not
    # keeping up, and the gap in sample_ids is explained rather than silent.
    dropped_before: int
    image: object = field(repr=False, default=None)

    def input_refs(self) -> list[dict]:
        """The prediction record's `inputs` value for this frame."""
        return [{"source_id": self.source_id, "sample_id": i} for i in self.sample_ids]


class SensorClient:
    """Subscribes to one sensor source and yields decoded frames."""

    def __init__(self, source_id: str, model: str = "", path: str | None = None):
        self.source_id = source_id
        self.model = model
        self.target = channel_target(path)
        self._channel: grpc.Channel | None = None

    def sources(self) -> list[sensorpb.SensorSource]:
        """Every source this app may subscribe to, subscribable or not."""
        stub = sensorgrpc.SensorServiceStub(self._connect())
        return list(stub.Sources(sensorpb.SensorSourcesRequest()).sources)

    def _connect(self) -> grpc.Channel:
        if self._channel is None:
            # A sample is one encoded frame; the agent caps it at 2 MiB.
            self._channel = grpc.insecure_channel(
                self.target, options=[("grpc.max_receive_message_length", 4 * 1024 * 1024)]
            )
        return self._channel

    def close(self) -> None:
        if self._channel is not None:
            self._channel.close()
            self._channel = None

    def frames(self):
        """Yield SensorFrame objects until the stream ends."""
        import av  # Imported here so the pure helpers stay importable without PyAV.

        stub = sensorgrpc.SensorServiceStub(self._connect())
        request = sensorpb.SensorSubscribeRequest(source_ids=[self.source_id], model=self.model)
        yield from decode_samples(
            stub.Subscribe(request),
            lambda encoding: av.CodecContext.create(encoding, "r"),
        )


def decode_samples(samples, make_decoder):
    """Turn identified samples into decoded frames that keep their sample ids.

    Separated from the gRPC call so the attribution rule below is testable
    without a live agent, a codec, or the generated stubs: it is the step that
    decides which sample identifiers a prediction may name, and getting it
    wrong loses the join key silently rather than loudly.
    """
    decoder = None
    # The encoding the current decoder was built for. The proto allows this to
    # change from sample to sample ("h264" or "vp8"), and a stream that switches
    # decodes to nothing at all under the decoder the first sample happened to
    # ask for, so the decoder follows the samples rather than the other way round.
    decoder_encoding = None
    pending_ids: list[int] = []
    pending_drops = 0
    # The most recent sample the pending bytes end at, so frames recovered by a
    # flush are timestamped by the sample that actually produced them and not by
    # the first sample of the next encoding.
    pending_tail = None
    for sample in samples:
        encoding = sample.encoding or DEFAULT_ENCODING
        if decoder is None or encoding != decoder_encoding:
            if decoder is not None:
                log.info(
                    "sample encoding changed from %s to %s; rebuilding the decoder",
                    decoder_encoding,
                    encoding,
                )
                yield from _flush_decoder(decoder, pending_ids, pending_drops, pending_tail)
                # Whatever the retired decoder could not turn into a frame was
                # encoded by the codec being replaced, so the new decoder can
                # never produce a frame from it. Dropping the accumulation here
                # is what keeps the next codec's frames from being credited with
                # sample identifiers they were not computed from.
                pending_ids = []
                pending_drops = 0
                pending_tail = None
            decoder = make_decoder(encoding)
            decoder_encoding = encoding
        pending_ids.append(sample.sample_id)
        pending_drops += sample.dropped_before
        pending_tail = sample
        emitted = False
        for packet in decoder.parse(sample.payload):
            for decoded in decoder.decode(packet):
                yield SensorFrame(
                    source_id=sample.source_id,
                    sample_ids=list(pending_ids),
                    boottime_nanos=sample.boottime_nanos,
                    uncertainty_nanos=sample.timestamp_uncertainty_nanos,
                    # Counted once per byte run. Several decoded frames can
                    # come out of the same samples, and repeating the drop
                    # count on each would overstate what was lost.
                    dropped_before=0 if emitted else pending_drops,
                    image=decoded.to_ndarray(format="bgr24"),
                )
                emitted = True
        if emitted:
            # Cleared once the sample has been fully decoded, not after the
            # first frame it yielded. A packet can decode to several frames
            # (a reorder flush, or several frames in one packet), and every one
            # of them really was computed from these samples. Clearing per
            # frame left the second and later frames with no sample_ids at all,
            # so input_refs() returned nothing and their predictions shipped
            # with no `inputs` field at all — silently losing the join key this
            # whole path exists to carry.
            pending_ids = []
            pending_drops = 0
            pending_tail = None


def _flush_decoder(decoder, sample_ids, dropped_before, tail):
    """Drain the frames a decoder still holds before it is replaced.

    Called only at an encoding change. The buffered bytes belong to the codec
    being retired, so this is the last moment anything can be decoded from them,
    and the samples that fed them are still known: a frame recovered here keeps
    the same attribution it would have had on the next sample. A decoder that
    cannot be flushed loses the buffered samples, which is logged rather than
    allowed to end the stream.
    """
    if not sample_ids or tail is None:
        return
    try:
        drained = list(decoder.decode(None))
    except Exception as exc:  # noqa: BLE001 - any codec failure here is survivable
        log.warning(
            "could not flush the decoder at an encoding change; %d buffered sample(s) produced no frame: %s",
            len(sample_ids),
            exc,
        )
        return
    emitted = False
    for decoded in drained:
        yield SensorFrame(
            source_id=tail.source_id,
            sample_ids=list(sample_ids),
            boottime_nanos=tail.boottime_nanos,
            uncertainty_nanos=tail.timestamp_uncertainty_nanos,
            dropped_before=0 if emitted else dropped_before,
            image=decoded.to_ndarray(format="bgr24"),
        )
        emitted = True
    if not emitted:
        log.info(
            "%d buffered sample(s) produced no frame before the encoding change",
            len(sample_ids),
        )


def freshest_frames(frames):
    """Yield the newest decoded frame, discarding whatever queued up behind it.

    A real-time inference loop has to judge the freshest frame available, not
    drain a queue. The producer keeps running while the model is busy, so
    anything that accumulated behind the frame being scored is already history
    by the time the model reaches it. Worse, it is history that compounds: each
    prediction falls a little further behind than the last, and the references
    it carries name samples the episode has long since moved past. That is
    precisely how a model input/outcome join ends up with nothing to pair, even
    though both sides recorded honestly.

    The backlog lives in the gRPC receive buffer, and the only way to learn
    what is behind the current sample is to read it. So reading and decoding
    run on their own thread and the newest decoded frame is kept in a
    single-entry slot; a consumer that asks for a frame gets the most recent
    one and the count of frames that were dropped to get there.

    Decoding unconditionally is affordable, which is what makes this work: on a
    Jetson Orin Nano one 1280x720 H.264 frame decodes in about 7 ms against
    roughly 450 ms for one YOLOv8n inference on the CPU, so the decoder keeps
    pace with the producer using a few percent of a core while the consumer
    scores whatever is newest. Decode is not the expensive step; inference is.

    Yields (frame, discarded) pairs. `discarded` is the number of decoded
    frames thrown away since the previous yield, so the caller can report what
    it skipped instead of leaving a silent gap. Frames are dropped here only
    after being fully decoded, so a yielded frame's `sample_ids` still names
    exactly the samples it was computed from.
    """
    state = threading.Condition()
    slot: dict = {"frame": None, "discarded": 0, "done": False, "error": None}

    def reader():
        try:
            for frame in frames:
                with state:
                    if slot["frame"] is not None:
                        # A frame the consumer never asked for. Counted rather
                        # than quietly overwritten: the prediction that finally
                        # lands has to be able to say how much it passed over.
                        slot["discarded"] += 1
                    slot["frame"] = frame
                    state.notify()
        except BaseException as exc:  # noqa: BLE001 - re-raised on the consumer thread
            with state:
                slot["error"] = exc
                state.notify()
        finally:
            with state:
                slot["done"] = True
                state.notify()

    thread = threading.Thread(target=reader, name="sensor-decode", daemon=True)
    thread.start()
    while True:
        with state:
            while slot["frame"] is None and not slot["done"]:
                state.wait()
            frame = slot["frame"]
            discarded = slot["discarded"]
            slot["frame"] = None
            slot["discarded"] = 0
            error = slot["error"]
            done = slot["done"]
        if frame is not None:
            # Drained before any end-of-stream or failure is reported, so the
            # last frames the decoder managed to produce are still scored.
            yield frame, discarded
            continue
        if error is not None:
            raise error
        if done:
            return


def frames_with_reconnect(
    client,
    attempts: int = DEFAULT_RECONNECT_ATTEMPTS,
    delay: float = DEFAULT_RECONNECT_DELAY_SECONDS,
    sleep=time.sleep,
):
    """Yield frames across a subscription that ends part way through its life.

    `SensorClient.frames()` ends whenever the subscription does, and a
    subscription ends for reasons that have nothing to do with the app: the
    agent restarted, or the socket went away with it. The data side already
    reconnects and retries (`wendydata.DataSocketClient.send`), so the input
    side has to as well; without it a single agent restart ends the app quietly
    while the campaign is still armed and there is still an episode to fill.

    Bounded on purpose. A socket that is gone for good has to exit with a
    diagnosis rather than spin forever, so the budget is finite. It counts
    *consecutive* failures and is refilled by every frame that arrives, so a
    long-lived app is not eventually killed by unrelated restarts spread over
    days.

    The source identifier is kept across reconnects. Harness identifiers are
    stable, and re-resolving on every drop would let a transient failure move
    the app onto a different camera without saying so.
    """
    remaining = attempts
    while True:
        try:
            for frame in client.frames():
                remaining = attempts
                yield frame
        except Exception as exc:  # noqa: BLE001 - grpc.RpcError and transport errors alike
            log.warning("sensor stream failed: %s", exc)
        else:
            log.warning("sensor stream ended")
        if remaining <= 0:
            log.error(
                "the sensor stream did not come back after %d reconnect attempt(s); giving up",
                attempts,
            )
            return
        remaining -= 1
        # Drop the channel so the next Subscribe redials rather than reusing a
        # connection the agent has already torn down.
        client.close()
        log.info(
            "reconnecting to the sensor socket in %.1fs (%d attempt(s) left after this one)",
            delay,
            remaining,
        )
        sleep(delay)
