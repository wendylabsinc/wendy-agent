package commands

// Native app-layer builds for Stagefile projects: when an iteration changes
// only `copy: from: local` files, the CLI rebuilds those COPY layers itself —
// deterministically, without Docker — and splices them into the persistent
// OCI layout directory the chunk-diff deploy already maintains. Everything
// here mirrors what BuildKit's COPY would produce semantically (same files,
// same modes, root:root ownership) but NOT byte-identically: native layers
// zero the mtimes, so the same input files always produce the same diff ID on
// any machine.

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// nativeDepsHash covers every build input EXCEPT the final stage's local copy
// files: the generated Dockerfile bytes, the Stagefile lockfile, every
// install-referenced file (pip requirements, package.json + lockfile), local
// copies feeding NON-final stages, the platform, and the sorted build args.
// Any change here means the deps layers may differ → the buildx path must run.
func nativeDepsHash(cwd, dockerfile, platform string, buildArgs map[string]string, sf *spec.File) (string, error) {
	h := sha256.New()
	io.WriteString(h, "wendy-native-deps-v1\n")
	io.WriteString(h, "platform="+platform+"\n")

	keys := make([]string, 0, len(buildArgs))
	for k := range buildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		io.WriteString(h, "arg "+k+"="+buildArgs[k]+"\n")
	}

	dfPath, err := confinedDockerfilePath(cwd, dockerfile)
	if err != nil {
		return "", err
	}
	dfData, err := os.ReadFile(dfPath)
	if err != nil {
		return "", fmt.Errorf("reading dockerfile for deps hash: %w", err)
	}
	fmt.Fprintf(h, "dockerfile %d\n", len(dfData))
	h.Write(dfData)

	// The lockfile pins base-image digests; absent is a valid state (hashed as
	// absent — creating it later changes the hash, correctly).
	if lockData, err := os.ReadFile(filepath.Join(cwd, stagefileLockName)); err == nil {
		fmt.Fprintf(h, "lock %d\n", len(lockData))
		h.Write(lockData)
	}

	for _, p := range nativeDepsPaths(sf) {
		if err := hashContextPath(h, cwd, p); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// nativeDepsPaths lists the local paths that feed deps layers: every stage's
// install-referenced files plus local copies of NON-final stages. The final
// stage's local copies are the app inputs and are deliberately excluded.
func nativeDepsPaths(sf *spec.File) []string {
	if len(sf.Stages) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	lastIdx := len(sf.Stages) - 1
	for i := range sf.Stages {
		s := &sf.Stages[i]
		if s.Install != nil {
			if s.Install.Pip != nil {
				add(s.Install.Pip.Requirements)
			}
			if s.Install.Npm != nil {
				add("package.json")
				add(spec.NpmLockfile(s.Install.Npm.Manager))
			}
		}
		if i == lastIdx {
			continue
		}
		for _, c := range s.Copy {
			if c.From != "local" {
				continue
			}
			for _, p := range c.Paths {
				add(p)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

// hashContextPath streams the content of a file (or a sorted walk of a
// directory) rooted at rel into h. A missing input is an error — the caller
// treats it as "not natively buildable" and falls back to buildx.
func hashContextPath(h io.Writer, cwd, rel string) error {
	abs := filepath.Join(cwd, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("deps input %q: %w", rel, err)
	}
	hashOne := func(name, file string) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("deps input %q: %w", name, err)
		}
		fmt.Fprintf(h, "file %s %d\n", name, len(data))
		h.Write(data)
		return nil
	}
	if !fi.IsDir() {
		return hashOne(rel, abs)
	}
	var files []string
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("deps input %q: %w", rel, err)
	}
	sort.Strings(files)
	for _, f := range files {
		relName, err := filepath.Rel(cwd, f)
		if err != nil {
			return err
		}
		if err := hashOne(filepath.ToSlash(relName), f); err != nil {
			return err
		}
	}
	return nil
}

// nativeStateName is the bookkeeping file inside the layout dir recording what
// the native path last built. It sits at the dir root, out of GC's reach
// (GC only touches blobs/sha256).
const nativeStateName = "state.json"

// nativeState records the native path's ground truth: the deps-input hash the
// current layout was built against, the manifest we last wrote (or adopted),
// and OUR app-layer digests in copy-entry order. A later run may go native
// only when all three still match reality.
type nativeState struct {
	DepsHash        string   `json:"deps_hash"`
	ManifestDigest  string   `json:"manifest_digest"`
	AppLayerDigests []string `json:"app_layer_digests"`
}

// loadNativeState returns the recorded state, or (nil, false) on any miss or
// malformation — never an error, since the fallback (buildx) is always valid.
func loadNativeState(dir string) (*nativeState, bool) {
	data, err := os.ReadFile(filepath.Join(dir, nativeStateName))
	if err != nil {
		return nil, false
	}
	var s nativeState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false
	}
	if s.DepsHash == "" || s.ManifestDigest == "" || len(s.AppLayerDigests) == 0 {
		return nil, false
	}
	return &s, true
}

// saveNativeState persists s atomically (temp + rename, 0600).
func saveNativeState(dir string, s nativeState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, nativeStateName))
}

// nativeLayerEpoch is the fixed modification time stamped on every entry of a
// native layer. Zeroed times are what make the layer a pure function of the
// input file contents (BuildKit's COPY preserves source mtimes, which defeats
// cross-machine and post-checkout dedup).
var nativeLayerEpoch = time.Unix(0, 0).UTC()

// nativeLayer is one deterministically-built COPY layer, gzip-compressed.
type nativeLayer struct {
	Blob      []byte   // gzip-compressed tar
	Digest    string   // "sha256:<hex>" of Blob (the manifest/blob digest)
	DiffID    string   // "sha256:<hex>" of the uncompressed tar
	FileNames []string // sorted tar entry names (dirs carry a trailing "/")
}

// nativeTarEntry is one pending tar entry, keyed by its (slash-form, no
// leading slash) tar path in the entries map.
type nativeTarEntry struct {
	hdr  tar.Header
	data []byte
}

// buildNativeCopyLayer materializes one Stagefile `copy` entry (from: local)
// as an OCI gzip layer with Docker COPY semantics:
//
//   - dest defaults to Paths[0]; a multi-source copy treats dest as a
//     directory (mirroring codegen's trailing-slash fix-up)
//   - a relative dest resolves against workDir (the image config's
//     WorkingDir; "/" when empty), exactly as COPY does
//   - a directory source copies its CONTENTS into dest
//   - modes are preserved, ownership is numeric root:root, parent directories
//     are synthesized 0755, symlinks stay symlinks
//
// Entries are emitted in sorted path order with fixed mtimes, so identical
// input files yield a byte-identical blob.
func buildNativeCopyLayer(cwd string, entry spec.CopyEntry, workDir string) (*nativeLayer, error) {
	if len(entry.Paths) == 0 {
		return nil, fmt.Errorf("copy entry has no paths")
	}
	dest := entry.Dest
	if dest == "" {
		dest = entry.Paths[0]
	}
	// Mirrors codegen.copyLines: multiple sources make the dest a directory.
	destIsDir := strings.HasSuffix(dest, "/") || len(entry.Paths) > 1
	if !path.IsAbs(dest) {
		wd := workDir
		if wd == "" {
			wd = "/"
		}
		if !path.IsAbs(wd) {
			wd = "/" + wd
		}
		dest = path.Join(wd, dest)
	} else {
		dest = path.Clean(dest)
	}

	entries := map[string]*nativeTarEntry{}

	// tarName converts an absolute in-image path to its tar entry name.
	tarName := func(p string) string {
		return strings.TrimPrefix(path.Clean(p), "/")
	}

	addDirChain := func(imgPath string) {
		// Synthesize each missing ancestor of imgPath as a 0755 root dir.
		clean := tarName(imgPath)
		if clean == "" || clean == "." {
			return
		}
		segs := strings.Split(clean, "/")
		for i := 1; i < len(segs); i++ {
			name := strings.Join(segs[:i], "/") + "/"
			if _, ok := entries[name]; ok {
				continue
			}
			entries[name] = &nativeTarEntry{hdr: tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name,
				Mode:     0o755,
				ModTime:  nativeLayerEpoch,
			}}
		}
	}

	addDir := func(imgPath string, mode os.FileMode) {
		name := tarName(imgPath)
		if name == "" || name == "." {
			return
		}
		addDirChain(imgPath)
		entries[name+"/"] = &nativeTarEntry{hdr: tar.Header{
			Typeflag: tar.TypeDir,
			Name:     name + "/",
			Mode:     int64(mode.Perm()),
			ModTime:  nativeLayerEpoch,
		}}
	}

	addFile := func(absSrc, imgPath string, mode os.FileMode) error {
		data, err := os.ReadFile(absSrc)
		if err != nil {
			return err
		}
		addDirChain(imgPath)
		name := tarName(imgPath)
		entries[name] = &nativeTarEntry{
			hdr: tar.Header{
				Typeflag: tar.TypeReg,
				Name:     name,
				Mode:     int64(mode.Perm()),
				Size:     int64(len(data)),
				ModTime:  nativeLayerEpoch,
			},
			data: data,
		}
		return nil
	}

	addSymlink := func(absSrc, imgPath string) error {
		target, err := os.Readlink(absSrc)
		if err != nil {
			return err
		}
		addDirChain(imgPath)
		name := tarName(imgPath)
		entries[name] = &nativeTarEntry{hdr: tar.Header{
			Typeflag: tar.TypeSymlink,
			Name:     name,
			Linkname: target,
			Mode:     0o777,
			ModTime:  nativeLayerEpoch,
		}}
		return nil
	}

	for _, src := range entry.Paths {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(src)))
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("copy path %q escapes the project directory", src)
		}
		abs := filepath.Join(cwd, filepath.FromSlash(rel))
		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("copy source %q: %w", src, err)
		}

		switch {
		case fi.IsDir():
			// Docker COPY copies a directory source's CONTENTS into dest.
			addDir(dest, fi.Mode())
			err := filepath.WalkDir(abs, func(p string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				childRel, err := filepath.Rel(abs, p)
				if err != nil {
					return err
				}
				if childRel == "." {
					return nil
				}
				imgPath := path.Join(dest, filepath.ToSlash(childRel))
				info, err := d.Info()
				if err != nil {
					return err
				}
				switch {
				case d.IsDir():
					addDir(imgPath, info.Mode())
					return nil
				case info.Mode()&os.ModeSymlink != 0:
					return addSymlink(p, imgPath)
				case info.Mode().IsRegular():
					return addFile(p, imgPath, info.Mode())
				default:
					return fmt.Errorf("copy source %q: unsupported file type %v", p, info.Mode())
				}
			})
			if err != nil {
				return nil, err
			}

		case fi.Mode()&os.ModeSymlink != 0:
			imgPath := dest
			if destIsDir {
				imgPath = path.Join(dest, path.Base(rel))
			}
			if err := addSymlink(abs, imgPath); err != nil {
				return nil, err
			}

		case fi.Mode().IsRegular():
			imgPath := dest
			if destIsDir {
				imgPath = path.Join(dest, path.Base(rel))
			}
			if err := addFile(abs, imgPath, fi.Mode()); err != nil {
				return nil, err
			}

		default:
			return nil, fmt.Errorf("copy source %q: unsupported file type %v", src, fi.Mode())
		}
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var rawTar bytes.Buffer
	tw := tar.NewWriter(&rawTar)
	for _, name := range names {
		e := entries[name]
		if err := tw.WriteHeader(&e.hdr); err != nil {
			return nil, fmt.Errorf("writing tar header %q: %w", name, err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				return nil, fmt.Errorf("writing tar entry %q: %w", name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("finishing layer tar: %w", err)
	}

	var blob bytes.Buffer
	gw, err := gzip.NewWriterLevel(&blob, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(rawTar.Bytes()); err != nil {
		return nil, fmt.Errorf("compressing layer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("compressing layer: %w", err)
	}

	rawSum := sha256.Sum256(rawTar.Bytes())
	blobSum := sha256.Sum256(blob.Bytes())
	return &nativeLayer{
		Blob:      blob.Bytes(),
		Digest:    "sha256:" + hex.EncodeToString(blobSum[:]),
		DiffID:    "sha256:" + hex.EncodeToString(rawSum[:]),
		FileNames: names,
	}, nil
}
