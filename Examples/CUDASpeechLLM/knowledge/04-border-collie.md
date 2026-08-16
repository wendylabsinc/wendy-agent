# Border Collie Go2 demonstration

Border Collie is a clean-room Unitree Go2 demonstration. Woof can hear an
allowlisted fruit request, use local camera perception to find that fruit,
approach through freshness and geometry gates, sit and bark, and attempt to
return to the Home pose captured at startup.

The Wendy application separates three services. `app` owns the durable mission
state machine, readiness gates, motion lease, watchdog, evidence journal, and
narrow Unitree motion boundary. `media` is the sole camera owner and performs
GPU fruit inference, annotated streaming, evidence capture, and bark playback.
`voice` owns the microphone, “Hey Wendy” wake model, and local Parakeet ASR,
then submits one canonical allowlisted request to the existing mission API; it
does not own a second motion path.

Stale or wrong-generation perception cannot authorize motion. Search and
approach are bounded by speed, freshness, geometry, time, and progress.
Operator stop uses the shared safe-stop path, and each run records evidence and
its terminal safety state. This is an example of Wendy packaging independently
built GPU, audio, and robot-control services while retaining narrow authority
and observable readiness.
