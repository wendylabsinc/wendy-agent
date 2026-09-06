package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

func serveTestBundle(t *testing.T) (*httptest.Server, *templateBundleIndex, *int) {
	t.Helper()
	data := buildTestTarball(t, "templates", "python", "fullstack", map[string]string{
		"template.json": `{"name":"fullstack","variables":[{"name":"PORT","type":"integer","default":3001}]}`,
		"wendy.json":    `{"appId":"{{.APP_ID}}","platform":"linux"}`,
		"app.py":        "message = 'Hello from Wendy!'\n",
		"README.md":     "# {{.APP_ID}}\n",
	})
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	index := &templateBundleIndex{
		Version: 1, Revision: "test-revision",
		Catalog: repoMeta{
			Templates: []repoMetaTemplate{{Name: "fullstack", DisplayName: "Web app", Category: "starter", Languages: []string{"python"}}},
			Languages: []repoMetaLanguage{{Key: "python", Name: "Python"}},
		},
		Bundles: map[string]templateBundle{"python/fullstack": {Path: "bundles/" + hash + ".tar.gz", SHA256: hash, Size: int64(len(data))}},
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/topic/starters/template-index.json":
			_ = json.NewEncoder(w).Encode(index)
		case "/topic/starters/bundles/" + hash + ".tar.gz":
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	oldURL, oldClient, oldRoot := templateBundleBaseURL, templateBundleClient, templateCacheRoot
	cache := t.TempDir()
	templateBundleBaseURL, templateBundleClient = server.URL, server.Client()
	templateCacheRoot = func() (string, error) { return cache, nil }
	t.Cleanup(func() {
		server.Close()
		templateBundleBaseURL, templateBundleClient, templateCacheRoot = oldURL, oldClient, oldRoot
	})
	return server, index, &requests
}

func TestTemplateBundleCachedAndOffline(t *testing.T) {
	server, _, requests := serveTestBundle(t)
	ctx := context.Background()
	meta, err := fetchRepoMeta(ctx, "topic/starters")
	if err != nil || meta.Templates[0].Name != "fullstack" {
		t.Fatalf("metadata: %+v %v", meta, err)
	}
	files, manifest, err := downloadTemplateArchive(ctx, "python", "fullstack", "topic/starters", nil)
	if err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("got %d requests; want one index and one selected bundle", *requests)
	}
	vals, err := collectTemplateValues(manifest, "my-app", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := renderAndWriteTemplate(files, dir, "my-app", "fullstack", vals); err != nil {
		t.Fatal(err)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if string(readme) != "# my-app\n" {
		t.Fatalf("rendered README: %q", readme)
	}
	server.Close()
	// A warm cache needs no network at all.
	if _, _, err := downloadTemplateArchive(ctx, "python", "fullstack", "topic/starters", nil); err != nil {
		t.Fatal(err)
	}
	// An expired index can still be used when the server is unreachable.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(templateIndexCachePath("topic/starters"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := downloadTemplateArchive(ctx, "python", "fullstack", "topic/starters", nil); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateBundleRejectsCorruptionAndRepairsCache(t *testing.T) {
	_, index, _ := serveTestBundle(t)
	bundle := index.Bundles["python/fullstack"]
	root, _ := templateCacheRoot()
	path := filepath.Join(root, "bundles", bundle.SHA256+".tar.gz")
	cacheTemplateFile(path, []byte("corrupt"))
	if _, _, err := downloadTemplateBundle(context.Background(), index, "python", "fullstack", "topic/starters", nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !verifyTemplateBundle(data, bundle) {
		t.Fatal("cache was not repaired")
	}
	bundle.Size++
	index.Bundles["python/fullstack"] = bundle
	if _, _, err := downloadTemplateBundle(context.Background(), index, "python", "fullstack", "topic/starters", nil); err == nil || !strings.Contains(err.Error(), "checksum or size") {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestTemplateBundleLegacyAndCancellation(t *testing.T) {
	_, _, _ = serveTestBundle(t)
	index, err := fetchTemplateIndex(context.Background(), "legacy-branch")
	if err != nil || index != nil {
		t.Fatalf("404 should allow legacy fallback: %+v %v", index, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := downloadTemplateArchive(ctx, "python", "fullstack", "topic/starters", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestTemplateBundleMetadataValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	valid := templateBundle{Path: "bundles/" + hash + ".tar.gz", SHA256: hash, Size: 100}
	if !validTemplateBundle(valid) {
		t.Fatal("rejected valid metadata")
	}
	for _, invalid := range []templateBundle{
		{Path: "../../secret", SHA256: hash, Size: 100},
		{Path: valid.Path, SHA256: hash, Size: maxTemplateBundleSize + 1},
		{Path: valid.Path, SHA256: "invalid", Size: 100},
	} {
		if validTemplateBundle(invalid) {
			t.Fatalf("accepted invalid metadata: %+v", invalid)
		}
	}
}

func TestTemplateStarterPickerAndBack(t *testing.T) {
	meta := &repoMeta{Templates: []repoMetaTemplate{
		{Name: "simple-api", DisplayName: "API", Category: "starter"},
		{Name: "camera-feed", DisplayName: "Camera", Category: "starter", Requirements: "A camera"},
		{Name: "llm", Category: "example"},
		{Name: "blink-led", Category: "starter", Targets: []string{targetWendyLite}},
	}}
	starters, examples := starterAndExampleItems(targetWendyOS, meta)
	if len(starters) != 2 || len(examples) != 1 || starters[0].Name != "API" || !strings.Contains(starters[1].Description, "A camera") {
		t.Fatalf("unexpected groups: %+v %+v", starters, examples)
	}
	original := pickInitTemplateItem
	t.Cleanup(func() { pickInitTemplateItem = original })
	answers := []string{"_examples", "_back", "_examples", "llm"}
	pickInitTemplateItem = func(_ string, items []tui.PickerItem) (string, error) {
		answer := answers[0]
		answers = answers[1:]
		for _, item := range items {
			if item.Value == answer {
				return answer, nil
			}
		}
		t.Fatalf("answer %q missing from %+v", answer, items)
		return "", nil
	}
	choice, err := pickStarterOrExample(targetWendyOS, meta, true)
	if err != nil || choice != "llm" || len(answers) != 0 {
		t.Fatalf("picker: %q %v", choice, err)
	}
}

func TestTemplateMissingVariableDoesNotPromptForDefaults(t *testing.T) {
	original := isInteractiveTerminalFn
	isInteractiveTerminalFn = func() bool { return false }
	t.Cleanup(func() { isInteractiveTerminalFn = original })
	manifest := &templateManifest{Variables: []templateVariable{
		{Name: "PORT", Type: "integer", Default: 3001},
		{Name: "API_KEY", Type: "string", Required: true},
	}}
	_, err := collectTemplateValues(manifest, "demo", nil)
	if err == nil || !strings.Contains(err.Error(), "--var API_KEY=") {
		t.Fatalf("expected missing key error, got %v", err)
	}
	vals, err := collectTemplateValues(manifest, "demo", map[string]string{"API_KEY": "test"})
	if err != nil || vals["PORT"] != 3001 {
		t.Fatalf("defaults: %+v %v", vals, err)
	}
}

func TestTemplateRunStartsGeneratedProject(t *testing.T) {
	_, index, _ := serveTestBundle(t)
	originalRun, originalRemember, originalInteractive := runInitializedTemplate, rememberTemplateLanguage, isInteractiveTerminalFn
	t.Cleanup(func() {
		runInitializedTemplate, rememberTemplateLanguage, isInteractiveTerminalFn = originalRun, originalRemember, originalInteractive
	})
	isInteractiveTerminalFn = func() bool { return false }
	remembered := ""
	rememberTemplateLanguage = func(language string) { remembered = language }
	cwd := t.TempDir()
	dest := filepath.Join(cwd, "my-app")
	deploymentError := errors.New("test deployment failure")
	called := false
	runInitializedTemplate = func(ctx context.Context, dir string) error {
		called = true
		if dir != dest {
			t.Fatalf("run prefix = %q, want %q", dir, dest)
		}
		if _, err := os.Stat(filepath.Join(dir, "app.py")); err != nil {
			t.Fatal(err)
		}
		return deploymentError
	}
	err := runTemplateFlow(cwd, dest, "my-app", "fullstack", targetWendyOS, &index.Catalog, initOptions{
		branch: "topic/starters", language: "python", languageSet: true,
		gitInit: "no", gitInitSet: true, run: true,
	})
	if !called || !errors.Is(err, deploymentError) || remembered != "python" {
		t.Fatalf("run=%v remembered=%q error=%v", called, remembered, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "app.py")); err != nil {
		t.Fatal("failed deployment removed the generated app")
	}
}

func TestTemplateGitDefaultDoesNotCreateNestedRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	parent := t.TempDir()
	if err := maybeGitInit(parent, initOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, ".git")); err != nil {
		t.Fatal("default did not initialize Git")
	}
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := maybeGitInit(child, initOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(child, ".git")); !os.IsNotExist(err) {
		t.Fatal("created a nested repository")
	}
}

func TestTemplateRememberedLanguageHonorsExplicitOverride(t *testing.T) {
	original, originalInteractive := preferredTemplateLanguage, isInteractiveTerminalFn
	t.Cleanup(func() { preferredTemplateLanguage, isInteractiveTerminalFn = original, originalInteractive })
	preferredTemplateLanguage = func() string { return "python" }
	isInteractiveTerminalFn = func() bool { return true }
	meta := &repoMeta{
		Templates: []repoMetaTemplate{{Name: "fullstack", Languages: []string{"python", "swift"}}},
		Languages: []repoMetaLanguage{{Key: "python", Name: "Python"}, {Key: "swift", Name: "Swift"}},
	}
	language, err := resolveTemplateLanguage(targetWendyOS, "fullstack", meta, initOptions{})
	if err != nil || language != "python" {
		t.Fatalf("remembered language: %q %v", language, err)
	}
	language, err = resolveTemplateLanguage(targetWendyOS, "fullstack", meta, initOptions{language: "swift", languageSet: true})
	if err != nil || language != "swift" {
		t.Fatalf("explicit override: %q %v", language, err)
	}
}

// Run against locally built publisher output as a cross-repository contract
// check: WENDY_TEMPLATE_BUNDLE_DIR=/path/to/output go test ... -run TestTemplatePublishedBundles.
func TestTemplatePublishedBundles(t *testing.T) {
	dir := os.Getenv("WENDY_TEMPLATE_BUNDLE_DIR")
	if dir == "" {
		t.Skip("set WENDY_TEMPLATE_BUNDLE_DIR to test publisher output")
	}
	data, err := os.ReadFile(filepath.Join(dir, "template-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := parseTemplateIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	for key, bundle := range index.Bundles {
		if !strings.HasSuffix(key, "/fullstack") {
			continue
		}
		t.Run(key, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, bundle.Path))
			if err != nil {
				t.Fatal(err)
			}
			if !validTemplateBundle(bundle) || !verifyTemplateBundle(data, bundle) {
				t.Fatal("invalid published bundle")
			}
			parts := strings.Split(key, "/")
			files, manifest, err := extractTemplateArchive(strings.NewReader(string(data)), parts[0], parts[1])
			if err != nil {
				t.Fatal(err)
			}
			values, err := collectTemplateValues(manifest, "my-app", nil)
			if err != nil {
				t.Fatal(err)
			}
			output := t.TempDir()
			if err := renderAndWriteTemplate(files, output, "my-app", "fullstack", values); err != nil {
				t.Fatal(err)
			}
			config, _ := os.ReadFile(filepath.Join(output, "wendy.json"))
			if !json.Valid(config) {
				t.Fatalf("invalid generated config: %s", config)
			}
		})
	}
}

func TestTemplateArchiveRejectsEscapingPathsAndLinks(t *testing.T) {
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	archive := tar.NewWriter(gz)
	entries := []struct {
		name, body, link string
		kind             byte
	}{
		{"template.json", `{"name":"fullstack"}`, "", tar.TypeReg},
		{"app.py", "safe", "", tar.TypeReg},
		{"../outside.py", "escape", "", tar.TypeReg},
		{"nested/../../outside.py", "escape", "", tar.TypeReg},
		{"/absolute.py", "escape", "", tar.TypeReg},
		{"symlink", "", "../../outside", tar.TypeSymlink},
		{"hardlink", "", "../../outside", tar.TypeLink},
	}
	for _, entry := range entries {
		header := &tar.Header{Name: "templates/python/fullstack/" + entry.name, Typeflag: entry.kind, Linkname: entry.link, Size: int64(len(entry.body)), Mode: 0o644}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	files, _, err := extractTemplateArchive(bytes.NewReader(data.Bytes()), "python", "fullstack")
	if err != nil || len(files) != 1 || string(files["app.py"]) != "safe" {
		t.Fatalf("extracted unsafe entries: %v %v", files, err)
	}
}

func TestTemplateRenderConfinesSymlinksAndRenames(t *testing.T) {
	output, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(output, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, path := range []string{"linked/escape.txt", "../escape.txt", "fullstack/escape.txt"} {
		err := renderAndWriteTemplate(map[string][]byte{path: []byte("escape")}, output, "../outside", "fullstack", nil)
		if err == nil {
			t.Fatalf("accepted escaping path %q", path)
		}
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside project: %v %v", entries, err)
	}
}
