package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// trainTestTargets is the fixture fleet shared with the plan tests: three
// devices whose lowest asset id (211) therefore coordinates.
func trainTestTargets() []fleetTarget {
	return []fleetTarget{
		{Name: "spark-3011", ID: "spark-3011.local", Address: "192.0.2.34:50051", PeerHost: "192.0.2.34", AssetID: 334, connect: unusedConnect},
		{Name: "spark-48fd", ID: "spark-48fd.local", Address: "192.0.2.11:50051", PeerHost: "192.0.2.11", AssetID: 211, connect: unusedConnect},
		{Name: "spark-edeb", ID: "spark-edeb.local", Address: "192.0.2.83:50051", PeerHost: "192.0.2.83", AssetID: 283, connect: unusedConnect},
	}
}

func unusedConnect(context.Context) (*grpcclient.AgentConnection, error) {
	return nil, errors.New("a test connected to a device; the deploy seam should have been stubbed")
}

// stubTrainTargets points group resolution at the fixture fleet for one test.
func stubTrainTargets(t *testing.T, targets []fleetTarget) {
	t.Helper()
	original := resolveFleetTargetsFn
	resolveFleetTargetsFn = func(context.Context, string, bool, string, string, time.Duration) ([]fleetTarget, error) {
		return targets, nil
	}
	t.Cleanup(func() { resolveFleetTargetsFn = original })
}

// deployRecord is what one device would have received.
type deployRecord struct {
	Device string
	Cwd    string
	Env    []string
}

// recordDeploys replaces the per-device deploy with a recorder, so a test can
// assert on what each device is told without an agent anywhere.
func recordDeploys(t *testing.T) *[]deployRecord {
	t.Helper()
	var mu sync.Mutex
	records := []deployRecord{}
	original := deployToTargetFn
	deployToTargetFn = func(_ context.Context, target fleetTarget, cwd string, _ *appconfig.AppConfig, opts runOptions) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, deployRecord{Device: target.Name, Cwd: cwd, Env: opts.env})
		return nil
	}
	t.Cleanup(func() { deployToTargetFn = original })
	return &records
}

func envValue(entries []string, key string) string {
	for _, kv := range entries {
		if name, value, ok := strings.Cut(kv, "="); ok && name == key {
			return value
		}
	}
	return ""
}

func TestTrainUpDryRunDeploysNothingAndWritesNothing(t *testing.T) {
	stubTrainTargets(t, trainTestTargets())
	records := recordDeploys(t)

	opts := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "dryrun-clean", template: "es-fleet", lan: true},
		transport:          "lan",
		dryRun:             true,
	}
	out, err := captureCommandStdout(t, func() error { return runFleetTrainUp(context.Background(), opts) })
	if err != nil {
		t.Fatal(err)
	}

	if len(*records) != 0 {
		t.Fatalf("a dry run deployed to %d device(s)", len(*records))
	}
	// A dry run is an audit: it must not leave a token behind that a later
	// real deploy would silently disagree with.
	statePath, err := trainStatePath("dryrun-clean", "sh.wendy.training.es-fleet")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote state at %s", statePath)
	}
	for _, want := range []string{
		"nothing was deployed",
		"staging:", // no --stage-dir, so staging is skipped and says so
		"device spark-48fd", "role coordinator",
		"WT_FLEET_TOKEN=<masked",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run output missing %q\n%s", want, out)
		}
	}
}

func TestTrainUpDryRunWithStageDirStagesButStillDeploysNothing(t *testing.T) {
	stubTrainTargets(t, trainTestTargets())
	records := recordDeploys(t)
	stageDir := filepath.Join(t.TempDir(), "staged")

	opts := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "dryrun-staged", template: "es-fleet", lan: true},
		transport:          "lan",
		dryRun:             true,
		stageDir:           stageDir,
	}
	out, err := captureCommandStdout(t, func() error { return runFleetTrainUp(context.Background(), opts) })
	if err != nil {
		t.Fatal(err)
	}
	if len(*records) != 0 {
		t.Fatalf("a dry run deployed to %d device(s)", len(*records))
	}
	// Asking for a staging directory is an explicit, inspectable side effect.
	if _, err := os.Stat(filepath.Join(stageDir, stageManifestName)); err != nil {
		t.Fatalf("expected a staged context at %s: %v", stageDir, err)
	}
	if !strings.Contains(out, "staged:") {
		t.Fatalf("output should report the staged directory\n%s", out)
	}
}

func TestTrainUpInjectsPerDeviceEnv(t *testing.T) {
	stubTrainTargets(t, trainTestTargets())
	records := recordDeploys(t)

	opts := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "deploy-env", template: "es-fleet", lan: true},
		transport:          "lan",
		env:                []string{"ES_POP=24"},
	}
	if _, err := captureCommandStdout(t, func() error { return runFleetTrainUp(context.Background(), opts) }); err != nil {
		t.Fatal(err)
	}

	if len(*records) != 3 {
		t.Fatalf("deployed to %d devices, want 3", len(*records))
	}
	// Plan order is by ascending asset id, so the coordinator goes first.
	if (*records)[0].Device != "spark-48fd" {
		t.Fatalf("first deploy was %s, want the coordinator spark-48fd", (*records)[0].Device)
	}

	seenSelf := map[string]bool{}
	token := ""
	for _, rec := range *records {
		self := envValue(rec.Env, "MESH_SELF")
		if self == "" {
			t.Fatalf("%s got no MESH_SELF", rec.Device)
		}
		if seenSelf[self] {
			t.Fatalf("two devices share MESH_SELF=%s; their identities are aliased", self)
		}
		seenSelf[self] = true

		if got := envValue(rec.Env, "ES_POP"); got != "24" {
			t.Fatalf("%s: ES_POP = %q, want 24", rec.Device, got)
		}
		if got := envValue(rec.Env, "WT_COORDINATOR"); got != "192.0.2.11:8080" {
			t.Fatalf("%s: WT_COORDINATOR = %q", rec.Device, got)
		}
		if strings.Contains(strings.Join(rec.Env, " "), ".local") {
			t.Fatalf("%s: a multicast name reached the device: %v", rec.Device, rec.Env)
		}
		current := envValue(rec.Env, trainFleetTokenEnvKey)
		if current == "" {
			t.Fatalf("%s got no fleet token", rec.Device)
		}
		if token != "" && current != token {
			t.Fatal("devices received different fleet tokens; they could not authenticate to each other")
		}
		token = current
	}

	// Every device must own its slice: mutating one must not disturb another.
	(*records)[0].Env[0] = "MUTATED=1"
	if strings.HasPrefix((*records)[1].Env[0], "MUTATED") {
		t.Fatal("device environments share a backing array")
	}

	// A real deploy persists the token so status and stop can authenticate.
	if _, ok := loadTrainState("deploy-env", "sh.wendy.training.es-fleet"); !ok {
		t.Fatal("deploy did not save fleet state")
	}
}

func TestTrainUpRejectsLANTransportWithoutLANResolution(t *testing.T) {
	opts := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "sparks", template: "es-fleet"},
		transport:          "lan",
	}
	err := runFleetTrainUp(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "--lan") {
		t.Fatalf("expected an error pointing at --lan, got %v", err)
	}
}

func TestTrainUpRejectsUnknownTransportAndMissingTemplate(t *testing.T) {
	missing := &trainUpOptions{trainCommonOptions: trainCommonOptions{group: "sparks"}}
	if err := runFleetTrainUp(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "--template") {
		t.Fatalf("expected a missing-template error, got %v", err)
	}
	unknown := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "sparks", template: "es-fleet"},
		transport:          "carrier-pigeon",
	}
	if err := runFleetTrainUp(context.Background(), unknown); err == nil || !strings.Contains(err.Error(), "unknown transport") {
		t.Fatalf("expected an unknown-transport error, got %v", err)
	}
}

func TestTrainUpRejectsBlockedEnvBeforeContactingDevices(t *testing.T) {
	stubTrainTargets(t, trainTestTargets())
	records := recordDeploys(t)

	opts := &trainUpOptions{
		trainCommonOptions: trainCommonOptions{group: "blocked-env", template: "es-fleet", lan: true},
		transport:          "lan",
		env:                []string{"WENDY_SNEAKY=1"},
	}
	err := runFleetTrainUp(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a reserved environment prefix")
	}
	// The agent would reject this at container create; failing here means the
	// operator learns before a build runs.
	if len(*records) != 0 {
		t.Fatal("a rejected environment key still reached a deploy")
	}
}

func TestMatchTrainContainers(t *testing.T) {
	const appID = "sh.wendy.training.byo"
	names := []string{
		"sh.wendy.training.byo",
		"sh.wendy.training.byo_node",
		"sh.wendy.training.byoish",
		"sh.wendy.training.es-fleet",
		"go2-artifacts-export",
		"yolo26x-coco-hour-challenge",
	}
	got := matchTrainContainers(names, appID)
	want := []string{"sh.wendy.training.byo", "sh.wendy.training.byo_node"}
	if len(got) != len(want) {
		t.Fatalf("matched %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matched %v, want %v", got, want)
		}
	}
	// An empty application id must never match everything on the device.
	if matched := matchTrainContainers(names, ""); len(matched) != 0 {
		t.Fatalf("an empty app id matched %v", matched)
	}
}

func TestTrainDeviceBaseURL(t *testing.T) {
	lan := fleetTarget{Name: "spark-48fd", Address: "192.0.2.11:50051", PeerHost: "192.0.2.11", AssetID: 211}
	got, err := trainDeviceBaseURL(lan, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.0.2.11:8080" {
		t.Fatalf("local-network URL = %q", got)
	}

	cloud := fleetTarget{Name: "spark-48fd", AssetID: 211}
	got, err = trainDeviceBaseURL(cloud, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://device-211.cloud.wendy.dev:8080" {
		t.Fatalf("cloud URL = %q", got)
	}

	if _, err := trainDeviceBaseURL(fleetTarget{Name: "orphan"}, 8080); err == nil {
		t.Fatal("a device with neither an address nor an asset id must not be addressable")
	}
}

func TestPollTrainStatusAuthenticatesAndSanitizes(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer secret-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Control characters and newlines must not reach a terminal verbatim.
		w.Write([]byte("{\"generation\":\t40,\n\"done\":\x07true}"))
	}))
	defer server.Close()

	// Status is polled from the operator's machine, so it dials the peer host.
	target := fleetTarget{Name: "fake", PeerHost: "127.0.0.1"}
	port := 0
	if _, portStr, ok := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":"); ok {
		for _, r := range portStr {
			port = port*10 + int(r-'0')
		}
	}

	summary := pollTrainStatus(context.Background(), server.Client(), target, port, "secret-token")
	if !strings.Contains(summary, "\"generation\": 40") && !strings.Contains(summary, "\"generation\":") {
		t.Fatalf("status not reported: %q", summary)
	}
	if strings.ContainsAny(summary, "\n\t\x07") {
		t.Fatalf("unsanitized control characters in %q", summary)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("bearer token not sent, got %q", gotAuth)
	}

	// A wrong token must be reported as such, not as a generic failure.
	unauthorized := pollTrainStatus(context.Background(), server.Client(), target, port, "wrong")
	if !strings.Contains(unauthorized, "unauthorized") {
		t.Fatalf("expected an unauthorized report, got %q", unauthorized)
	}
}

func TestPollTrainStatusUnreachableDeviceIsReportedNotFatal(t *testing.T) {
	// Port 1 on the loopback interface refuses immediately.
	target := fleetTarget{Name: "down", Address: "127.0.0.1:50051", PeerHost: "127.0.0.1"}
	got := pollTrainStatus(context.Background(), http.DefaultClient, target, 1, "token")
	if !strings.Contains(got, "unreachable") {
		t.Fatalf("got %q, want an unreachable report", got)
	}
}

func TestMergeTrainConfigFlagsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "train.json")
	body := map[string]any{
		"group":     "from-file",
		"template":  "sweep",
		"transport": "lan",
		"lan":       true,
		"env":       map[string]string{"A": "file", "B": "file"},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var o trainUpOptions
	cmd := newFleetTrainUpCmd()
	// Simulate the operator typing --group and one --env, leaving the rest to
	// the file.
	if err := cmd.Flags().Set("group", "from-flag"); err != nil {
		t.Fatal(err)
	}
	o.group = "from-flag"
	o.configPath = path
	o.env = []string{"A=flag"}

	if err := mergeTrainConfig(cmd, &o); err != nil {
		t.Fatal(err)
	}
	if o.group != "from-flag" {
		t.Fatalf("group = %q; an explicit flag must win over the file", o.group)
	}
	if o.template != "sweep" || o.transport != "lan" || !o.lan {
		t.Fatalf("file values not applied: %+v", o)
	}
	// File environment sits underneath, so the flag's A wins while B survives.
	merged, err := validateTrainEnv(o.env)
	if err != nil {
		t.Fatal(err)
	}
	if merged["A"] != "flag" {
		t.Fatalf("A = %q, want the flag value", merged["A"])
	}
	if merged["B"] != "file" {
		t.Fatalf("B = %q, want the file value", merged["B"])
	}
}

func TestResolveTrainTargetNeedsATemplateOrSavedState(t *testing.T) {
	_, _, err := resolveTrainTarget(trainCommonOptions{group: "never-deployed"})
	if err == nil || !strings.Contains(err.Error(), "--template") {
		t.Fatalf("expected guidance to pass --template, got %v", err)
	}

	// With a template, the application id resolves even before any deploy.
	appID, state, err := resolveTrainTarget(trainCommonOptions{group: "g", template: "byo"})
	if err != nil {
		t.Fatal(err)
	}
	if appID != "sh.wendy.training.byo" {
		t.Fatalf("appID = %q", appID)
	}
	if state != nil {
		t.Fatal("expected no saved state for a fleet that was never deployed")
	}
}

func TestResolveTrainPeerHostsReplacesMulticastNames(t *testing.T) {
	// Containers cannot resolve multicast names, so a plan must never carry
	// one. This is the failure that cost a hardware attempt before the
	// launcher was replaced.
	original := osLookupHostFn
	osLookupHostFn = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "spark-48fd.local":
			return []string{"192.168.0.46"}, nil
		case "spark-edeb.local":
			return []string{"192.168.0.132"}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { osLookupHostFn = original })

	targets := []fleetTarget{
		{Name: "spark-48fd", PeerHost: "spark-48fd.local", AssetID: 211},
		{Name: "spark-edeb", PeerHost: "spark-edeb.local", AssetID: 283},
	}
	warnings, err := resolveTrainPeerHosts(context.Background(), targets, transportLAN)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("private addresses must not warn: %v", warnings)
	}
	if targets[0].PeerHost != "192.168.0.46" || targets[1].PeerHost != "192.168.0.132" {
		t.Fatalf("peer hosts not resolved: %q %q", targets[0].PeerHost, targets[1].PeerHost)
	}
}

func TestResolveTrainPeerHostsRefusesUnresolvableAndWarnsOnPublic(t *testing.T) {
	original := osLookupHostFn
	osLookupHostFn = func(_ context.Context, host string) ([]string, error) {
		if host == "public.example" {
			return []string{"93.184.216.34"}, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { osLookupHostFn = original })

	// A device nobody can resolve must fail here, not one layer down inside a
	// container that reports only "Name or service not known".
	unresolvable := []fleetTarget{{Name: "ghost", PeerHost: "ghost.local"}}
	if _, err := resolveTrainPeerHosts(context.Background(), unresolvable, transportLAN); err == nil {
		t.Fatal("expected an error for an unresolvable device")
	}

	public := []fleetTarget{{Name: "far", PeerHost: "public.example"}}
	warnings, err := resolveTrainPeerHosts(context.Background(), public, transportLAN)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "outside private address space") {
		t.Fatalf("expected one public-address warning, got %v", warnings)
	}

	// The mesh transport addresses peers by asset id, so no resolution happens.
	meshWarnings, err := resolveTrainPeerHosts(context.Background(), unresolvable, transportMesh)
	if err != nil || meshWarnings != nil {
		t.Fatalf("mesh transport must not resolve: %v %v", meshWarnings, err)
	}
}

func TestResolveTrainPeerHostsLeavesAddressesAlone(t *testing.T) {
	targets := []fleetTarget{{Name: "already", PeerHost: "192.0.2.11"}}
	if _, err := resolveTrainPeerHosts(context.Background(), targets, transportLAN); err != nil {
		t.Fatal(err)
	}
	if targets[0].PeerHost != "192.0.2.11" {
		t.Fatalf("an address was rewritten to %q", targets[0].PeerHost)
	}
}
