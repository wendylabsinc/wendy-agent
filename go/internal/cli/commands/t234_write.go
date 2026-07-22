//go:build darwin || linux

package commands

import (
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/t234"
)

// newT234WriteCmd builds the hidden `__t234-write` subcommand: the privileged
// half of the Orin USB-recovery flash. installOrin re-execs the CLI as
// `sudo wendy __t234-write …` for each raw block operation on the disks the
// flashing initrd exports (same pattern as `__bmap-write`). Flag parsing is
// delegated to t234.ParseWriterArgs so this re-exec path and the Windows
// in-process path can never disagree about the argument format.
func newT234WriteCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__t234-write",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := t234.ParseWriterArgs(args)
			if err != nil {
				return err
			}
			return t234.RunHelperRequest(req, cmd.OutOrStdout())
		},
	}
}
