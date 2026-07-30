package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// newDriversCmd is the "drivers" command group under `wendy device`, mirroring
// `wendy device apps`. Driver add-ons are prebuilt, signed systemd-sysext images
// applied on top of the immutable /usr; see the agent's WendyDriverService.
func newDriversCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "drivers",
		Aliases: []string{"driver"},
		Short:   "Manage kernel driver add-ons on the target device",
	}
	cmd.AddCommand(
		newDriversListCmd(),
		newDriversInstallCmd(),
		newDriversRemoveCmd(),
	)
	return cmd
}

func newDriversListCmd() *cobra.Command {
	var available bool
	var pr int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed driver add-ons (or --available: those published for this device)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("selected device does not support driver add-ons")
			}
			if available {
				return renderAvailableDrivers(ctx, target, pr)
			}
			resp, err := target.Agent.DriverService.ListDrivers(ctx, &agentpbv2.ListDriversRequest{})
			if err != nil {
				return fmt.Errorf("listing drivers: %w", err)
			}
			return renderDriversList(resp)
		},
	}
	cmd.Flags().BoolVar(&available, "available", false, "List drivers published in the registry for this device's OS + kernel")
	cmd.Flags().IntVar(&pr, "pr", 0, "With --available, resolve against a per-PR build manifest")
	return cmd
}

// renderAvailableDrivers lists the driver add-ons published for the device's OS
// version, filtered to its running kernel and marking those already installed.
func renderAvailableDrivers(ctx context.Context, target *SelectedDevice, pr int) error {
	deviceType, dl, err := deviceDriverCoords(ctx, target)
	if err != nil {
		return err
	}
	osVersion, kernel := dl.GetBaseVersion(), dl.GetKernelVersion()
	exts, err := driverExtensionsFor(deviceType, osVersion, pr)
	if err != nil {
		return err
	}

	installed := map[string]bool{}
	for _, d := range dl.GetInstalled() {
		installed[d.GetName()] = true
	}

	// Only add-ons built for this device's running kernel are installable here.
	type avail struct {
		e     extensionEntry
		local bool
	}
	var rows []avail
	for _, e := range exts {
		if !kernelMatches(e, kernel) {
			continue
		}
		rows = append(rows, avail{e, installed[e.Name]})
	}

	if jsonOutput {
		type jr struct {
			Name          string   `json:"name"`
			Version       string   `json:"version,omitempty"`
			KernelVersion string   `json:"kernelVersion"`
			Modules       []string `json:"modulesLoad,omitempty"`
			Installed     bool     `json:"installed"`
		}
		out := struct {
			DeviceType string `json:"deviceType"`
			OSVersion  string `json:"osVersion"`
			Kernel     string `json:"kernel"`
			Available  []jr   `json:"available"`
		}{DeviceType: deviceType, OSVersion: osVersion, Kernel: kernel}
		for _, r := range rows {
			out.Available = append(out.Available, jr{r.e.Name, r.e.Version, r.e.KernelVersion, r.e.ModulesLoad, r.local})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	cliLogln("Available for %s · OS %s · kernel %s", deviceType, osVersion, kernel)
	if len(rows) == 0 {
		cliLogln("No drivers published for this OS version + kernel.")
		return nil
	}
	// A filled dot marks an add-on already installed on the device.
	headers := []string{"", "Name", "Version", "Modules"}
	var trows [][]string
	for _, r := range rows {
		icon := stateIconStopped
		if r.local {
			icon = stateIconRunning
		}
		trows = append(trows, []string{icon, r.e.Name, r.e.Version, strings.Join(r.e.ModulesLoad, ", ")})
	}
	fmt.Print(tui.RenderTable(headers, trows))
	return nil
}

func renderDriversList(resp *agentpbv2.ListDriversResponse) error {
	if jsonOutput {
		type jsonDriver struct {
			Name    string   `json:"name"`
			Modules []string `json:"modulesLoad,omitempty"`
			Loaded  bool     `json:"loaded"`
		}
		out := struct {
			BaseVersion   string       `json:"baseVersion"`
			KernelVersion string       `json:"kernelVersion"`
			Installed     []jsonDriver `json:"installed"`
			LoadedModules []string     `json:"loadedModules,omitempty"`
		}{
			BaseVersion:   resp.GetBaseVersion(),
			KernelVersion: resp.GetKernelVersion(),
			LoadedModules: resp.GetLoadedModules(),
		}
		for _, d := range resp.GetInstalled() {
			out.Installed = append(out.Installed, jsonDriver{
				Name:    d.GetName(),
				Modules: d.GetModulesLoad(),
				Loaded:  d.GetLoaded(),
			})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	cliLogln("OS %s · kernel %s", resp.GetBaseVersion(), resp.GetKernelVersion())
	if len(resp.GetInstalled()) == 0 {
		cliLogln("No driver add-ons installed.")
		return nil
	}
	// A filled dot marks a driver whose modules are currently loaded (realized),
	// an empty dot one that is merged but not (yet) loaded. No Version column:
	// the agent does not persist per-driver version metadata yet.
	headers := []string{"", "Name", "Modules"}
	var rows [][]string
	for _, d := range resp.GetInstalled() {
		icon := stateIconStopped
		if d.GetLoaded() {
			icon = stateIconRunning
		}
		rows = append(rows, []string{icon, d.GetName(), strings.Join(d.GetModulesLoad(), ", ")})
	}
	fmt.Print(tui.RenderTable(headers, rows))
	return nil
}

func newDriversRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a driver add-on from the device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]

			if !force {
				confirmed, err := tui.Confirm(fmt.Sprintf("Remove driver %s?", name))
				if err != nil {
					if errors.Is(err, tui.ErrCancelled) {
						return ErrUserCancelled
					}
					return err
				}
				if !confirmed {
					cliNotice("Cancelled.")
					return nil
				}
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("selected device does not support driver add-ons")
			}
			// No --purge flag: the MVP has no content-addressed rollback store,
			// so remove always deletes the .raw (RemoveDriverRequest.purge is
			// reserved for when the store lands).
			stream, err := target.Agent.DriverService.RemoveDriver(ctx, &agentpbv2.RemoveDriverRequest{Name: name})
			if err != nil {
				return fmt.Errorf("removing driver: %w", err)
			}
			return consumeDriverApply(stream, name)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip the confirmation prompt")
	return cmd
}

// driverApplyRecvStream is satisfied by both the InstallDriver (bidi) and
// RemoveDriver (server-streaming) client streams.
type driverApplyRecvStream interface {
	Recv() (*agentpbv2.DriverApplyResponse, error)
}

// consumeDriverApply prints streamed apply progress and returns the outcome.
func consumeDriverApply(stream driverApplyRecvStream, name string) error {
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return fmt.Errorf("driver %s: stream ended without a result", name)
		}
		if err != nil {
			return err
		}
		switch {
		case resp.GetProgress() != nil:
			p := resp.GetProgress()
			cliLogln("  %s… %d%%", p.GetPhase(), p.GetPercent())
		case resp.GetCompleted() != nil:
			c := resp.GetCompleted()
			cliSuccess("Driver %s applied.", c.GetName())
			if c.GetRebootRequired() {
				cliLogln("  A reboot is required to finish.")
			}
			return nil
		case resp.GetFailed() != nil:
			return fmt.Errorf("driver %s: %s", name, resp.GetFailed().GetErrorMessage())
		}
	}
}
