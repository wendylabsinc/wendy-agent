Hosted CI runs local coverage on macOS and Ubuntu.

Authenticated tests use a dedicated E2E fixture, never personal Wendy config.
Cloud state, interactive prompts, remote targets, and physical hardware need
explicit fixtures or targets. Physical-device CI routes remain disabled until
the hardware is reliable enough to be a useful gate.
