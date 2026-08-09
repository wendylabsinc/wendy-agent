package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	trainingassets "github.com/wendylabsinc/wendy/Training"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// stageManifestName records the SHA-256 (Secure Hash Algorithm, 256 bit) of
// every staged file. Operators compare it against the source tree to prove the
// context a device built is the context they shipped, so the manifest is the
// reason nothing in this file ever edits a staged file after writing it.
const stageManifestName = "stage-manifest.json"

// embeddedTemplatesDir is the directory inside the embedded assets that holds
// the named templates.
const embeddedTemplatesDir = "templates"

// embeddedLibraryDir is the pip-installable wendytrain project inside the
// embedded assets. It stages under the same relative path in the context.
const embeddedLibraryDir = "wendytrain"

// templateSource is where a template's files come from.
type templateSource struct {
	Name     string // as given on the command line
	Embedded bool
	Dir      string // absolute dir for path templates, "" when embedded
	FS       fs.FS  // rooted at the template directory
}

// stageManifestEntry is one file's fingerprint in the stage manifest.
type stageManifestEntry struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// stageManifest is the manifest document itself. Files is a map so that
// encoding/json emits its keys in sorted order, which keeps the manifest
// byte-stable across stagings of an unchanged tree.
type stageManifest struct {
	Created string                        `json:"created"`
	Files   map[string]stageManifestEntry `json:"files"`
}

// stageIgnoredNames are directory or file names dropped from a staged context
// wherever they appear. They are development artefacts: shipping them would
// enlarge the build context and, worse, change the manifest whenever a local
// test run left a cache behind.
var stageIgnoredNames = map[string]bool{
	"tests":         true,
	"__pycache__":   true,
	".pytest_cache": true,
	".venv":         true,
	".git":          true,
}

// stageIgnoredSuffixes are name suffixes dropped for the same reason.
var stageIgnoredSuffixes = []string{".pyc", ".egg-info"}

// stageCopyIgnored reports whether a slash-separated relative path is excluded
// from a staged context. Any path element matching an ignored name or suffix
// excludes the whole subtree, which is what the shutil.ignore_patterns call it
// replaces did per directory level.
func stageCopyIgnored(rel string) bool {
	for _, element := range strings.Split(rel, "/") {
		if element == "" || element == "." {
			continue
		}
		if stageIgnoredNames[element] {
			return true
		}
		for _, suffix := range stageIgnoredSuffixes {
			if strings.HasSuffix(element, suffix) {
				return true
			}
		}
	}
	return false
}

// embeddedTemplateNames lists the templates compiled into the binary, sorted.
func embeddedTemplateNames() []string {
	entries, err := fs.ReadDir(trainingassets.Assets, embeddedTemplatesDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// resolveTemplateSource locates a template and returns its parsed wendy.json.
//
// A bare name (no path separator) is a template compiled into this binary, so a
// released Command Line Interface (CLI) needs no checkout of the repository.
// Anything else is a directory on disk, which is the escape hatch for local
// iteration on a template.
func resolveTemplateSource(nameOrPath string) (templateSource, *appconfig.AppConfig, error) {
	trimmed := strings.TrimSpace(nameOrPath)
	if trimmed == "" {
		return templateSource{}, nil, fmt.Errorf("a template is required; name one of %s, or give a directory containing a wendy.json", strings.Join(embeddedTemplateNames(), ", "))
	}

	src := templateSource{Name: nameOrPath}
	if isBareTemplateName(trimmed) {
		sub, err := fs.Sub(trainingassets.Assets, path.Join(embeddedTemplatesDir, trimmed))
		if err == nil {
			_, err = fs.Stat(sub, "wendy.json")
		}
		if err != nil {
			return templateSource{}, nil, fmt.Errorf("unknown template %q; built-in templates are %s, or give a directory containing a wendy.json", nameOrPath, strings.Join(embeddedTemplateNames(), ", "))
		}
		src.Embedded = true
		src.FS = sub
	} else {
		dir, err := filepath.Abs(trimmed)
		if err != nil {
			return templateSource{}, nil, fmt.Errorf("resolving template path %q: %w", nameOrPath, err)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return templateSource{}, nil, fmt.Errorf("template %q is not a directory; name a built-in template (%s) or give a directory containing a wendy.json", nameOrPath, strings.Join(embeddedTemplateNames(), ", "))
		}
		if _, err := os.Stat(filepath.Join(dir, "wendy.json")); err != nil {
			return templateSource{}, nil, fmt.Errorf("template directory %s has no wendy.json", dir)
		}
		src.Dir = dir
		src.FS = os.DirFS(dir)
	}

	// Read through the FS in both cases: an embedded template has no file on
	// disk to hand to appconfig.LoadFromFile.
	data, err := fs.ReadFile(src.FS, "wendy.json")
	if err != nil {
		return templateSource{}, nil, fmt.Errorf("reading wendy.json for template %q: %w", nameOrPath, err)
	}
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return templateSource{}, nil, fmt.Errorf("parsing wendy.json for template %q: %w", nameOrPath, err)
	}
	if err := cfg.Validate(); err != nil {
		return templateSource{}, nil, fmt.Errorf("template %q has an invalid wendy.json: %w", nameOrPath, err)
	}
	return src, &cfg, nil
}

// isBareTemplateName reports whether the argument names a built-in template
// rather than a directory. Anything carrying a separator, or the relative
// directory shorthands, is a path.
func isBareTemplateName(s string) bool {
	if s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, `/\`)
}

// stageTrainingContext copies a template plus the wendytrain library into a
// self-contained build context and writes the stage manifest.
//
// The CLI rejects a build context that reaches outside the wendy.json
// directory, so a template cannot reference the library where it lives. The
// library, cartpole.py and single_train.py always come from the embedded
// assets even for a path template, so the library a device runs is the one this
// binary was released with. Files the template itself provides are never
// overwritten: a template that ships its own cartpole.py keeps it.
//
// dest is created when empty. The staged directory is returned.
func stageTrainingContext(src templateSource, dest string) (string, error) {
	if src.FS == nil {
		return "", errors.New("template source has no filesystem; resolve it with resolveTemplateSource first")
	}
	if dest == "" {
		created, err := os.MkdirTemp("", "wendy-fleet-train-"+stageDirNameHint(src)+"-")
		if err != nil {
			return "", fmt.Errorf("creating staging directory: %w", err)
		}
		dest = created
	} else if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("creating staging directory %s: %w", dest, err)
	}

	fromTemplate, err := stageTree(src.FS, ".", dest, "", nil)
	if err != nil {
		return "", fmt.Errorf("staging template %q: %w", src.Name, err)
	}

	if _, err := stageTree(trainingassets.Assets, embeddedLibraryDir, dest, embeddedLibraryDir, fromTemplate); err != nil {
		return "", fmt.Errorf("staging the wendytrain library: %w", err)
	}

	// A staged context is flat, so the two modules the templates borrow from
	// the single template are staged at the root under the names the templates
	// import.
	borrowed := []struct {
		needle string
		from   string
		to     string
	}{
		{needle: "cartpole", from: path.Join(embeddedTemplatesDir, "single", "cartpole.py"), to: "cartpole.py"},
		{needle: "single_train", from: path.Join(embeddedTemplatesDir, "single", "train.py"), to: "single_train.py"},
	}
	for _, item := range borrowed {
		referenced, err := referencesModule(src.FS, item.needle)
		if err != nil {
			return "", fmt.Errorf("scanning template %q for %s: %w", src.Name, item.needle, err)
		}
		if !referenced || fromTemplate[item.to] {
			continue
		}
		data, err := trainingassets.Assets.ReadFile(item.from)
		if err != nil {
			return "", fmt.Errorf("reading embedded %s: %w", item.from, err)
		}
		if err := os.WriteFile(filepath.Join(dest, filepath.FromSlash(item.to)), data, 0o644); err != nil {
			return "", fmt.Errorf("staging %s: %w", item.to, err)
		}
	}

	if err := writeStageManifest(dest); err != nil {
		return "", err
	}
	return dest, nil
}

// stageDirNameHint yields a temporary-directory name fragment. os.MkdirTemp
// rejects a pattern containing a separator, and a path template's name is a
// path, so the name is reduced to its last element and sanitized.
func stageDirNameHint(src templateSource) string {
	name := src.Name
	if !src.Embedded {
		name = filepath.Base(filepath.FromSlash(strings.TrimSpace(name)))
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "template"
	}
	return b.String()
}

// stageTree copies root within fsys into dest/prefix, skipping ignored paths
// and any relative path already present in keep. It returns the set of
// destination-relative slash paths it wrote.
func stageTree(fsys fs.FS, root, dest, prefix string, keep map[string]bool) (map[string]bool, error) {
	written := map[string]bool{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// fs.FS paths are always slash separated, so trim rather than reach for
		// filepath, whose separator differs on Windows.
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		}
		if rel == "" || rel == "." {
			return nil
		}
		if prefix != "" {
			rel = path.Join(prefix, rel)
		}
		if stageCopyIgnored(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			// Symlinks and devices have no meaning in a build context that is
			// checksummed and shipped elsewhere.
			return nil
		}
		if keep[rel] {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, stagePerm(d)); err != nil {
			return err
		}
		written[rel] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// stagePerm keeps a source file's executable bit, which build scripts in a
// path template rely on. Embedded files report read-only, so a staged copy
// would be unwritable on a restage; normalize those to a plain writable file.
func stagePerm(d fs.DirEntry) fs.FileMode {
	info, err := d.Info()
	if err != nil {
		return 0o644
	}
	if info.Mode().Perm()&0o111 != 0 && info.Mode().Perm()&0o200 != 0 {
		return 0o755
	}
	return 0o644
}

// referencesModule reports whether a template mentions needle in its
// Dockerfile, Containerfile or any Python file at its root. The templates name
// the modules they borrow in a COPY line or an import, so a substring scan of
// those files is enough and stays honest when a template stops using one.
func referencesModule(fsys fs.FS, needle string) (bool, error) {
	candidates := []string{"Dockerfile", "Containerfile"}
	pyFiles, err := fs.Glob(fsys, "*.py")
	if err != nil {
		return false, err
	}
	sort.Strings(pyFiles)
	candidates = append(candidates, pyFiles...)

	for _, name := range candidates {
		data, err := fs.ReadFile(fsys, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if strings.Contains(string(data), needle) {
			return true, nil
		}
	}
	return false, nil
}

// writeStageManifest records every staged file's SHA-256 and size so an
// operator can prove the context a device built matches the source tree. The
// manifest excludes itself, since it cannot contain its own digest.
func writeStageManifest(dir string) error {
	files := map[string]stageManifestEntry{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == stageManifestName {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[rel] = stageManifestEntry{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
		return nil
	})
	if err != nil {
		return fmt.Errorf("checksumming the staged context: %w", err)
	}

	manifest := stageManifest{
		Created: time.Now().UTC().Format(time.RFC3339),
		Files:   files,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", stageManifestName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, stageManifestName), append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", stageManifestName, err)
	}
	return nil
}

// applyLANHostNetworking rewrites every network entitlement, top level and per
// service, to host mode on the loaded configuration.
//
// It never touches the staged wendy.json: a rewritten file on disk would break
// the stage manifest's promise that a staged context matches its source. The
// deploy path carries the amended configuration in memory instead.
func applyLANHostNetworking(appCfg *appconfig.AppConfig) error {
	if appCfg == nil {
		return errors.New("no app config to rewrite for the lan transport")
	}
	rewritten := rewriteNetworkEntitlementsToHost(appCfg.Entitlements)
	for _, svc := range appCfg.Services {
		if svc == nil {
			continue
		}
		rewritten += rewriteNetworkEntitlementsToHost(svc.Entitlements)
	}
	if rewritten == 0 {
		return fmt.Errorf("transport lan needs a network entitlement to rewrite, but %s declares none; add {\"type\": \"network\", \"mode\": \"host\"} to its wendy.json, or use the mesh transport", appCfg.AppID)
	}
	return nil
}

// rewriteNetworkEntitlementsToHost replaces network entitlements in place and
// returns how many it replaced. Replacing the whole entry rather than only the
// mode drops the mesh serviceCIDR and port mappings, which host mode rejects.
// Every non-network entitlement is left exactly as it was.
func rewriteNetworkEntitlementsToHost(entitlements []appconfig.Entitlement) int {
	count := 0
	for i, e := range entitlements {
		if e.Type != appconfig.EntitlementNetwork {
			continue
		}
		entitlements[i] = appconfig.Entitlement{
			Type: appconfig.EntitlementNetwork,
			Mode: "host",
		}
		count++
	}
	return count
}
