package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// A driver add-on is built for one exact kernel, so an OS update that bumps it
// stops the add-on merging — visible only in the device journal. These advisories
// are informational: any failure prints nothing and never changes the outcome.

// An unresponsive driver service must not hold up the OS update.
const driverListTimeout = 5 * time.Second

// installedDriverAddons returns the device's add-ons, or nil when it has none or
// cannot report them: an older agent answers Unimplemented and an unenrolled
// (plaintext) connection has no driver service at all.
func installedDriverAddons(ctx context.Context, conn *grpcclient.AgentConnection) *agentpbv2.ListDriversResponse {
	if conn == nil || conn.DriverService == nil {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, driverListTimeout)
	defer cancel()
	resp, err := conn.DriverService.ListDrivers(callCtx, &agentpbv2.ListDriversRequest{})
	if err != nil || len(resp.GetInstalled()) == 0 {
		return nil
	}
	return resp
}

// warnDriverAddonsBeforeUpdate names the installed add-ons and what this update
// may do to them. An empty targetVersion (a local artifact or --artifact-url
// resolves no manifest) limits it to the generic warning.
func warnDriverAddonsBeforeUpdate(ctx context.Context, conn *grpcclient.AgentConnection, deviceType, targetVersion string, pr int) {
	dl := installedDriverAddons(ctx, conn)
	if dl == nil {
		return
	}
	installed := dl.GetInstalled()

	names := make([]string, 0, len(installed))
	for _, d := range installed {
		names = append(names, d.GetName())
	}
	cliLogln("%s", tui.WarningMessage(fmt.Sprintf("%s installed: %s.",
		countAddons(len(names)), strings.Join(names, ", "))))
	// Stated as a rule, not as a fact about these add-ons: some may already be
	// stale, which is precisely the state this warning exists for.
	cliLogln("An add-on is built for one exact kernel and stops loading if this update changes it.")
	for _, d := range installed {
		if driverStale(d, dl.GetKernelVersion()) {
			cliLogln("  %s: already not usable on kernel %s", d.GetName(), dl.GetKernelVersion())
		}
	}

	if deviceType == "" || targetVersion == "" {
		return
	}
	exts, err := driverExtensionsFor(deviceType, targetVersion, pr)
	if err != nil {
		// The manifest is advisory here; the generic warning above already stands.
		return
	}
	// The manifest publishes one entry per (name, kernel), so collect every
	// kernel a name ships for rather than letting the last entry win.
	published := map[string][]string{}
	for _, e := range exts {
		published[e.Name] = append(published[e.Name], e.KernelVersion)
	}
	for _, d := range installed {
		kernels, ok := published[d.GetName()]
		switch {
		case !ok:
			cliLogln("  %s: no rebuild published for %s", d.GetName(), targetVersion)
		case slices.Contains(kernels, dl.GetKernelVersion()):
			cliLogln("  %s: unaffected — %s publishes it for this kernel", d.GetName(), targetVersion)
		default:
			cliLogln("  %s: rebuild published for kernel %s — reinstall after the update",
				d.GetName(), strings.Join(kernels, ", "))
		}
	}
}

// reportDriverAddonsAfterUpdate reports which add-ons survived the update. It
// dials its own connection: the outcome is already decided, so failing to
// connect here must cost nothing.
func reportDriverAddonsAfterUpdate(ctx context.Context, host string) {
	callCtx, cancel := context.WithTimeout(ctx, driverListTimeout)
	defer cancel()
	conn, err := connectWithAutoTLS(callCtx, hostPort(host, defaultAgentPort))
	if err != nil {
		return
	}
	defer conn.Close()

	dl := installedDriverAddons(callCtx, conn)
	if dl == nil {
		return
	}
	var stale []string
	for _, d := range dl.GetInstalled() {
		if driverStale(d, dl.GetKernelVersion()) {
			stale = append(stale, d.GetName())
		}
	}
	if len(stale) == 0 {
		cliLogln("%s still match the running kernel (%s).", countAddons(len(dl.GetInstalled())), dl.GetKernelVersion())
		return
	}
	cliLogln("%s", tui.WarningMessage(fmt.Sprintf("%s no longer built for the running kernel (%s): %s.",
		countAddons(len(stale)), dl.GetKernelVersion(), strings.Join(stale, ", "))))
	cliLogln("Reinstall with %s", tui.Command("wendy device drivers install <name>"))
}

func countAddons(n int) string {
	if n == 1 {
		return "1 driver add-on"
	}
	return fmt.Sprintf("%d driver add-ons", n)
}
