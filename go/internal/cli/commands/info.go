package commands

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Display CLI version and system information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeCLIInfo(cmd, jsonOutput)
		},
	}
}

// writeCLIInfo emits the local, non-identifying diagnostics shared by the
// backwards-compatible `wendy info` command and the `wendy --host-info` flag.
func writeCLIInfo(cmd *cobra.Command, asJSON bool) error {
	info := map[string]string{
		"version":   version.Version,
		"os":        runtime.GOOS,
		"osVersion": hostOSVersion(),
		"arch":      runtime.GOARCH,
		"goVersion": runtime.Version(),
	}

	if asJSON {
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}

	osVersion := info["osVersion"]
	if osVersion == "" {
		osVersion = "unknown"
	}
	lines := []string{
		"Wendy CLI",
		fmt.Sprintf("  Version:    %s", info["version"]),
		fmt.Sprintf("  OS:         %s", info["os"]),
		fmt.Sprintf("  OS Version: %s", osVersion),
		fmt.Sprintf("  Arch:       %s", info["arch"]),
		fmt.Sprintf("  Go Version: %s", info["goVersion"]),
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
	return err
}
