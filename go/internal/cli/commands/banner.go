package commands

import (
	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

// printPlatformBanner writes a one-line platform summary (or the full block
// under --verbose) to stderr. It is a no-op when WENDY_NO_BANNER is set. Target
// fields are absent here; they are appended later once a device is connected.
func printPlatformBanner(cmd *cobra.Command, verbose bool) {
	if env.NoBanner() {
		return
	}
	info := platforminfo.Collect()
	w := cmd.ErrOrStderr()
	if verbose {
		_, _ = w.Write([]byte(info.Block() + "\n"))
		return
	}
	_, _ = w.Write([]byte(info.OneLine() + "\n"))
}
