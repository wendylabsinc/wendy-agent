# Walter's G1 and Spark implementation

Walter is a Unitree G1. The intended split keeps robot-specific action safety
on the G1 and heavy speech/model compute on the DGX Spark. The PowerConf is
plugged into the Spark and is used for both microphone capture and speaker
playback.

The G1 side owns a guarded action library and synchronization gateway. The
action runtime checks the master gate, supported FSM, AI mode, and onboard
Unitree action catalog before any allowlisted upper-body action. The sync
gateway supports authenticated status, clock exchange, preparation,
scheduling, cancellation, and timing observations.

The Spark side owns the Ultravox speech-language model, Kokoro text-to-speech,
audio capture/playback, orchestration, and optional Hermes memory and skills.
This direct PR 1719 voice application sends 16 kHz PowerConf audio to an
Ultravox v0.5 audio projector plus Llama 3.1 8B Q4_K_M through llama.cpp, then
sends the reply through Kokoro and directly back to the PowerConf with ALSA.
It intentionally does not depend on a browser for audio.

Timing keeps user speech onset/end, model response, audio render, Unitree
command dispatch, and visible physical-motion onset separate. Physical motion
may lag dispatch inside the Unitree controller, and PowerConf DSP may make
acoustic onset lag the first host write. Those are measured independently.

G1 and Spark apps have separate identities, versions, build contexts, caches,
targets, and readiness boundaries so they can deploy concurrently. Stable
models remain in persistent volumes while frequently changed application code
is replaced quickly. A legacy wake, VAD, ASR, LLM, and TTS path remains a
commissioning fallback, but only one audio owner should run at a time.
