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
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

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
