package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	trainingassets "github.com/wendylabsinc/wendy/Training"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// stagedRelPaths lists every regular file in a staged context, as
// slash-separated paths relative to its root.
func stagedRelPaths(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking staged dir: %v", err)
	}
	sort.Strings(out)
	return out
}

// readStageManifest parses the manifest a staging wrote.
func readStageManifest(t *testing.T, dir string) stageManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, stageManifestName))
	if err != nil {
		t.Fatalf("reading %s: %v", stageManifestName, err)
	}
	var m stageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing %s: %v", stageManifestName, err)
	}
	return m
}

// stageEmbedded resolves and stages a built-in template into a fresh directory.
func stageEmbedded(t *testing.T, name string) (templateSource, string) {
	t.Helper()
	src, _, err := resolveTemplateSource(name)
	if err != nil {
		t.Fatalf("resolveTemplateSource(%q): %v", name, err)
	}
	dest := t.TempDir()
	staged, err := stageTrainingContext(src, dest)
	if err != nil {
		t.Fatalf("stageTrainingContext(%q): %v", name, err)
	}
	if staged != dest {
		t.Fatalf("staged into %q, want %q", staged, dest)
	}
	return src, staged
}

func TestStageTrainingContextChecksumsMatchSource(t *testing.T) {
	_, staged := stageEmbedded(t, "es-fleet")

	// Every library module must arrive byte for byte; the manifest is only
	// meaningful if staging is a copy and not a transformation.
	libraryFiles := 0
	err := fs.WalkDir(trainingassets.Assets, "wendytrain", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".py") {
			return nil
		}
		want, err := trainingassets.Assets.ReadFile(p)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(staged, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("staged %s: %v", p, err)
			return nil
		}
		if string(got) != string(want) {
			t.Errorf("staged %s differs from the embedded original", p)
		}
		libraryFiles++
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded library: %v", err)
	}
	if libraryFiles == 0 {
		t.Fatal("no wendytrain Python modules were staged")
	}

	manifest := readStageManifest(t, staged)
	if manifest.Created == "" {
		t.Error("manifest has no created timestamp")
	}

	onDisk := stagedRelPaths(t, staged)
	var wantListed []string
	for _, rel := range onDisk {
		if rel != stageManifestName {
			wantListed = append(wantListed, rel)
		}
	}
	listed := make([]string, 0, len(manifest.Files))
	for rel := range manifest.Files {
		listed = append(listed, rel)
	}
	sort.Strings(listed)
	if strings.Join(listed, "\n") != strings.Join(wantListed, "\n") {
		t.Errorf("manifest lists\n%s\nwant\n%s", strings.Join(listed, "\n"), strings.Join(wantListed, "\n"))
	}
	if _, ok := manifest.Files[stageManifestName]; ok {
		t.Error("the manifest lists itself")
	}

	for rel, entry := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(staged, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading staged %s: %v", rel, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
			t.Errorf("%s: manifest sha256 %s, recomputed %s", rel, entry.SHA256, got)
		}
		if entry.Bytes != int64(len(data)) {
			t.Errorf("%s: manifest bytes %d, actual %d", rel, entry.Bytes, len(data))
		}
	}
}

func TestStageExcludesTestsAndCaches(t *testing.T) {
	for _, name := range embeddedTemplateNames() {
		t.Run(name, func(t *testing.T) {
			_, staged := stageEmbedded(t, name)
			for _, rel := range stagedRelPaths(t, staged) {
				if stageCopyIgnored(rel) {
					t.Errorf("staged an excluded path: %s", rel)
				}
			}
		})
	}
}

func TestStageCartpoleReference(t *testing.T) {
	wantCartpole, err := trainingassets.Assets.ReadFile("templates/single/cartpole.py")
	if err != nil {
		t.Fatalf("reading embedded cartpole.py: %v", err)
	}

	t.Run("path template referencing cartpole", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{
			"wendy.json": minimalTemplateJSON,
			"Dockerfile": "FROM python:3.11-slim\nCOPY cartpole.py /app/cartpole.py\n",
		})
		src, _, err := resolveTemplateSource(dir)
		if err != nil {
			t.Fatalf("resolveTemplateSource: %v", err)
		}
		staged, err := stageTrainingContext(src, t.TempDir())
		if err != nil {
			t.Fatalf("stageTrainingContext: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(staged, "cartpole.py"))
		if err != nil {
			t.Fatalf("cartpole.py was not staged: %v", err)
		}
		if string(got) != string(wantCartpole) {
			t.Error("staged cartpole.py differs from templates/single/cartpole.py")
		}
	})

	t.Run("byo does not reference cartpole", func(t *testing.T) {
		_, staged := stageEmbedded(t, "byo")
		if _, err := os.Stat(filepath.Join(staged, "cartpole.py")); !os.IsNotExist(err) {
			t.Errorf("byo staged a cartpole.py it never references (stat error: %v)", err)
		}
	})

	t.Run("a template keeps its own copy", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{
			"wendy.json":  minimalTemplateJSON,
			"Dockerfile":  "FROM python:3.11-slim\nCOPY cartpole.py /app/cartpole.py\n",
			"cartpole.py": "# a template's own cartpole\n",
		})
		src, _, err := resolveTemplateSource(dir)
		if err != nil {
			t.Fatalf("resolveTemplateSource: %v", err)
		}
		staged, err := stageTrainingContext(src, t.TempDir())
		if err != nil {
			t.Fatalf("stageTrainingContext: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(staged, "cartpole.py"))
		if err != nil {
			t.Fatalf("reading staged cartpole.py: %v", err)
		}
		if string(got) != "# a template's own cartpole\n" {
			t.Error("staging overwrote a file the template provided")
		}
	})
}

func TestStageSweepIncludesSingleTrain(t *testing.T) {
	_, staged := stageEmbedded(t, "sweep")

	want, err := trainingassets.Assets.ReadFile("templates/single/train.py")
	if err != nil {
		t.Fatalf("reading embedded single/train.py: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(staged, "single_train.py"))
	if err != nil {
		t.Fatalf("single_train.py was not staged: %v", err)
	}
	if string(got) != string(want) {
		t.Error("staged single_train.py differs from templates/single/train.py")
	}

	// The sweep template ships its own train.py, which must survive alongside.
	sweepTrain, err := os.ReadFile(filepath.Join(staged, "train.py"))
	if err != nil {
		t.Fatalf("sweep train.py was not staged: %v", err)
	}
	if string(sweepTrain) == string(want) {
		t.Error("the sweep template's train.py was replaced by the single template's")
	}

	manifest := readStageManifest(t, staged)
	if _, ok := manifest.Files["single_train.py"]; !ok {
		t.Error("the manifest does not list single_train.py")
	}
}

func TestApplyLANHostNetworking(t *testing.T) {
	meshEntitlement := appconfig.Entitlement{
		Type:        appconfig.EntitlementNetwork,
		Mode:        "mesh",
		ServiceCIDR: "10.99.0.0/16",
		Ports:       []appconfig.PortMapping{{Host: 8080, Container: 8080}},
	}
	persistEntitlement := appconfig.Entitlement{
		Type: "persist",
		Name: "wt-ckpt",
		Path: "/data/checkpoints",
	}
	hostEntitlement := appconfig.Entitlement{Type: appconfig.EntitlementNetwork, Mode: "host"}

	t.Run("service entitlement", func(t *testing.T) {
		cfg := &appconfig.AppConfig{
			AppID: "sh.wendy.training.es-fleet",
			Services: map[string]*appconfig.ServiceConfig{
				"trainer": {
					Context:      ".",
					Entitlements: []appconfig.Entitlement{meshEntitlement, persistEntitlement},
				},
			},
		}
		if err := applyLANHostNetworking(cfg); err != nil {
			t.Fatalf("applyLANHostNetworking: %v", err)
		}
		got := cfg.Services["trainer"].Entitlements
		if len(got) != 2 {
			t.Fatalf("entitlement count changed: %d", len(got))
		}
		if !reflect.DeepEqual(got[0], hostEntitlement) {
			t.Errorf("network entitlement is %+v, want %+v", got[0], hostEntitlement)
		}
		if got[1].Type != persistEntitlement.Type || got[1].Name != persistEntitlement.Name || got[1].Path != persistEntitlement.Path {
			t.Errorf("persist entitlement changed: %+v", got[1])
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("rewritten config no longer validates: %v", err)
		}
	})

	t.Run("top level entitlement", func(t *testing.T) {
		cfg := &appconfig.AppConfig{
			AppID:        "sh.wendy.training.byo",
			Entitlements: []appconfig.Entitlement{meshEntitlement, persistEntitlement},
		}
		if err := applyLANHostNetworking(cfg); err != nil {
			t.Fatalf("applyLANHostNetworking: %v", err)
		}
		if !reflect.DeepEqual(cfg.Entitlements[0], hostEntitlement) {
			t.Errorf("network entitlement is %+v, want %+v", cfg.Entitlements[0], hostEntitlement)
		}
		if cfg.Entitlements[1].Name != "wt-ckpt" {
			t.Errorf("persist entitlement changed: %+v", cfg.Entitlements[1])
		}
	})

	t.Run("top level and every service are rewritten", func(t *testing.T) {
		cfg := &appconfig.AppConfig{
			AppID:        "sh.wendy.training.multi",
			Entitlements: []appconfig.Entitlement{meshEntitlement},
			Services: map[string]*appconfig.ServiceConfig{
				"a": {Context: ".", Entitlements: []appconfig.Entitlement{meshEntitlement}},
				"b": {Context: ".", Entitlements: []appconfig.Entitlement{persistEntitlement, meshEntitlement}},
				"c": {Context: ".", Entitlements: []appconfig.Entitlement{persistEntitlement}},
			},
		}
		if err := applyLANHostNetworking(cfg); err != nil {
			t.Fatalf("applyLANHostNetworking: %v", err)
		}
		if !reflect.DeepEqual(cfg.Entitlements[0], hostEntitlement) {
			t.Error("top-level network entitlement was not rewritten")
		}
		if !reflect.DeepEqual(cfg.Services["a"].Entitlements[0], hostEntitlement) {
			t.Error("service a was not rewritten")
		}
		if !reflect.DeepEqual(cfg.Services["b"].Entitlements[1], hostEntitlement) {
			t.Error("service b was not rewritten")
		}
		if len(cfg.Services["c"].Entitlements) != 1 || cfg.Services["c"].Entitlements[0].Type != "persist" {
			t.Errorf("service c gained or lost an entitlement: %+v", cfg.Services["c"].Entitlements)
		}
	})

	t.Run("no network entitlement is an error", func(t *testing.T) {
		src, cfg, err := resolveTemplateSource("single")
		if err != nil {
			t.Fatalf("resolveTemplateSource(single): %v", err)
		}
		if src.Name != "single" {
			t.Errorf("source name is %q", src.Name)
		}
		err = applyLANHostNetworking(cfg)
		if err == nil {
			t.Fatal("expected an error for a template with no network entitlement")
		}
		if !strings.Contains(err.Error(), cfg.AppID) {
			t.Errorf("error does not name the app: %v", err)
		}
		if !strings.Contains(err.Error(), "lan") || !strings.Contains(err.Error(), "network entitlement") {
			t.Errorf("error does not explain that the lan transport needs a network entitlement: %v", err)
		}
	})

	t.Run("nil config is an error", func(t *testing.T) {
		if err := applyLANHostNetworking(nil); err == nil {
			t.Fatal("expected an error for a nil config")
		}
	})

	t.Run("the staged wendy.json on disk is untouched", func(t *testing.T) {
		src, cfg, err := resolveTemplateSource("es-fleet")
		if err != nil {
			t.Fatalf("resolveTemplateSource: %v", err)
		}
		staged, err := stageTrainingContext(src, t.TempDir())
		if err != nil {
			t.Fatalf("stageTrainingContext: %v", err)
		}
		stagedJSON := filepath.Join(staged, "wendy.json")
		before, err := os.ReadFile(stagedJSON)
		if err != nil {
			t.Fatalf("reading staged wendy.json: %v", err)
		}
		beforeManifest := readStageManifest(t, staged)

		if err := applyLANHostNetworking(cfg); err != nil {
			t.Fatalf("applyLANHostNetworking: %v", err)
		}

		after, err := os.ReadFile(stagedJSON)
		if err != nil {
			t.Fatalf("re-reading staged wendy.json: %v", err)
		}
		if string(after) != string(before) {
			t.Error("applyLANHostNetworking rewrote the staged wendy.json; the rewrite must stay in memory")
		}
		sum := sha256.Sum256(after)
		if got, want := hex.EncodeToString(sum[:]), beforeManifest.Files["wendy.json"].SHA256; got != want {
			t.Errorf("staged wendy.json no longer matches the manifest: %s != %s", got, want)
		}
		// The in-memory copy really did change, so the test above is not
		// passing for the trivial reason that nothing happened.
		if !strings.Contains(string(before), `"mesh"`) {
			t.Fatal("the es-fleet template no longer declares a mesh network entitlement; this test needs updating")
		}
		if cfg.Services["trainer"].Entitlements[0].Mode != "host" {
			t.Error("the in-memory config was not rewritten")
		}
	})
}

func TestResolveTemplateSource(t *testing.T) {
	t.Run("built-in names", func(t *testing.T) {
		for _, name := range []string{"byo", "es-fleet", "ppo-fleet", "single", "sweep"} {
			src, cfg, err := resolveTemplateSource(name)
			if err != nil {
				t.Fatalf("resolveTemplateSource(%q): %v", name, err)
			}
			if !src.Embedded {
				t.Errorf("%s: expected an embedded source", name)
			}
			if src.Dir != "" {
				t.Errorf("%s: embedded source has Dir %q, want empty", name, src.Dir)
			}
			if src.Name != name {
				t.Errorf("%s: Name is %q", name, src.Name)
			}
			if src.FS == nil {
				t.Fatalf("%s: source has no filesystem", name)
			}
			if want := "sh.wendy.training." + name; cfg.AppID != want {
				t.Errorf("%s: appId is %q, want %q", name, cfg.AppID, want)
			}
		}
	})

	t.Run("unknown name lists the built-ins", func(t *testing.T) {
		_, _, err := resolveTemplateSource("nope")
		if err == nil {
			t.Fatal("expected an error for an unknown template")
		}
		for _, name := range []string{"byo", "es-fleet", "ppo-fleet", "single", "sweep"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error does not list %q: %v", name, err)
			}
		}
	})

	t.Run("empty name", func(t *testing.T) {
		if _, _, err := resolveTemplateSource("  "); err == nil {
			t.Fatal("expected an error for an empty template name")
		}
	})

	t.Run("directory path", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{"wendy.json": minimalTemplateJSON})
		rel := filepath.Join(dir, ".")
		src, cfg, err := resolveTemplateSource(rel)
		if err != nil {
			t.Fatalf("resolveTemplateSource(%q): %v", rel, err)
		}
		if src.Embedded {
			t.Error("a directory path resolved as an embedded template")
		}
		want, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		if src.Dir != want {
			t.Errorf("Dir is %q, want %q", src.Dir, want)
		}
		if cfg.AppID != "sh.wendy.training.tmp" {
			t.Errorf("appId is %q", cfg.AppID)
		}
	})

	t.Run("directory without a wendy.json", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := resolveTemplateSource(dir)
		if err == nil {
			t.Fatal("expected an error for a directory with no wendy.json")
		}
		if !strings.Contains(err.Error(), "wendy.json") {
			t.Errorf("error does not mention wendy.json: %v", err)
		}
	})

	t.Run("path that is not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "wendy.json")
		if err := os.WriteFile(file, []byte(minimalTemplateJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolveTemplateSource(file); err == nil {
			t.Fatal("expected an error for a path that is not a directory")
		}
	})

	t.Run("invalid wendy.json", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{
			"wendy.json": `{"appId": "sh.wendy.training.bad", "entitlements": [{"type": "network", "mode": "sideways"}]}`,
		})
		if _, _, err := resolveTemplateSource(dir); err == nil {
			t.Fatal("expected a validation error")
		}
	})
}

func TestReferencesModule(t *testing.T) {
	tests := []struct {
		name         string
		template     string
		cartpole     bool
		singleTrain  bool
		templateKind string
	}{
		{name: "byo", template: "byo", cartpole: false, singleTrain: false},
		{name: "es-fleet", template: "es-fleet", cartpole: true, singleTrain: false},
		{name: "ppo-fleet", template: "ppo-fleet", cartpole: true, singleTrain: false},
		{name: "single", template: "single", cartpole: true, singleTrain: false},
		{name: "sweep", template: "sweep", cartpole: true, singleTrain: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, _, err := resolveTemplateSource(tc.template)
			if err != nil {
				t.Fatalf("resolveTemplateSource: %v", err)
			}
			got, err := referencesModule(src.FS, "cartpole")
			if err != nil {
				t.Fatalf("referencesModule(cartpole): %v", err)
			}
			if got != tc.cartpole {
				t.Errorf("cartpole reference is %v, want %v", got, tc.cartpole)
			}
			got, err = referencesModule(src.FS, "single_train")
			if err != nil {
				t.Fatalf("referencesModule(single_train): %v", err)
			}
			if got != tc.singleTrain {
				t.Errorf("single_train reference is %v, want %v", got, tc.singleTrain)
			}
		})
	}

	t.Run("Containerfile counts", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{
			"wendy.json":    minimalTemplateJSON,
			"Containerfile": "FROM python:3.11-slim\nCOPY cartpole.py /app/\n",
		})
		got, err := referencesModule(os.DirFS(dir), "cartpole")
		if err != nil {
			t.Fatalf("referencesModule: %v", err)
		}
		if !got {
			t.Error("a Containerfile reference was not detected")
		}
	})

	t.Run("only the template root is scanned", func(t *testing.T) {
		dir := t.TempDir()
		writeTestTemplate(t, dir, map[string]string{
			"wendy.json":       minimalTemplateJSON,
			"nested/helper.py": "import cartpole\n",
		})
		got, err := referencesModule(os.DirFS(dir), "cartpole")
		if err != nil {
			t.Fatalf("referencesModule: %v", err)
		}
		if got {
			t.Error("a nested file should not count as a root reference")
		}
	})
}

func TestStageCopyIgnored(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{rel: "train.py", want: false},
		{rel: "wendy.json", want: false},
		{rel: "Dockerfile", want: false},
		{rel: "wendytrain/wendytrain/es.py", want: false},
		{rel: "protests/keep.py", want: false},
		{rel: "src/testsuite/a.py", want: false},
		{rel: "not.pycache/a.py", want: false},
		{rel: "", want: false},
		{rel: ".", want: false},
		{rel: "tests", want: true},
		{rel: "tests/test_es.py", want: true},
		{rel: "templates/single/tests/conftest.py", want: true},
		{rel: "__pycache__/train.cpython-311.pyc", want: true},
		{rel: "wendytrain/wendytrain/__pycache__/es.cpython-311.pyc", want: true},
		{rel: "es.pyc", want: true},
		{rel: "wendytrain/wendytrain.egg-info/PKG-INFO", want: true},
		{rel: "wendytrain.egg-info", want: true},
		{rel: ".pytest_cache/CACHEDIR.TAG", want: true},
		{rel: ".venv/lib/python3.11/site-packages/x.py", want: true},
		{rel: ".git/config", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			if got := stageCopyIgnored(tc.rel); got != tc.want {
				t.Errorf("stageCopyIgnored(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// minimalTemplateJSON is the smallest wendy.json that validates, used by the
// path-template tests so they do not depend on any built-in template's shape.
const minimalTemplateJSON = `{
    "appId": "sh.wendy.training.tmp",
    "platform": "linux",
    "version": "0.1.0",
    "entitlements": [
        {"type": "network", "mode": "mesh", "serviceCIDR": "10.99.0.0/16"}
    ]
}
`

// writeTestTemplate lays out a template directory from slash-separated paths.
func writeTestTemplate(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("creating %s: %v", path.Dir(name), err)
		}
		if err := os.WriteFile(target, []byte(files[name]), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}
