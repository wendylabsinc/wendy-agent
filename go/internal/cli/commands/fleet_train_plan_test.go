package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The fixtures mirror the three Sparks the training work was verified on, so a
// failure here reads the same way as the hardware checklist. spark-48fd holds
// the lowest asset id and therefore coordinates.
func trainPlanTestTargets() []fleetTarget {
	return []fleetTarget{
		{Name: "spark-3011", ID: "spark-3011.local", Address: "192.0.2.34", AssetID: 334},
		{Name: "spark-48fd", ID: "spark-48fd.local", Address: "192.0.2.11", AssetID: 211},
		{Name: "spark-edeb", ID: "spark-edeb.local", Address: "192.0.2.83", AssetID: 283},
	}
}

func trainPlanTestInput() trainPlanInput {
	return trainPlanInput{
		Template:  "es-fleet",
		AppID:     "sh.wendy.training.es",
		Group:     "spark-*",
		Transport: transportMesh,
		BaseEnv:   map[string]string{"WT_RUN_ID": "demo-1", "ES_POP": "64"},
	}
}

// trainPlanByName indexes a computed plan by device name.
func trainPlanByName(plan *trainPlan) map[string]trainDevicePlan {
	out := make(map[string]trainDevicePlan, len(plan.Devices))
	for _, device := range plan.Devices {
		out[device.Target.Name] = device
	}
	return out
}

func mustComputeTrainPlan(t *testing.T, targets []fleetTarget, in trainPlanInput) *trainPlan {
	t.Helper()
	plan, err := computeTrainPlan(targets, in)
	if err != nil {
		t.Fatalf("computeTrainPlan: %v", err)
	}
	return plan
}

func TestTrainPlanExactlyOneCoordinator(t *testing.T) {
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), trainPlanTestInput())

	var coordinators []string
	var workerIDs []int32
	for _, device := range plan.Devices {
		if isTrainCoordinatorRole(device.Role) {
			coordinators = append(coordinators, device.Target.Name)
		} else {
			workerIDs = append(workerIDs, device.Target.AssetID)
		}
	}
	if len(coordinators) != 1 || coordinators[0] != "spark-48fd" {
		t.Fatalf("coordinators = %v, want exactly [spark-48fd]", coordinators)
	}
	// Ranking is by ascending asset id, so the coordinator is also rank 0.
	if plan.Devices[0].Target.Name != "spark-48fd" || plan.Devices[0].Rank != 0 {
		t.Fatalf("rank 0 device = %+v, want spark-48fd at rank 0", plan.Devices[0])
	}
	if got, want := workerIDs, []int32{283, 334}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("worker asset ids = %v, want %v", got, want)
	}
	for i, device := range plan.Devices {
		if device.Rank != i {
			t.Fatalf("device %s has rank %d at position %d", device.Target.Name, device.Rank, i)
		}
		if device.Env["WT_ROLE"] != device.Role {
			t.Fatalf("device %s: WT_ROLE=%q but role=%q", device.Target.Name, device.Env["WT_ROLE"], device.Role)
		}
	}
}

func TestTrainPlanRolePins(t *testing.T) {
	t.Run("pin moves the coordinator", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{"spark-3011": trainRoleCoordinator}
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
		byName := trainPlanByName(plan)
		if byName["spark-3011"].Role != trainRoleCoordinator {
			t.Fatalf("spark-3011 role = %q, want coordinator", byName["spark-3011"].Role)
		}
		// Rank 0 must not also claim the coordinator role once a pin has it.
		if byName["spark-48fd"].Role != trainRoleWorker {
			t.Fatalf("spark-48fd role = %q, want worker", byName["spark-48fd"].Role)
		}
		if got, want := byName["spark-edeb"].Env["WT_COORDINATOR"], "device-334.cloud.wendy.dev:8080"; got != want {
			t.Fatalf("WT_COORDINATOR = %q, want %q", got, want)
		}
	})

	t.Run("a learner pin coordinates", func(t *testing.T) {
		// The Python launcher looked the coordinator up by the literal role
		// name and raised StopIteration when the only coordinating device was
		// pinned as a learner. Both names must resolve.
		in := trainPlanTestInput()
		in.RolePins = map[string]string{"spark-edeb": trainRoleLearner}
		plan, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err != nil {
			t.Fatalf("learner pin: %v", err)
		}
		byName := trainPlanByName(plan)
		if byName["spark-edeb"].Role != trainRoleLearner {
			t.Fatalf("spark-edeb role = %q, want learner", byName["spark-edeb"].Role)
		}
		if byName["spark-48fd"].Role != trainRoleWorker {
			t.Fatalf("spark-48fd role = %q, want worker", byName["spark-48fd"].Role)
		}
		for _, device := range plan.Devices {
			if got, want := device.Env["WT_COORDINATOR"], "device-283.cloud.wendy.dev:8080"; got != want {
				t.Fatalf("%s WT_COORDINATOR = %q, want %q", device.Target.Name, got, want)
			}
		}
	})

	t.Run("actor pins keep the automatic coordinator", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{"spark-3011": trainRoleActor}
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
		byName := trainPlanByName(plan)
		if byName["spark-3011"].Role != trainRoleActor {
			t.Fatalf("spark-3011 role = %q, want actor", byName["spark-3011"].Role)
		}
		if byName["spark-48fd"].Role != trainRoleCoordinator {
			t.Fatalf("spark-48fd role = %q, want coordinator", byName["spark-48fd"].Role)
		}
	})

	t.Run("two coordinating pins conflict", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{
			"spark-3011": trainRoleCoordinator,
			"spark-edeb": trainRoleLearner,
		}
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil {
			t.Fatal("expected an error for two coordinating pins")
		}
		if !strings.Contains(err.Error(), "--role") || !strings.Contains(err.Error(), "exactly one coordinator") {
			t.Fatalf("error %q should tell the user to pin --role until exactly one coordinator remains", err)
		}
	})

	t.Run("no coordinating role at all", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{
			"spark-3011": trainRoleWorker,
			"spark-48fd": trainRoleWorker,
			"spark-edeb": trainRoleWorker,
		}
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil || !strings.Contains(err.Error(), "exactly one coordinator") {
			t.Fatalf("error = %v, want an exactly-one-coordinator error", err)
		}
	})

	t.Run("pin naming a device outside the group", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{"spark-9999": trainRoleCoordinator}
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil || !strings.Contains(err.Error(), "spark-9999") {
			t.Fatalf("error = %v, want it to name the unmatched device", err)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		in := trainPlanTestInput()
		in.RolePins = map[string]string{"spark-3011": "captain"}
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil || !strings.Contains(err.Error(), "captain") {
			t.Fatalf("error = %v, want it to name the unknown role", err)
		}
	})
}

func TestTrainPlanMeshPeersCompleteIncludingSelf(t *testing.T) {
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), trainPlanTestInput())

	// Plan order is ascending asset id, and every device sees the whole fleet
	// (templates skip themselves by matching MESH_SELF).
	const want = "211,283,334"
	for _, device := range plan.Devices {
		if got := device.Env["MESH_PEERS"]; got != want {
			t.Fatalf("%s MESH_PEERS = %q, want %q", device.Target.Name, got, want)
		}
		if got := device.Env["MESH_SELF"]; got != trainAssetIDString(device.Target.AssetID) {
			t.Fatalf("%s MESH_SELF = %q, want %d", device.Target.Name, got, device.Target.AssetID)
		}
		if !strings.Contains(device.Env["MESH_PEERS"], device.Env["MESH_SELF"]) {
			t.Fatalf("%s MESH_PEERS %q must include self %q", device.Target.Name, device.Env["MESH_PEERS"], device.Env["MESH_SELF"])
		}
	}
}

// trainAssetIDString renders an asset id the way the plan does.
func trainAssetIDString(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}

func TestTrainPlanSetEnvForwarded(t *testing.T) {
	in := trainPlanTestInput()
	in.BaseEnv["WT_FLEET_TOKEN"] = "0123456789abcdef0123456789abcdef"
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)

	for _, device := range plan.Devices {
		if device.Env["WT_RUN_ID"] != "demo-1" {
			t.Fatalf("%s WT_RUN_ID = %q, want demo-1", device.Target.Name, device.Env["WT_RUN_ID"])
		}
		if device.Env["ES_POP"] != "64" {
			t.Fatalf("%s ES_POP = %q, want 64", device.Target.Name, device.Env["ES_POP"])
		}
		if device.Env["WT_FLEET_TOKEN"] != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("%s did not receive the fleet token", device.Target.Name)
		}
		if device.Env["WT_NODE_COUNT"] != "3" {
			t.Fatalf("%s WT_NODE_COUNT = %q, want 3", device.Target.Name, device.Env["WT_NODE_COUNT"])
		}
	}
	// The caller's map must survive untouched; the plan copies it per device.
	if _, ok := in.BaseEnv["MESH_PEERS"]; ok {
		t.Fatal("computeTrainPlan wrote back into the caller's BaseEnv")
	}

	t.Run("MESH_PORT drives the peer and coordinator ports", func(t *testing.T) {
		ported := trainPlanTestInput()
		ported.Transport = transportLAN
		ported.BaseEnv["MESH_PORT"] = "9100"
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), ported)
		if plan.MeshPort != 9100 {
			t.Fatalf("MeshPort = %d, want 9100", plan.MeshPort)
		}
		if got, want := plan.Devices[0].Env["WT_COORDINATOR"], "192.0.2.11:9100"; got != want {
			t.Fatalf("WT_COORDINATOR = %q, want %q", got, want)
		}
	})

	t.Run("an unusable MESH_PORT warns instead of silently defaulting", func(t *testing.T) {
		broken := trainPlanTestInput()
		broken.BaseEnv["MESH_PORT"] = "eighty-eighty"
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), broken)
		if plan.MeshPort != defaultTrainMeshPort {
			t.Fatalf("MeshPort = %d, want the default", plan.MeshPort)
		}
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "MESH_PORT") {
			t.Fatalf("warnings = %v, want a MESH_PORT warning", plan.Warnings)
		}
	})
}

func TestTrainPlanSweep(t *testing.T) {
	params := []map[string]any{{"seed": 1}, {"seed": 2}, {"seed": 3}}

	t.Run("sweep template gets index and params", func(t *testing.T) {
		in := trainPlanTestInput()
		in.IsSweep = true
		in.SweepParams = params
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
		seen := map[string]bool{}
		for _, device := range plan.Devices {
			index := device.Env["WT_SWEEP_INDEX"]
			if index != strconv.Itoa(device.Rank) {
				t.Fatalf("%s WT_SWEEP_INDEX = %q, want %d", device.Target.Name, index, device.Rank)
			}
			seen[index] = true
			var decoded []map[string]any
			if err := json.Unmarshal([]byte(device.Env["WT_SWEEP_PARAMS"]), &decoded); err != nil {
				t.Fatalf("%s WT_SWEEP_PARAMS is not JSON: %v", device.Target.Name, err)
			}
			if len(decoded) != 3 {
				t.Fatalf("%s got %d sweep entries, want the whole array", device.Target.Name, len(decoded))
			}
		}
		if len(seen) != 3 {
			t.Fatalf("sweep indices = %v, want three distinct values", seen)
		}
	})

	t.Run("sweep without parameters errors", func(t *testing.T) {
		in := trainPlanTestInput()
		in.IsSweep = true
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil || !strings.Contains(err.Error(), "--sweep") {
			t.Fatalf("error = %v, want it to ask for --sweep", err)
		}
	})

	t.Run("count mismatch names both counts", func(t *testing.T) {
		in := trainPlanTestInput()
		in.IsSweep = true
		in.SweepParams = params[:1]
		_, err := computeTrainPlan(trainPlanTestTargets(), in)
		if err == nil {
			t.Fatal("expected a count mismatch error")
		}
		if !strings.Contains(err.Error(), "1 entries") || !strings.Contains(err.Error(), "3 device") {
			t.Fatalf("error %q should name both the parameter count and the device count", err)
		}
	})

	t.Run("parameters on a non-sweep template warn and are dropped", func(t *testing.T) {
		in := trainPlanTestInput()
		in.SweepParams = params
		plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
		want := "sweep parameters are ignored: only the sweep template reads them"
		if len(plan.Warnings) != 1 || plan.Warnings[0] != want {
			t.Fatalf("warnings = %v, want exactly %q", plan.Warnings, want)
		}
		for _, device := range plan.Devices {
			if _, ok := device.Env["WT_SWEEP_INDEX"]; ok {
				t.Fatalf("%s got WT_SWEEP_INDEX on a non-sweep template", device.Target.Name)
			}
			if _, ok := device.Env["WT_SWEEP_PARAMS"]; ok {
				t.Fatalf("%s got WT_SWEEP_PARAMS on a non-sweep template", device.Target.Name)
			}
		}
	})
}

func TestTrainPlanLANPeersResolvedExcludingSelf(t *testing.T) {
	in := trainPlanTestInput()
	in.Transport = transportLAN
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
	byName := trainPlanByName(plan)

	// Containers cannot resolve multicast names, so the plan ships addresses.
	if got, want := byName["spark-3011"].Env["MESH_PEERS"], "192.0.2.11:8080,192.0.2.83:8080"; got != want {
		t.Fatalf("spark-3011 MESH_PEERS = %q, want %q", got, want)
	}
	if got, want := byName["spark-48fd"].Env["MESH_PEERS"], "192.0.2.83:8080,192.0.2.34:8080"; got != want {
		t.Fatalf("spark-48fd MESH_PEERS = %q, want %q", got, want)
	}
	for _, device := range plan.Devices {
		peers := device.Env["MESH_PEERS"]
		if strings.Contains(peers, ".local") || strings.Contains(device.Env["WT_COORDINATOR"], ".local") {
			t.Fatalf("%s carries an mDNS name: peers=%q coordinator=%q", device.Target.Name, peers, device.Env["WT_COORDINATOR"])
		}
		if strings.Contains(peers, device.Target.Address) {
			t.Fatalf("%s MESH_PEERS %q must exclude its own address", device.Target.Name, peers)
		}
		if len(strings.Split(peers, ",")) != 2 {
			t.Fatalf("%s MESH_PEERS = %q, want two peers", device.Target.Name, peers)
		}
		// Asset ids still identify the device even when peers are addresses.
		if device.Env["MESH_SELF"] != trainAssetIDString(device.Target.AssetID) {
			t.Fatalf("%s MESH_SELF = %q", device.Target.Name, device.Env["MESH_SELF"])
		}
	}
	if byName["spark-48fd"].Role != trainRoleCoordinator {
		t.Fatalf("spark-48fd role = %q, want coordinator", byName["spark-48fd"].Role)
	}
	if !plan.LANRewrite {
		t.Fatal("the lan transport must flag the host-network rewrite")
	}
}

func TestTrainPlanTopologyTrio(t *testing.T) {
	// Found on hardware: with address peers, a template cannot derive its
	// topology numerically, so the trio is emitted for every transport.
	cases := []struct {
		transport   trainTransport
		coordinator string
	}{
		{transportMesh, "device-211.cloud.wendy.dev:8080"},
		{transportLAN, "192.0.2.11:8080"},
	}
	for _, tc := range cases {
		t.Run(string(tc.transport), func(t *testing.T) {
			in := trainPlanTestInput()
			in.Transport = tc.transport
			plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
			for _, device := range plan.Devices {
				if got := device.Env["WT_COORDINATOR"]; got != tc.coordinator {
					t.Fatalf("%s WT_COORDINATOR = %q, want %q", device.Target.Name, got, tc.coordinator)
				}
				if got := device.Env["WT_NODE_COUNT"]; got != "3" {
					t.Fatalf("%s WT_NODE_COUNT = %q, want 3", device.Target.Name, got)
				}
				if got := device.Env["WT_NODE_INDEX"]; got != strconv.Itoa(device.Rank) {
					t.Fatalf("%s WT_NODE_INDEX = %q, want %d", device.Target.Name, got, device.Rank)
				}
			}
			if plan.Devices[0].Env["WT_NODE_INDEX"] != "0" || !isTrainCoordinatorRole(plan.Devices[0].Role) {
				t.Fatalf("index 0 must be the coordinator, got %+v", plan.Devices[0])
			}
		})
	}
}

func TestTrainPlanRankFallbackWithoutAssetIDs(t *testing.T) {
	targets := []fleetTarget{
		{Name: "spark-edeb", ID: "spark-edeb.local", Address: "192.0.2.83", AssetID: 283},
		{Name: "spark-3011", ID: "spark-3011.local", Address: "192.0.2.34"},
		{Name: "spark-48fd", ID: "spark-48fd.local", Address: "192.0.2.11"},
	}

	t.Run("lan ranks by name and warns", func(t *testing.T) {
		in := trainPlanTestInput()
		in.Transport = transportLAN
		plan := mustComputeTrainPlan(t, targets, in)

		order := []string{plan.Devices[0].Target.Name, plan.Devices[1].Target.Name, plan.Devices[2].Target.Name}
		want := []string{"spark-3011", "spark-48fd", "spark-edeb"}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("order = %v, want %v (ascending name)", order, want)
			}
		}
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "ranking by device name") {
			t.Fatalf("warnings = %v, want a name-ranking warning", plan.Warnings)
		}
		if !strings.Contains(plan.Warnings[0], "spark-3011") || !strings.Contains(plan.Warnings[0], "spark-48fd") {
			t.Fatalf("warning %q must name the devices without an asset id", plan.Warnings[0])
		}
		byName := trainPlanByName(plan)
		for _, name := range []string{"spark-3011", "spark-48fd"} {
			if _, ok := byName[name].Env["MESH_SELF"]; ok {
				t.Fatalf("%s has no asset id, so MESH_SELF must be omitted", name)
			}
		}
		if byName["spark-edeb"].Env["MESH_SELF"] != "283" {
			t.Fatalf("spark-edeb MESH_SELF = %q, want 283", byName["spark-edeb"].Env["MESH_SELF"])
		}
		// Rank 0 by name coordinates, and the address peers still work.
		if byName["spark-3011"].Role != trainRoleCoordinator {
			t.Fatalf("spark-3011 role = %q, want coordinator", byName["spark-3011"].Role)
		}
		if got, want := byName["spark-48fd"].Env["WT_COORDINATOR"], "192.0.2.34:8080"; got != want {
			t.Fatalf("WT_COORDINATOR = %q, want %q", got, want)
		}
	})

	t.Run("mesh refuses and names the devices", func(t *testing.T) {
		in := trainPlanTestInput()
		in.Transport = transportMesh
		_, err := computeTrainPlan(targets, in)
		if err == nil {
			t.Fatal("mesh must refuse to guess an identity for an id-less device")
		}
		for _, name := range []string{"spark-3011", "spark-48fd"} {
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %q must name %s", err, name)
			}
		}
		if strings.Contains(err.Error(), "spark-edeb") {
			t.Fatalf("error %q names a device that does have an asset id", err)
		}
	})
}

func TestTrainPlanEnvMapsAreNotShared(t *testing.T) {
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), trainPlanTestInput())
	if len(plan.Devices) < 2 {
		t.Fatal("need at least two devices")
	}
	plan.Devices[0].Env["WT_RUN_ID"] = "mutated"
	plan.Devices[0].Env["ONLY_ON_FIRST"] = "1"
	for _, device := range plan.Devices[1:] {
		if device.Env["WT_RUN_ID"] != "demo-1" {
			t.Fatalf("%s saw a mutation of another device's env", device.Target.Name)
		}
		if _, ok := device.Env["ONLY_ON_FIRST"]; ok {
			t.Fatalf("%s shares its env map with another device", device.Target.Name)
		}
	}
}

func TestValidateTrainEnv(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		got, err := validateTrainEnv([]string{"WT_RUN_ID=demo-1", "ES_POP=64", "EMPTY=", "WITH_EQUALS=a=b"})
		if err != nil {
			t.Fatalf("validateTrainEnv: %v", err)
		}
		want := map[string]string{"WT_RUN_ID": "demo-1", "ES_POP": "64", "EMPTY": "", "WITH_EQUALS": "a=b"}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("key %s = %q, want %q", k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("got %d entries, want %d", len(got), len(want))
		}
	})

	t.Run("later duplicates win", func(t *testing.T) {
		got, err := validateTrainEnv([]string{"SEED=1", "SEED=2"})
		if err != nil {
			t.Fatalf("validateTrainEnv: %v", err)
		}
		if got["SEED"] != "2" {
			t.Fatalf("SEED = %q, want 2", got["SEED"])
		}
	})

	for _, tc := range []struct {
		name    string
		entry   string
		wantSub string
	}{
		{"missing equals sign", "JUST_A_KEY", "KEY=VALUE"},
		{"lowercase is fine but a digit start is not", "1BAD=x", "environment variable name"},
		{"dash in key", "BAD-KEY=x", "environment variable name"},
		{"empty key", "=value", "environment variable name"},
		{"blocked WENDY_ prefix", "WENDY_APP_ID=x", "WENDY_"},
		{"blocked LD_ prefix", "LD_PRELOAD=/tmp/x.so", "LD_"},
		{"blocked DYLD_ prefix", "DYLD_INSERT_LIBRARIES=/tmp/x", "DYLD_"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateTrainEnv([]string{tc.entry})
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.entry)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q should mention %q", err, tc.wantSub)
			}
		})
	}

	t.Run("blocked prefixes explain the agent rejection", func(t *testing.T) {
		_, err := validateTrainEnv([]string{"WENDY_ANYTHING=1"})
		if err == nil || !strings.Contains(err.Error(), "container create") {
			t.Fatalf("error = %v, want it to explain where the deploy would fail", err)
		}
	})
}

func TestParseSweepParams(t *testing.T) {
	t.Run("inline array", func(t *testing.T) {
		got, err := parseSweepParams(`[{"seed": 1}, {"seed": 2}]`)
		if err != nil {
			t.Fatalf("parseSweepParams: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2", len(got))
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("re-encoding: %v", err)
		}
		// Numbers must survive as written; a float round trip would print 1e+00.
		if string(encoded) != `[{"seed":1},{"seed":2}]` {
			t.Fatalf("re-encoded = %s", encoded)
		}
	})

	t.Run("leading whitespace still counts as inline", func(t *testing.T) {
		if _, err := parseSweepParams("  [ {\"seed\": 1} ] "); err != nil {
			t.Fatalf("parseSweepParams: %v", err)
		}
	})

	t.Run("file path", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sweep.json")
		if err := os.WriteFile(path, []byte(`[{"seed":1},{"seed":2},{"seed":3}]`+"\n"), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		got, err := parseSweepParams(path)
		if err != nil {
			t.Fatalf("parseSweepParams: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d entries, want 3", len(got))
		}
	})

	for _, tc := range []struct {
		name    string
		arg     string
		wantSub string
	}{
		{"not an array", `{"seed": 1}`, "array"},
		{"element is not an object", `[{"seed":1}, 7]`, "entry 1"},
		{"element is a string", `["seed"]`, "entry 0"},
		{"malformed JSON", `[{"seed":]`, "array"},
		{"empty argument", "  ", "--sweep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseSweepParams(tc.arg)
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q should mention %q", err, tc.wantSub)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := parseSweepParams(filepath.Join(t.TempDir(), "absent.json"))
		if err == nil || !strings.Contains(err.Error(), "absent.json") {
			t.Fatalf("error = %v, want it to name the missing file", err)
		}
	})
}

func TestParseRolePins(t *testing.T) {
	got, err := parseRolePins([]string{"spark-48fd=coordinator", "spark-3011=Worker", " spark-edeb = learner "})
	if err != nil {
		t.Fatalf("parseRolePins: %v", err)
	}
	want := map[string]string{"spark-48fd": "coordinator", "spark-3011": "worker", "spark-edeb": "learner"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("pin %s = %q, want %q", k, got[k], v)
		}
	}

	for _, tc := range []struct {
		name    string
		entry   string
		wantSub string
	}{
		{"no equals sign", "spark-48fd", "device=role"},
		{"empty device", "=worker", "device=role"},
		{"empty role", "spark-48fd=", "device=role"},
		{"unknown role", "spark-48fd=captain", "captain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRolePins([]string{tc.entry}); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}

	t.Run("actor is a role", func(t *testing.T) {
		got, err := parseRolePins([]string{"spark-3011=actor"})
		if err != nil || got["spark-3011"] != trainRoleActor {
			t.Fatalf("got %v, err %v", got, err)
		}
	})
}

func TestRenderTrainPlanMasksToken(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	in := trainPlanTestInput()
	in.BaseEnv["WT_FLEET_TOKEN"] = token
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)
	plan.Source = "embedded"

	var buf bytes.Buffer
	renderTrainPlan(&buf, plan, "/tmp/stage-es", true)
	out := buf.String()

	if strings.Contains(out, token) {
		t.Fatalf("the token leaked into the rendered plan:\n%s", out)
	}
	if !strings.Contains(out, "WT_FLEET_TOKEN=<masked, 32 hex chars>") {
		t.Fatalf("expected a masked token line, got:\n%s", out)
	}
	for _, want := range []string{
		"template:  es-fleet (embedded)",
		"app id:    sh.wendy.training.es",
		"group:     spark-*",
		"transport: mesh",
		"staged:    /tmp/stage-es",
		"device spark-48fd (asset 211, role coordinator, rank 0)",
		"device spark-edeb (asset 283, role worker, rank 1)",
		"device spark-3011 (asset 334, role worker, rank 2)",
		"  env MESH_PEERS=211,283,334",
		"  env WT_NODE_COUNT=3",
		"was not saved",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered plan missing %q:\n%s", want, out)
		}
	}
	// Env lines are sorted so two renders of the same plan diff cleanly.
	first := strings.Index(out, "  env ES_POP=64")
	second := strings.Index(out, "  env MESH_PEERS=")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("env lines are not sorted:\n%s", out)
	}

	t.Run("a saved token has no ephemeral note", func(t *testing.T) {
		var saved bytes.Buffer
		renderTrainPlan(&saved, plan, "/tmp/stage-es", false)
		if strings.Contains(saved.String(), "was not saved") {
			t.Fatalf("unexpected ephemeral note:\n%s", saved.String())
		}
	})
}

func TestRenderTrainPlanShowsLANRewriteAndStagingSkipped(t *testing.T) {
	in := trainPlanTestInput()
	in.Transport = transportLAN
	in.SweepParams = []map[string]any{{"seed": 1}}
	plan := mustComputeTrainPlan(t, trainPlanTestTargets(), in)

	var buf bytes.Buffer
	renderTrainPlan(&buf, plan, "", false)
	out := buf.String()

	for _, want := range []string{
		"transport: lan",
		"staging:   skipped (--dry-run; pass --stage-dir to stage)",
		`network entitlement rewritten to: {"type": "network", "mode": "host"}`,
		"warning: sweep parameters are ignored",
		"  env MESH_PEERS=192.0.2.83:8080,192.0.2.34:8080",
		"  env WT_COORDINATOR=192.0.2.11:8080",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered plan missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "staged:") {
		t.Fatalf("nothing was staged, so no staged line belongs in:\n%s", out)
	}

	t.Run("id-less device renders an honest asset column", func(t *testing.T) {
		targets := []fleetTarget{
			{Name: "spark-3011", Address: "192.0.2.34"},
			{Name: "spark-edeb", Address: "192.0.2.83", AssetID: 283},
		}
		lanIn := trainPlanTestInput()
		lanIn.Transport = transportLAN
		lanPlan := mustComputeTrainPlan(t, targets, lanIn)
		var lanBuf bytes.Buffer
		renderTrainPlan(&lanBuf, lanPlan, "", false)
		if !strings.Contains(lanBuf.String(), "device spark-3011 (asset unknown, role coordinator, rank 0)") {
			t.Fatalf("expected an unknown asset id, got:\n%s", lanBuf.String())
		}
	})
}

func TestLoadTrainConfigFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(dir, "train.json")
		body := `{
  "group": "spark-*",
  "lan": true,
  "template": "es-fleet",
  "transport": "lan",
  "env": {"WT_RUN_ID": "demo-1"},
  "sweep": [{"seed": 1}],
  "roles": {"spark-48fd": "coordinator"}
}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		cfg, err := loadTrainConfigFile(path)
		if err != nil {
			t.Fatalf("loadTrainConfigFile: %v", err)
		}
		if cfg.Group != "spark-*" || cfg.Template != "es-fleet" || cfg.Transport != "lan" {
			t.Fatalf("cfg = %+v", cfg)
		}
		if cfg.LAN == nil || !*cfg.LAN {
			t.Fatalf("lan = %v, want true (a pointer so an absent key differs from false)", cfg.LAN)
		}
		if cfg.Env["WT_RUN_ID"] != "demo-1" {
			t.Fatalf("env = %v", cfg.Env)
		}
		if len(cfg.Sweep) != 1 || cfg.Roles["spark-48fd"] != "coordinator" {
			t.Fatalf("sweep = %v, roles = %v", cfg.Sweep, cfg.Roles)
		}
	})

	t.Run("absent lan key stays nil", func(t *testing.T) {
		path := filepath.Join(dir, "minimal.json")
		if err := os.WriteFile(path, []byte(`{"template":"single"}`), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		cfg, err := loadTrainConfigFile(path)
		if err != nil {
			t.Fatalf("loadTrainConfigFile: %v", err)
		}
		if cfg.LAN != nil {
			t.Fatalf("lan = %v, want nil so the flag default survives", *cfg.LAN)
		}
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		path := filepath.Join(dir, "typo.json")
		if err := os.WriteFile(path, []byte(`{"template":"single","tempalte":"single"}`), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		_, err := loadTrainConfigFile(path)
		if err == nil || !strings.Contains(err.Error(), "tempalte") {
			t.Fatalf("error = %v, want it to name the unknown field", err)
		}
	})

	t.Run("unknown transport rejected", func(t *testing.T) {
		path := filepath.Join(dir, "transport.json")
		if err := os.WriteFile(path, []byte(`{"transport":"carrier-pigeon"}`), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		if _, err := loadTrainConfigFile(path); err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
			t.Fatalf("error = %v, want it to name the unknown transport", err)
		}
	})

	t.Run("unknown role rejected", func(t *testing.T) {
		path := filepath.Join(dir, "roles.json")
		if err := os.WriteFile(path, []byte(`{"roles":{"spark-3011":"captain"}}`), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		if _, err := loadTrainConfigFile(path); err == nil || !strings.Contains(err.Error(), "captain") {
			t.Fatalf("error = %v, want it to name the unknown role", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := loadTrainConfigFile(filepath.Join(dir, "absent.json"))
		if err == nil || !strings.Contains(err.Error(), "absent.json") {
			t.Fatalf("error = %v, want it to name the missing file", err)
		}
	})

	t.Run("trailing content rejected", func(t *testing.T) {
		path := filepath.Join(dir, "trailing.json")
		if err := os.WriteFile(path, []byte(`{"template":"single"}{"template":"other"}`), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		if _, err := loadTrainConfigFile(path); err == nil || !strings.Contains(err.Error(), "single JSON object") {
			t.Fatalf("error = %v, want a single-object error", err)
		}
	})
}

func TestMaskSecretValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"generated token", "0123456789abcdef0123456789abcdef", "<masked, 32 hex chars>"},
		{"uppercase hex", "DEADBEEF", "<masked, 8 hex chars>"},
		{"operator chosen", "operator-chosen", "<masked, 15 chars>"},
		{"non hex letters", "zzzz", "<masked, 4 chars>"},
		{"empty", "", "<masked, 0 chars>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskSecretValue(tc.value); got != tc.want {
				t.Fatalf("maskSecretValue(%q) = %q, want %q", tc.value, got, tc.want)
			}
			if tc.value != "" && strings.Contains(maskSecretValue(tc.value), tc.value) {
				t.Fatalf("mask %q leaks the value", maskSecretValue(tc.value))
			}
		})
	}
}
