package main

import (
	"os"
	"strconv"
)

// simOnlyEnvVar opts the agent into sim-only mode: the agent runs purely as a
// sim front (e.g. natively on macOS with WENDY_SIM_BACKEND_ADDR pointing at a
// sim container) where no containerd is available. All gRPC services are still
// constructed and registered; only the containerd-backed background loops and
// boot-time one-shots are not launched, silencing the otherwise constant
// containerd dial timeouts.
const simOnlyEnvVar = "WENDY_SIM_ONLY"

// simOnlyMode reports whether WENDY_SIM_ONLY is set to a true boolean value
// ("1", "true", ...). Unset, empty, or unparseable values mean off, so the
// default behavior is unchanged.
func simOnlyMode() bool {
	v, err := strconv.ParseBool(os.Getenv(simOnlyEnvVar))
	return err == nil && v
}
