package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// packBuildContext walks cwd and produces an uncompressed tar of the build
// context, applying the same ignore file BuildKit would apply for
// dockerfilePath.
//
// dockerfilePath must be the RESOLVED build file: a Stagefile compile or an
// optimize auto-fix redirects it to Dockerfile.generated, and the
// per-dockerfile ignore file is keyed on that resolved name. Passing the
// original name would select the wrong ignore file, and because the
// Stagefile-derived one is an allowlist, that ships a context missing files the
// build needs — a confusing remote failure rather than merely a slow one.
//
// Entries are emitted in sorted order carrying only mode metadata, so an
// unchanged context packs byte-identically. Chunk dedup is content-addressed,
// so any nondeterminism here (ordering, mtime) would re-send the whole context
// on every build.
func packBuildContext(cwd, dockerfilePath string) ([]byte, error) {
	dockerfileRel, err := filepath.Rel(cwd, dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("locating build file within the context: %w", err)
	}
	dockerfileRel = filepath.ToSlash(dockerfileRel)
	if dockerfileRel == ".." || strings.HasPrefix(dockerfileRel, "../") {
		return nil, fmt.Errorf("build file %q is outside the build context", dockerfilePath)
	}

	matcher, ignoreFileRel, err := loadBuildContextIgnore(cwd, dockerfilePath)
	if err != nil {
		return nil, err
	}
	// Docker always sends the selected build file and ignore file even when the
	// ignore rules exclude them; BuildKit removes them from COPY visibility.
	forceInclude := map[string]bool{dockerfileRel: true}
	if ignoreFileRel != "" {
		forceInclude[ignoreFileRel] = true
	}
	hasForcedDescendant := func(dir string) bool {
		prefix := strings.TrimSuffix(dir, "/") + "/"
		for rel := range forceInclude {
			if strings.HasPrefix(rel, prefix) {
				return true
			}
		}
		return false
	}

	var paths []string
	err = filepath.WalkDir(cwd, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(cwd, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		excluded, matchErr := matcher.MatchesOrParentMatches(rel)
		if matchErr != nil {
			return fmt.Errorf("matching %s against the build ignore file: %w", rel, matchErr)
		}
		if d.IsDir() {
			if excluded {
				// A later negation may re-include a descendant. Walking all ignored
				// directories when negations exist is intentionally conservative:
				// it preserves exact ordered Docker semantics even for wildcard
				// exclusions such as !**/keep.txt.
				if matcher.Exclusions() || hasForcedDescendant(rel) {
					return nil
				}
				return filepath.SkipDir
			}
			paths = append(paths, rel)
			return nil
		}
		if excluded && !forceInclude[rel] {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("remote build contexts do not support non-regular entry %q (%s)", rel, info.Mode().Type())
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking build context: %w", err)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range paths {
		full := filepath.Join(cwd, filepath.FromSlash(rel))
		info, statErr := os.Lstat(full)
		if statErr != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, statErr)
		}
		if info.IsDir() {
			if err := tw.WriteHeader(&tar.Header{
				Name:     rel + "/",
				Mode:     int64(info.Mode().Perm()),
				Typeflag: tar.TypeDir,
			}); err != nil {
				return nil, fmt.Errorf("writing tar header for %s: %w", rel, err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("remote build context entry %q changed to an unsupported type while packing", rel)
		}
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, readErr)
		}
		// No ModTime: mtime is not build input, and letting it vary would defeat
		// chunk dedup for an otherwise unchanged context.
		if err := tw.WriteHeader(&tar.Header{
			Name: rel,
			Mode: int64(info.Mode().Perm()),
			Size: int64(len(data)),
		}); err != nil {
			return nil, fmt.Errorf("writing tar header for %s: %w", rel, err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, fmt.Errorf("writing %s into the context tar: %w", rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing context tar: %w", err)
	}
	return buf.Bytes(), nil
}

// loadBuildContextIgnore parses the exact ignore file BuildKit selects for the
// resolved build file. The fingerprint matcher in deployfastpath.go is
// deliberately conservative and order-independent; using it for transfer
// would be unsafe because under-excluding here sends ignored source and secrets
// to a remote machine.
func loadBuildContextIgnore(cwd, dockerfilePath string) (*patternmatcher.PatternMatcher, string, error) {
	ignorePath := filepath.Join(cwd, ".dockerignore")
	if dockerfilePath != "" {
		perDockerfile := dockerfilePath + ".dockerignore"
		if fi, err := os.Stat(perDockerfile); err == nil && fi.Mode().IsRegular() {
			ignorePath = perDockerfile
		}
	}

	f, err := os.Open(ignorePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, "", fmt.Errorf("opening build ignore file: %w", err)
		}
		matcher, matchErr := patternmatcher.New(nil)
		return matcher, "", matchErr
	}
	defer f.Close()

	patterns, err := ignorefile.ReadAll(f)
	if err != nil {
		return nil, "", fmt.Errorf("reading build ignore file: %w", err)
	}
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, "", fmt.Errorf("parsing build ignore file: %w", err)
	}
	ignoreRel, err := filepath.Rel(cwd, ignorePath)
	if err != nil {
		return nil, "", fmt.Errorf("locating build ignore file within the context: %w", err)
	}
	ignoreRel = filepath.ToSlash(ignoreRel)
	if ignoreRel == ".." || strings.HasPrefix(ignoreRel, "../") {
		return nil, "", fmt.Errorf("build ignore file %q is outside the build context", ignorePath)
	}
	return matcher, ignoreRel, nil
}

// pushBuildContext content-chunks the context tar, asks the build host which
// chunks it is missing, sends only those, and returns the ordered manifest the
// host uses to reassemble the exact bytes. QueryChunks/WriteChunks are a
// generic sha256-addressed store, so a repeat build re-sends only what changed.
func pushBuildContext(ctx context.Context, cs agentpb.WendyContainerServiceClient, tarBytes []byte) (*agentpbv2.ChunkManifest, error) {
	refs, err := chunk.ChunkBytes(tarBytes)
	if err != nil {
		return nil, fmt.Errorf("chunking build context: %w", err)
	}

	hashes := make([][]byte, len(refs))
	for i, r := range refs {
		h := r.Hash
		hashes[i] = h[:]
	}

	missingResp, err := cs.QueryChunks(ctx, &agentpb.QueryChunksRequest{ChunkHashes: hashes})
	if err != nil {
		return nil, fmt.Errorf("querying build-host chunks: %w", err)
	}
	missing := make(map[string]bool, len(missingResp.GetMissingHashes()))
	for _, h := range missingResp.GetMissingHashes() {
		missing[string(h)] = true
	}

	stream, err := cs.WriteChunks(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening chunk stream to the build host: %w", err)
	}
	for i, r := range refs {
		if !missing[string(hashes[i])] {
			continue
		}
		if err := stream.Send(&agentpb.WriteChunksRequest{
			Hash: hashes[i],
			Data: tarBytes[r.Offset : r.Offset+r.Len],
		}); err != nil {
			return nil, fmt.Errorf("sending build-context chunk: %w", err)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return nil, fmt.Errorf("completing build-context transfer: %w", err)
	}

	return &agentpbv2.ChunkManifest{
		ChunkHashes: hashes,
		TotalSize:   int64(len(tarBytes)),
	}, nil
}
