package commands

import (
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// implicitDeviceReason describes how a device was chosen when the user did not
// name one. The two cases need different wording: the direct path falls back to
// the device recorded by `wendy device set-default`, while the cloud path never
// consults that setting and instead auto-selects when the org has exactly one
// enrolled device. Calling the latter a "default" would be untrue.
type implicitDeviceReason int

const (
	// implicitDefaultDevice is the hostname from config, used because neither
	// --device nor an interactive pick supplied one.
	implicitDefaultDevice implicitDeviceReason = iota
	// implicitSoleCloudDevice is the org's only enrolled cloud asset, selected
	// without asking because there was nothing to choose between.
	implicitSoleCloudDevice
)

// noticedImplicitDevice guards against repeating the notice inside one
// invocation. connectResolvedAgent is called again on the retry paths in
// connectToAgent, which would otherwise announce the same device up to four
// times for a single command.
var noticedImplicitDevice bool

// noteImplicitDevice tells the user which device a command acted on when they
// did not name one, so an implicit target is never invisible.
//
// The line naming the device prints every time, because that is the whole
// point: knowing which machine was touched. The follow-up explaining how to
// change it is throttled to once a day, since repeating it on every command
// would be noise rather than help.
func noteImplicitDevice(name string, reason implicitDeviceReason) {
	if name == "" || noticedImplicitDevice {
		return
	}
	// Machine-readable output must stay parseable, and the existing
	// default-device spinner is gated the same way. Note the CLI turns this on by
	// itself when stdout is not a terminal, so piped and CI runs stay quiet.
	if jsonOutput {
		return
	}
	noticedImplicitDevice = true

	withHint := !implicitDeviceHintShownToday()
	for _, line := range implicitDeviceLines(tui.Device(name), reason, withHint) {
		cliNotice("%s", line)
	}
	if withHint {
		recordImplicitDeviceHintShown()
	}
}

// implicitDeviceLines builds the notice. Split out from the printing so the
// wording and the hint-throttling decision can be asserted in tests.
func implicitDeviceLines(name string, reason implicitDeviceReason, withHint bool) []string {
	var lines []string
	switch reason {
	case implicitSoleCloudDevice:
		lines = append(lines, "Using "+name+", the only device enrolled in this organisation.")
	default:
		lines = append(lines, "Using default device "+name+".")
	}
	if withHint {
		lines = append(lines, "Target a different device for one command with --device, or change the default with 'wendy device set-default'.")
	}
	return lines
}

func implicitDeviceHintToday() string {
	return time.Now().Format("2006-01-02")
}

func implicitDeviceHintShownToday() bool {
	cfg, err := config.Load()
	if err != nil {
		// Without config state, prefer showing the hint over hiding it.
		return false
	}
	return cfg.ImplicitDeviceHintShownAt == implicitDeviceHintToday()
}

func recordImplicitDeviceHintShown() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	cfg.ImplicitDeviceHintShownAt = implicitDeviceHintToday()
	_ = config.Save(cfg)
}
