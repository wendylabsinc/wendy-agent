//go:build !darwin && !linux

package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newT234WriteCmd is a stub on platforms without USB recovery flashing.
func newT234WriteCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__t234-write",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("__t234-write is supported on macOS and Linux only")
		},
	}
}
