package commands

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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

// driverStageTimeout bounds one add-on fetch. Generous because the agent
// downloads the image, but bounded so a stalled registry cannot hold up an OTA.
const driverStageTimeout = 10 * time.Minute

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
func warnDriverAddonsBeforeUpdate(ctx context.Context, conn *grpcclient.AgentConnection, deviceType, targetVersion string, pr int, stageDrivers bool) {
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
	byName := map[string][]extensionEntry{}
	for _, e := range exts {
		byName[e.Name] = append(byName[e.Name], e)
	}
	for _, d := range installed {
		kernels, ok := published[d.GetName()]
		switch {
		case !ok:
			cliLogln("  %s: no rebuild published for %s", d.GetName(), targetVersion)
		case slices.Contains(kernels, dl.GetKernelVersion()):
			cliLogln("  %s: unaffected — %s publishes it for this kernel", d.GetName(), targetVersion)
		case stageDrivers:
			stageRebuild(ctx, conn, d.GetName(), byName[d.GetName()])
		default:
			cliLogln("  %s: rebuild published for kernel %s — reinstall after the update",
				d.GetName(), strings.Join(kernels, ", "))
		}
	}
}

// stageRebuild puts the published rebuild in place for the kernel the update is
// about to boot into, so the add-on loads on the first boot of the new slot.
// Advisory like the rest of this file: a failure prints, it never fails the OTA.
func stageRebuild(ctx context.Context, conn *grpcclient.AgentConnection, name string, entries []extensionEntry) {
	// Every entry here targets a kernel other than the running one, so any of them
	// is a candidate; more than one means the release ships several kernels and
	// there is no way to tell from here which the update will boot.
	if len(entries) != 1 {
		cliLogln("  %s: rebuild published for several kernels — reinstall after the update", name)
		return
	}
	e := entries[0]
	if e.Path == "" {
		cliLogln("  %s: rebuild has no artifact in the manifest — reinstall after the update", name)
		return
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		cliLogln("  %s: rebuild has an unreadable signature — reinstall after the update", name)
		return
	}
	spec := &agentpbv2.DriverSpec{
		Name:          e.Name,
		Version:       e.Version,
		KernelVersion: e.KernelVersion,
		Sha256:        e.SHA256,
		Signature:     sig,
		ArtifactUrl:   gcsBaseURL + "/" + e.Path,
		ModulesLoad:   e.ModulesLoad,
		StageOnly:     true,
	}
	if err := runDriverStage(ctx, conn, spec); err != nil {
		cliLogln("  %s: could not stage the rebuild (%v) — reinstall after the update", name, err)
		return
	}
	cliLogln("  %s: staged for kernel %s — it will load on the first boot of the new slot", name, e.KernelVersion)
}

// runDriverStage drives the install stream to completion for a staged add-on.
// The agent fetches the image itself, so no chunks follow the spec.
func runDriverStage(ctx context.Context, conn *grpcclient.AgentConnection, spec *agentpbv2.DriverSpec) error {
	callCtx, cancel := context.WithTimeout(ctx, driverStageTimeout)
	defer cancel()
	stream, err := conn.DriverService.InstallDriver(callCtx)
	if err != nil {
		return err
	}
	if err := stream.Send(&agentpbv2.InstallDriverRequest{
		RequestType: &agentpbv2.InstallDriverRequest_Spec{Spec: spec},
	}); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("stream ended without a result")
		}
		if err != nil {
			return err
		}
		if f := resp.GetFailed(); f != nil {
			return errors.New(f.GetErrorMessage())
		}
		if resp.GetCompleted() != nil {
			return nil
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
