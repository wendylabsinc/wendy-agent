//go:build darwin || linux

package commands

import (
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/t234"
)

// newT234WriteCmd builds the hidden `__t234-write` subcommand: the privileged
// half of the Orin USB-recovery flash. installOrin re-execs the CLI as
// `sudo wendy __t234-write …` for each raw block operation on the disks the
// flashing initrd exports (same pattern as `__bmap-write`).
func newT234WriteCmd() *cobra.Command {
	var device, blob, bundleDir, dumpTo, releaseSerial string
	var writePlan, release bool
	var dumpBytes int64
	cmd := &cobra.Command{
		Use:    "__t234-write",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if release {
				return t234.ReleaseUSB(releaseSerial)
			}
			return t234.RunWriter(t234.WriterOptions{
				Device:    device,
				Blob:      blob,
				WritePlan: writePlan,
				BundleDir: bundleDir,
				DumpTo:    dumpTo,
				DumpBytes: dumpBytes,
				Progress:  cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&device, "device", "", "Raw block device to operate on")
	cmd.Flags().StringVar(&blob, "blob", "", "Write this file at offset 0")
	cmd.Flags().BoolVar(&writePlan, "write-plan", false, "Write the bundle's GPT + partition images")
	cmd.Flags().StringVar(&bundleDir, "bundle", "", "Extracted bundle root (with wendy-prep/plan.json)")
	cmd.Flags().StringVar(&dumpTo, "dump", "", "Read the device into this file")
	cmd.Flags().Int64Var(&dumpBytes, "bytes", 0, "Bytes to read with --dump")
	cmd.Flags().BoolVar(&release, "release", false, "Force-disconnect the flashing gadget")
	cmd.Flags().StringVar(&releaseSerial, "serial", "", "USB serial to match with --release")
	return cmd
}
