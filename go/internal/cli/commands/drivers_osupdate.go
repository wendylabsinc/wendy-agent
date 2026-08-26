package commands

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// A driver add-on is built for one exact kernel, so an OS update that bumps it
// stops the add-on merging — visible only in the device journal. The pre-flight
// stages each rebuild ahead of the reboot and reports what it could not stage;
// the caller decides whether to go ahead. The post-update report is advisory.

var driverExtensionsForFn = driverExtensionsFor

// driverListTimeout bounds reading the installed set. Short: the answer gates an
// OS update, so a slow reply has to become a decision rather than a hang.
const driverListTimeout = 5 * time.Second

// driverStageTimeout bounds one add-on fetch. Generous because the agent
// downloads the image, but bounded so a stalled registry cannot hold up an OTA.
const driverStageTimeout = 10 * time.Minute

// driverPreflight is what the pre-update check concluded. Anything recorded here
// means the device is about to update without a driver it has today.
type driverPreflight struct {
	// unreadable is set when the installed set could not be read at all, which is
	// the worst case: there may be no add-ons, or there may be a critical one.
	unreadable string
	unstaged   []unstagedAddon
}

type unstagedAddon struct{ name, reason string }

func (p driverPreflight) blocking() bool {
	return p.unreadable != "" || len(p.unstaged) > 0
}

// installedDriverAddons reads the device's add-ons. A nil response and a nil error
// mean the device cannot have any — an unenrolled connection exposes no driver
// service, an older agent answers Unimplemented — and blocking on those would
// block the update that fixes them. Every other failure is returned: "could not
// ask" and "has none" must not look alike to the caller.
func installedDriverAddons(ctx context.Context, conn *grpcclient.AgentConnection) (*agentpbv2.ListDriversResponse, error) {
	if conn == nil || conn.DriverService == nil {
		return nil, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, driverListTimeout)
	defer cancel()
	resp, err := conn.DriverService.ListDrivers(callCtx, &agentpbv2.ListDriversRequest{})
	if status.Code(err) == codes.Unimplemented {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// warnDriverAddonsBeforeUpdate names the installed add-ons and what this update
// may do to them. An empty targetVersion (a local artifact or --artifact-url
// resolves no manifest) limits it to the generic warning.
func warnDriverAddonsBeforeUpdate(ctx context.Context, conn *grpcclient.AgentConnection, deviceType, targetVersion, driversDir string, pr int, stageDrivers bool) driverPreflight {
	var pf driverPreflight
	dl, err := installedDriverAddons(ctx, conn)
	if err != nil {
		pf.unreadable = err.Error()
		cliLogln("%s", tui.WarningMessage("Could not read the installed driver add-ons: "+err.Error()))
		return pf
	}
	installed := dl.GetInstalled()

	if len(installed) > 0 {
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
	}

	// Files the operator supplied win over the registry: they are the answer for a
	// network with no route to it. Reached even with nothing installed, so a fresh
	// air-gapped device can be pre-loaded.
	if driversDir != "" {
		stageFromDir(ctx, conn, installed, driversDir, &pf)
		return pf
	}
	if len(installed) == 0 {
		return pf
	}

	// Without a resolved manifest nothing can be staged: a local artifact or
	// --artifact-url names no version to look add-ons up against.
	if deviceType == "" || targetVersion == "" {
		if stageDrivers {
			for _, d := range installed {
				pf.unstaged = append(pf.unstaged, unstagedAddon{d.GetName(),
					"this update resolves no manifest, so no rebuild can be staged"})
			}
		}
		return pf
	}
	exts, err := driverExtensionsForFn(deviceType, targetVersion, pr)
	if err != nil {
		cliLogln("  could not read the add-ons published for %s: %v", targetVersion, err)
		if stageDrivers {
			for _, d := range installed {
				pf.unstaged = append(pf.unstaged, unstagedAddon{d.GetName(),
					fmt.Sprintf("could not read the published add-ons: %v", err)})
			}
		}
		return pf
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
		name := d.GetName()
		kernels, ok := published[name]
		switch {
		case !ok:
			cliLogln("  %s: no rebuild published for %s", name, targetVersion)
			if stageDrivers {
				pf.unstaged = append(pf.unstaged, unstagedAddon{name, "no rebuild published for " + targetVersion})
			}
		case slices.Contains(kernels, dl.GetKernelVersion()):
			cliLogln("  %s: unaffected — %s publishes it for this kernel", name, targetVersion)
		case stageDrivers:
			if reason := stageRebuild(ctx, conn, name, byName[name]); reason != "" {
				pf.unstaged = append(pf.unstaged, unstagedAddon{name, reason})
			}
		default:
			// --no-drivers: the operator has already accepted this, so it is
			// reported but never blocks.
			cliLogln("  %s: rebuild published for kernel %s — reinstall after the update",
				name, strings.Join(kernels, ", "))
		}
	}
	return pf
}

// stageRebuild puts the published rebuild in place for the kernel the update is
// about to boot into, so the add-on loads on the first boot of the new slot.
// Returns "" on success, or why it could not be staged.
func stageRebuild(ctx context.Context, conn *grpcclient.AgentConnection, name string, entries []extensionEntry) string {
	// Every entry here targets a kernel other than the running one, so any of them
	// is a candidate; more than one means the release ships several kernels and
	// there is no way to tell from here which the update will boot.
	if len(entries) != 1 {
		reason := "published for several kernels, so the one this update boots is ambiguous"
		cliLogln("  %s: %s", name, reason)
		return reason
	}
	e := entries[0]
	if e.Path == "" {
		reason := "the published rebuild has no artifact in the manifest"
		cliLogln("  %s: %s", name, reason)
		return reason
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		reason := "the published rebuild has an unreadable signature"
		cliLogln("  %s: %s", name, reason)
		return reason
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
		reason := fmt.Sprintf("staging failed: %v", err)
		cliLogln("  %s: %s", name, reason)
		return reason
	}
	cliLogln("  %s: staged for kernel %s — it will load on the first boot of the new slot", name, e.KernelVersion)
	return ""
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
	return awaitDriverApply(stream)
}

// awaitDriverApply drains an apply stream to its verdict.
func awaitDriverApply(stream agentpbv2.WendyDriverService_InstallDriverClient) error {
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

	// Unlike the pre-flight, silence is right here: the update is already applied
	// and the agent may still be starting after the reboot.
	dl, err := installedDriverAddons(callCtx, conn)
	if err != nil || len(dl.GetInstalled()) == 0 {
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

// confirmDriverPreflight turns the pre-flight verdict into a decision. The tool
// cannot tell a critical NIC driver from a webcam, so it asks rather than guessing.
func confirmDriverPreflight(pf driverPreflight) error {
	cliLogln("")
	if pf.unreadable != "" {
		cliLogln("%s", tui.WarningMessage("The device's driver add-ons could not be read, so this update may remove one."))
	} else {
		cliLogln("%s", tui.WarningMessage(fmt.Sprintf("%s will not be in place after this update:", countAddons(len(pf.unstaged)))))
		for _, u := range pf.unstaged {
			cliLogln("    %s — %s", u.name, u.reason)
		}
	}

	if !isInteractiveTerminal() {
		return fmt.Errorf("aborted: the device would update without %s (re-run with --no-drivers to update anyway)",
			driverPreflightSubject(pf))
	}
	confirmed, err := tui.Confirm("Continue with the update anyway?")
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return ErrUserCancelled
		}
		return err
	}
	if !confirmed {
		cliNotice("Cancelled.")
		return ErrUserCancelled
	}
	return nil
}

func driverPreflightSubject(pf driverPreflight) string {
	if pf.unreadable != "" {
		return "a driver add-on it may have (the installed set could not be read)"
	}
	names := make([]string, 0, len(pf.unstaged))
	for _, u := range pf.unstaged {
		names = append(names, u.name)
	}
	return strings.Join(names, ", ")
}

// checkDriversDir rejects a --drivers-dir the update cannot honour, before
// anything transfers. A directory with no images is a wrong path, not a verdict
// about the device, so it fails outright rather than becoming a prompt.
func checkDriversDir(conn *grpcclient.AgentConnection, dir string) error {
	if conn == nil || conn.DriverService == nil {
		return fmt.Errorf("--drivers-dir needs the device's driver service; enrol the device first")
	}
	files, err := driverRawsIn(dir)
	if err != nil {
		return fmt.Errorf("--drivers-dir %s: %w", dir, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("--drivers-dir %s contains no .raw driver add-ons", dir)
	}
	return nil
}

func driverRawsIn(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "*.raw"))
}

// stageFromDir stages every add-on image in dir, so an air-gapped update can put
// the next kernel's drivers in place. Each image names its own kernel, so the
// caller does not have to know the target. Staging one the device has not got is
// how a fresh device is pre-loaded; an installed one with no image here is a
// driver the device loses on reboot.
func stageFromDir(ctx context.Context, conn *grpcclient.AgentConnection, installed []*agentpbv2.InstalledDriver, dir string, pf *driverPreflight) {
	files, err := driverRawsIn(dir)
	if err != nil {
		pf.unreadable = err.Error()
		cliLogln("%s", tui.WarningMessage("Could not read "+dir+": "+err.Error()))
		return
	}
	cliLogln("Staging driver add-ons from %s", tui.Path(dir))
	present := make(map[string]bool, len(files))
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".raw")
		present[name] = true
		if err := stageLocalRebuild(ctx, conn, name, path); err != nil {
			reason := fmt.Sprintf("staging failed: %v", err)
			cliLogln("  %s: %s", name, reason)
			pf.unstaged = append(pf.unstaged, unstagedAddon{name, reason})
			continue
		}
		cliLogln("  %s: staged from %s", name, filepath.Base(path))
	}
	for _, d := range installed {
		if name := d.GetName(); !present[name] {
			reason := fmt.Sprintf("no %s.raw in %s", name, dir)
			cliLogln("  %s: %s", name, reason)
			pf.unstaged = append(pf.unstaged, unstagedAddon{name, reason})
		}
	}
}

// stageLocalRebuild streams one local .raw to the agent for staging. The digest
// is computed here so the agent can verify what it received, exactly as
// `drivers install --file` does.
func stageLocalRebuild(ctx context.Context, conn *grpcclient.AgentConnection, name, path string) error {
	sum, err := sha256File(path)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, driverStageTimeout)
	defer cancel()
	stream, err := conn.DriverService.InstallDriver(callCtx)
	if err != nil {
		return err
	}
	// No KernelVersion: the image declares the kernel it was built for, and the
	// agent files it under that.
	if err := stream.Send(&agentpbv2.InstallDriverRequest{
		RequestType: &agentpbv2.InstallDriverRequest_Spec{Spec: &agentpbv2.DriverSpec{
			Name:      name,
			Sha256:    sum,
			StageOnly: true,
		}},
	}); err != nil {
		return err
	}
	if err := streamDriverChunks(stream, path); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	return awaitDriverApply(stream)
}
