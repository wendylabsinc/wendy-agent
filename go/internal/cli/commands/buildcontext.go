package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
	ignore := loadDockerIgnoreForBuild(cwd, dockerfilePath)

	dockerfileRel, err := filepath.Rel(cwd, dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("locating build file within the context: %w", err)
	}
	dockerfileRel = filepath.ToSlash(dockerfileRel)

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
		if d.IsDir() {
			if ignore.matches(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// The build file itself always ships: an allowlist-style ignore would
		// otherwise exclude the one file the build cannot run without.
		if rel != dockerfileRel && ignore.matches(rel) {
			return nil
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
		info, statErr := os.Stat(full)
		if statErr != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, statErr)
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
