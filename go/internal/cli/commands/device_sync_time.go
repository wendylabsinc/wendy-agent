package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	clitimesync "github.com/wendylabsinc/wendy/go/internal/cli/timesync"
)

func newDeviceSyncTimeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sync-time",
		Hidden: true,
		Short:  "Sync the clock on nearby WendyOS devices via Roughtime multicast",
		Long: `Queries a Roughtime server for a cryptographically signed timestamp,
then multicasts the signed proof to all WendyOS devices on the local network.
Devices verify the Roughtime signature themselves — the Mac is an untrusted relay.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := clitimesync.BroadcastTime(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("Synced: %s ± %s  (via %s)\n",
				result.Midpoint.UTC().Format("2006-01-02T15:04:05Z"),
				result.Radius.Round(time.Millisecond),
				result.Server)
			return nil
		},
	}
}
