package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// defaultAppStoreAPIBase is the Wendy Cloud API that resolves AppStore app ids
// to install manifests. This is the deployed wendy-cloud-services Cloud Run
// service (the same host as defaultCloudGRPC in auth.go); cloud.wendy.sh is the
// web dashboard, not the API. The api.wendy.sh domain mapping will alias this
// service once its DNS is live. Override with --api or the WENDY_APPSTORE_API
// env var.
const defaultAppStoreAPIBase = "https://wendy-cloud-services-114319063177.us-central1.run.app"

// resolveAppStoreAPIBase picks the AppStore API base, preferring the flag, then
// the WENDY_APPSTORE_API env var, then the built-in default. The returned value
// has no trailing slash.
func resolveAppStoreAPIBase(flagVal string) string {
	base := flagVal
	if base == "" {
		base = os.Getenv("WENDY_APPSTORE_API")
	}
	if base == "" {
		base = defaultAppStoreAPIBase
	}
	return strings.TrimRight(base, "/")
}

// newAppCmd is the top-level "app" command group, the AppStore-facing entry
// point. Device-scoped management (list/start/stop/remove) lives under
// "wendy device apps"; this group surfaces "wendy app install <app-id>" to match
// the install command shown on https://appstore.wendy.dev.
func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Install apps from the Wendy AppStore",
		Long:  "Browse https://appstore.wendy.dev, then install an app onto a device with 'wendy app install <app-id>'.",
	}
	cmd.AddCommand(newAppsInstallCmd())
	return cmd
}

func newAppsInstallCmd() *cobra.Command {
	var apiBase string
	var noStart bool

	cmd := &cobra.Command{
		Use:   "install <app-id>",
		Short: "Install an app from the Wendy AppStore",
		Long: "Resolves an AppStore app id to an install manifest (one or more container " +
			"services, wired together with dependency ordering, secrets, and shared /etc/hosts " +
			"entries) and deploys it to the target device. See https://appstore.wendy.dev.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			appID := args[0]

			base := resolveAppStoreAPIBase(apiBase)
			manifest, err := resolveAppManifest(ctx, base, appID)
			if err != nil {
				return err
			}
			order, reqs, err := buildServiceInstall(manifest)
			if err != nil {
				return fmt.Errorf("preparing install for %s: %w", appID, err)
			}
			if len(order) == 1 {
				cliLogln("Resolved %s to %s", appID, reqs[order[0]].ImageName)
			} else {
				cliLogln("Resolved %s to %d services: %s", appID, len(order), strings.Join(order, ", "))
			}

			target, err := resolveTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("selected device does not support installing apps")
			}
			svc := target.Agent.ContainerService

			// Create + start each service in dependency order. Each service's Started
			// ack must complete before the next is created so the next service's
			// /etc/hosts already resolves its dependencies (the agent records a
			// service's bridge IP at StartContainer time).
			for _, name := range order {
				cliLogln("Installing service %s...", name)
				if err := createContainerWithProgress(ctx, svc, reqs[name]); err != nil {
					return fmt.Errorf("installing service %s: %w", name, err)
				}
				if noStart {
					continue
				}
				if err := startInstalledService(ctx, svc, reqs[name].AppName); err != nil {
					return fmt.Errorf("starting service %s: %w", name, err)
				}
			}
			if noStart {
				cliSuccess("Installed %s (%d service(s), not started).", appID, len(order))
				return nil
			}
			cliSuccess("Installed and started %s (%d service(s)).", appID, len(order))
			return nil
		},
	}

	cmd.Flags().StringVar(&apiBase, "api", "", "Wendy AppStore API base URL (default: $WENDY_APPSTORE_API or "+defaultAppStoreAPIBase+")")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "Create the app container(s) but do not start them")
	return cmd
}

// startInstalledService starts a single installed service and waits for the
// agent's Started ack. StartContainer is server-streaming; the first Recv is
// that ack (mirrors the drain in multibuild.go's startAndStreamServices).
func startInstalledService(ctx context.Context, svc agentpb.WendyContainerServiceClient, appName string) error {
	stream, err := svc.StartContainer(ctx, &agentpb.StartContainerRequest{
		AppName:       appName,
		RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
	})
	if err != nil {
		return err
	}
	if _, err := stream.Recv(); err != nil && err != io.EOF {
		return err
	}
	return nil
}
