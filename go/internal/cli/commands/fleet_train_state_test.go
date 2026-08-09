package commands

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain sandboxes HOME for this package, so these tests read and write a
// throwaway configuration directory rather than the developer's own.

func TestNewFleetTokenIsRandomHex(t *testing.T) {
	first, err := newFleetToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newFleetToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatalf("token length = %d, want 32", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("token is not hexadecimal: %v", err)
	}
	if first == second {
		t.Fatal("two generated tokens are identical; the source is not random")
	}
}

func TestEnsureFleetTokenPrecedence(t *testing.T) {
	const group, appID = "sparks", "sh.wendy.training.es-fleet"

	// Nothing saved yet: a token is generated and, once persisted, reused so a
	// later status or stop against the same fleet can authenticate.
	generated, ephemeral, err := ensureFleetToken("", nil, group, appID, true)
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral {
		t.Fatal("a persisting call must not report an ephemeral token")
	}
	if err := saveTrainState(trainState{Token: generated, AppID: appID, Group: group}); err != nil {
		t.Fatal(err)
	}
	again, _, err := ensureFleetToken("", nil, group, appID, true)
	if err != nil {
		t.Fatal(err)
	}
	if again != generated {
		t.Fatalf("token changed across invocations: %q then %q", generated, again)
	}

	// An explicit token wins over the saved one, whether passed as a flag or
	// through the environment map.
	explicit, _, err := ensureFleetToken("operator-chosen", nil, group, appID, true)
	if err != nil {
		t.Fatal(err)
	}
	if explicit != "operator-chosen" {
		t.Fatalf("explicit token ignored, got %q", explicit)
	}
	fromEnv, _, err := ensureFleetToken("", map[string]string{trainFleetTokenEnvKey: "from-env"}, group, appID, true)
	if err != nil {
		t.Fatal(err)
	}
	if fromEnv != "from-env" {
		t.Fatalf("environment token ignored, got %q", fromEnv)
	}
}

func TestEnsureFleetTokenDryRunPersistsNothing(t *testing.T) {
	const group, appID = "dry", "sh.wendy.training.single"

	token, ephemeral, err := ensureFleetToken("", nil, group, appID, false)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected a token to render")
	}
	if !ephemeral {
		t.Fatal("a non-persisting call generated a token but did not report it as ephemeral")
	}
	path, err := trainStatePath(group, appID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote %s; it must leave no state behind", path)
	}
}

func TestSaveTrainStateIsOwnerOnly(t *testing.T) {
	st := trainState{
		Token: "abc123", AppID: "sh.wendy.training.sweep", Group: "sparks",
		Template: "sweep", Transport: "lan", MeshPort: 8080,
	}
	if err := saveTrainState(st); err != nil {
		t.Fatal(err)
	}
	path, err := trainStatePath(st.Group, st.AppID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The file holds a bearer token, so it must not be group or world readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", perm)
	}

	loaded, ok := loadTrainState(st.Group, st.AppID)
	if !ok {
		t.Fatal("saved state did not load back")
	}
	if loaded.Token != st.Token || loaded.Transport != "lan" || loaded.MeshPort != 8080 {
		t.Fatalf("round trip lost fields: %+v", loaded)
	}
	if loaded.UpdatedAt == "" {
		t.Fatal("UpdatedAt was not stamped")
	}
	// No temporary file may survive the atomic write.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary state file was left behind")
	}
}

func TestTrainStatePathSeparatesFleets(t *testing.T) {
	a, err := trainStatePath("sparks", "sh.wendy.training.es-fleet")
	if err != nil {
		t.Fatal(err)
	}
	b, err := trainStatePath("sparks", "sh.wendy.training.sweep")
	if err != nil {
		t.Fatal(err)
	}
	c, err := trainStatePath("others", "sh.wendy.training.es-fleet")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a == c {
		t.Fatalf("state paths collide: %q %q %q", a, b, c)
	}
	// Application ids contain dots and the group is user supplied; neither may
	// escape the training state directory.
	for _, p := range []string{a, b, c} {
		if strings.Contains(filepath.Base(p), "/") || strings.Contains(p, "..") {
			t.Fatalf("unsafe state path %q", p)
		}
	}
}

func TestLoadTrainStateMissingIsNotAnError(t *testing.T) {
	if _, ok := loadTrainState("never-deployed", "sh.wendy.training.byo"); ok {
		t.Fatal("expected no state for a fleet that was never deployed")
	}
}
