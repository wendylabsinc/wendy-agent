package commands

import (
	"fmt"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/mount"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func newMountCmd() *cobra.Command {
	var (
		protocol   string
		readOnly   bool
		mountpoint string
	)
	cmd := &cobra.Command{
		Use:   "mount <volume> [mountpoint]",
		Short: "Mount a persistent volume as a local drive",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			volume := args[0]
			if len(args) == 2 {
				mountpoint = args[1]
			}

			parentCtx := cmd.Context()
			target, err := resolveTarget(parentCtx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("mounting requires a WendyOS device")
			}

			// Validate the volume exists and warn if a running app uses it.
			vols, err := target.Agent.ContainerService.ListVolumes(parentCtx, &agentpb.ListVolumesRequest{})
			if err != nil {
				return fmt.Errorf("listing volumes: %w", err)
			}
			found := false
			for _, v := range vols.GetVolumes() {
				if v.GetName() == volume {
					found = true
					if len(v.GetUsedBy()) > 0 {
						fmt.Fprintf(cmd.OutOrStderr(),
							"warning: volume %q is in use by %v; writes from your PC and the app can corrupt data\n",
							volume, v.GetUsedBy())
					}
				}
			}
			if !found {
				return fmt.Errorf("volume %q not found", volume)
			}

			deviceName := target.Agent.Host
			if mountpoint == "" {
				if protocol == "webdav" || (protocol == "" && runtime.GOOS == "windows") {
					mountpoint = "W:"
				} else {
					mountpoint, err = mount.DefaultMountpoint(deviceName, volume)
					if err != nil {
						return err
					}
				}
			}

			// Cancel on SIGINT/SIGTERM for a clean unmount.
			ctx, stop := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			fsc := mount.NewFSClient(ctx, target.Agent.VolumeFsService, volume)
			return mount.Run(ctx, mount.Options{
				FS:         fsc,
				Protocol:   protocol,
				Mountpoint: mountpoint,
				ReadOnly:   readOnly,
				DeviceName: deviceName,
				Volume:     volume,
				Stdout:     cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "", "mount protocol: nfs or webdav (default: per-OS)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "mount read-only")
	return cmd
}

func newUnmountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unmount <mountpoint|drive>",
		Short: "Unmount a volume mounted by 'wendy device mount'",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mount.Unmount(args[0])
		},
	}
}
