package services

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
)

const (
	defaultFileSyncRoot          = "/var/lib/wendy/files"
	fileSyncMaxChunkSize         = 4 * 1024 * 1024
	fileSyncMaxInflightFiles     = 128
	fileSyncMaxManifestFiles     = 100_000
	fileSyncMaxManifestTotalSize = 100 * 1024 * 1024 * 1024
)

var fileSyncRoot = defaultFileSyncRoot

// FileSyncAppDir returns the agent-managed directory for one app's synced
// deployment files. The appID must be validated before it is used as a host
// filesystem path component.
func FileSyncAppDir(appID string) (string, error) {
	if err := appconfig.ValidateAppID(appID); err != nil {
		return "", err
	}
	return filepath.Join(fileSyncRoot, appID), nil
}

// FileSyncService implements the v1 WendyFileSyncService. Files are stored in
// an agent-managed, app-scoped directory and are later mounted into containers
// by the container runtime; the service never mounts CLI host paths directly.
type FileSyncService struct {
	agentpb.UnimplementedWendyFileSyncServiceServer
	logger *zap.Logger
}

func NewFileSyncService(logger *zap.Logger) *FileSyncService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FileSyncService{logger: logger}
}

type incomingFile struct {
	path     string
	tmpPath  string
	file     *os.File
	hash     hash.Hash
	size     int64
	sequence uint64
}

func (s *FileSyncService) SyncFiles(stream agentpb.WendyFileSyncService_SyncFilesServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return fmt.Errorf("expected FileSyncStart, got %T", first.GetRequestType())
	}

	appDir, err := FileSyncAppDir(start.GetAppId())
	if err != nil {
		return fmt.Errorf("invalid appId: %w", err)
	}
	appID := start.GetAppId()
	localManifest := start.GetManifest()
	manifestByPath := make(map[string]*agentpb.FileSyncEntry, len(localManifest.GetFiles()))
	var manifestTotalSize int64
	if len(localManifest.GetFiles()) > fileSyncMaxManifestFiles {
		return fmt.Errorf("manifest has %d files, max %d", len(localManifest.GetFiles()), fileSyncMaxManifestFiles)
	}
	for _, e := range localManifest.GetFiles() {
		cleaned, err := validateFileSyncPath(e.GetPath())
		if err != nil {
			return fmt.Errorf("invalid manifest path %q: %w", e.GetPath(), err)
		}
		if cleaned != e.GetPath() {
			return fmt.Errorf("manifest path %q must be clean", e.GetPath())
		}
		if _, ok := manifestByPath[cleaned]; ok {
			return fmt.Errorf("duplicate manifest path %q", cleaned)
		}
		if e.GetSize() < 0 {
			return fmt.Errorf("manifest path %q has negative size", cleaned)
		}
		manifestTotalSize += e.GetSize()
		if manifestTotalSize > fileSyncMaxManifestTotalSize {
			return fmt.Errorf("manifest total size exceeds %d bytes", fileSyncMaxManifestTotalSize)
		}
		manifestByPath[cleaned] = e
	}

	manifest, err := buildAgentFileSyncManifest(appDir, start.GetChecksum() || manifestHasChecksums(localManifest))
	if err != nil {
		return fmt.Errorf("building agent manifest: %w", err)
	}
	if err := stream.Send(&agentpb.FileSyncResponse{
		ResponseType: &agentpb.FileSyncResponse_Manifest{Manifest: manifest},
	}); err != nil {
		return err
	}

	incoming := make(map[string]*incomingFile)
	var totalReceived int64
	defer cleanupIncoming(incoming)

	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.Send(&agentpb.FileSyncResponse{
				ResponseType: &agentpb.FileSyncResponse_Complete{Complete: &agentpb.FileSyncComplete{}},
			})
		}
		if err != nil {
			return err
		}

		switch r := req.GetRequestType().(type) {
		case *agentpb.FileSyncRequest_Chunk:
			if err := receiveFileSyncChunk(appDir, incoming, manifestByPath, manifestTotalSize, &totalReceived, r.Chunk); err != nil {
				return err
			}
		case *agentpb.FileSyncRequest_Commit:
			if err := commitFileSyncFile(appDir, incoming, manifestByPath, r.Commit); err != nil {
				return err
			}
			s.logger.Info("file sync committed file",
				zap.String("app_id", appID),
				zap.String("path", r.Commit.GetPath()),
				zap.Int64("size", r.Commit.GetSize()),
				zap.String("sha256_prefix", sha256Prefix(r.Commit.GetSha256())),
			)
			if err := stream.Send(&agentpb.FileSyncResponse{
				ResponseType: &agentpb.FileSyncResponse_Ack{Ack: &agentpb.FileSyncAck{Path: r.Commit.GetPath()}},
			}); err != nil {
				return err
			}
		case *agentpb.FileSyncRequest_Chmod:
			if err := chmodFileSyncFile(appDir, r.Chmod); err != nil {
				return err
			}
			s.logger.Info("file sync updated file mode",
				zap.String("app_id", appID),
				zap.String("path", r.Chmod.GetPath()),
				zap.Uint32("mode", r.Chmod.GetMode()&0o777),
			)
			if err := stream.Send(&agentpb.FileSyncResponse{
				ResponseType: &agentpb.FileSyncResponse_Ack{Ack: &agentpb.FileSyncAck{Path: r.Chmod.GetPath()}},
			}); err != nil {
				return err
			}
		case *agentpb.FileSyncRequest_Delete:
			if err := deleteFileSyncPaths(appDir, r.Delete.GetPaths()); err != nil {
				return err
			}
			for _, p := range r.Delete.GetPaths() {
				s.logger.Info("file sync deleted stale file",
					zap.String("app_id", appID),
					zap.String("path", p),
				)
			}
		default:
			return fmt.Errorf("unexpected file sync request %T", req.GetRequestType())
		}
	}
}

func manifestHasChecksums(manifest *agentpb.FileSyncManifest) bool {
	for _, e := range manifest.GetFiles() {
		if len(e.GetSha256()) > 0 {
			return true
		}
	}
	return false
}

func cleanupIncoming(files map[string]*incomingFile) {
	for _, f := range files {
		if f.file != nil {
			_ = f.file.Close()
		}
		if f.tmpPath != "" {
			_ = os.Remove(f.tmpPath)
		}
	}
}

func receiveFileSyncChunk(appDir string, incoming map[string]*incomingFile, manifest map[string]*agentpb.FileSyncEntry, manifestTotalSize int64, totalReceived *int64, chunk *agentpb.FileSyncChunk) error {
	if chunk == nil {
		return fmt.Errorf("nil FileSyncChunk")
	}
	cleaned, err := validateFileSyncPath(chunk.GetPath())
	if err != nil {
		return fmt.Errorf("invalid chunk path %q: %w", chunk.GetPath(), err)
	}
	entry := manifest[cleaned]
	if entry == nil {
		return fmt.Errorf("chunk for %s not present in manifest", cleaned)
	}
	if len(chunk.GetData()) > fileSyncMaxChunkSize {
		return fmt.Errorf("chunk for %s is %d bytes, max %d", cleaned, len(chunk.GetData()), fileSyncMaxChunkSize)
	}
	if chunk.GetCumulativeSize() > entry.GetSize() {
		return fmt.Errorf("cumulative size for %s exceeds manifest size: got %d, want at most %d", cleaned, chunk.GetCumulativeSize(), entry.GetSize())
	}
	if totalReceived != nil && *totalReceived+int64(len(chunk.GetData())) > manifestTotalSize {
		return fmt.Errorf("streamed file sync bytes exceed manifest total size: got %d, want at most %d", *totalReceived+int64(len(chunk.GetData())), manifestTotalSize)
	}

	state := incoming[cleaned]
	if state == nil {
		if chunk.GetSequence() != 0 {
			return fmt.Errorf("first chunk for %s has sequence %d", cleaned, chunk.GetSequence())
		}
		if len(incoming) >= fileSyncMaxInflightFiles {
			return fmt.Errorf("too many inflight file sync files: max %d", fileSyncMaxInflightFiles)
		}
		if err := os.MkdirAll(appDir, 0o700); err != nil {
			return fmt.Errorf("creating file sync dir: %w", err)
		}
		tmp, err := os.CreateTemp(appDir, ".sync-*")
		if err != nil {
			return fmt.Errorf("creating temp file for %s: %w", cleaned, err)
		}
		state = &incomingFile{path: cleaned, tmpPath: tmp.Name(), file: tmp, hash: sha256.New()}
		incoming[cleaned] = state
	} else if chunk.GetSequence() != state.sequence {
		return fmt.Errorf("chunk sequence mismatch for %s: got %d, want %d", cleaned, chunk.GetSequence(), state.sequence)
	}

	data := chunk.GetData()
	if len(data) > 0 {
		if _, err := state.file.Write(data); err != nil {
			return fmt.Errorf("writing chunk for %s: %w", cleaned, err)
		}
		if _, err := state.hash.Write(data); err != nil {
			return err
		}
	}
	state.size += int64(len(data))
	if totalReceived != nil {
		*totalReceived += int64(len(data))
	}
	if state.size != chunk.GetCumulativeSize() {
		return fmt.Errorf("cumulative size mismatch for %s: got %d, streamed %d", cleaned, chunk.GetCumulativeSize(), state.size)
	}
	if got := state.hash.Sum(nil); !bytes.Equal(got, chunk.GetSha256()) {
		return fmt.Errorf("cumulative hash mismatch for %s", cleaned)
	}
	state.sequence++
	return nil
}

func commitFileSyncFile(appDir string, incoming map[string]*incomingFile, manifest map[string]*agentpb.FileSyncEntry, commit *agentpb.FileSyncCommit) error {
	if commit == nil {
		return fmt.Errorf("nil FileSyncCommit")
	}
	cleaned, err := validateFileSyncPath(commit.GetPath())
	if err != nil {
		return fmt.Errorf("invalid commit path %q: %w", commit.GetPath(), err)
	}
	state := incoming[cleaned]
	if state == nil {
		return fmt.Errorf("commit for %s without chunks", cleaned)
	}
	entry := manifest[cleaned]
	if entry == nil {
		return fmt.Errorf("commit for %s not present in manifest", cleaned)
	}
	if state.size != commit.GetSize() || state.size != entry.GetSize() {
		return fmt.Errorf("size mismatch for %s", cleaned)
	}
	got := state.hash.Sum(nil)
	if !bytes.Equal(got, commit.GetSha256()) {
		return fmt.Errorf("hash mismatch for %s", cleaned)
	}
	if len(entry.GetSha256()) > 0 && !bytes.Equal(got, entry.GetSha256()) {
		return fmt.Errorf("hash mismatch for %s", cleaned)
	}
	if err := state.file.Close(); err != nil {
		return fmt.Errorf("closing temp file for %s: %w", cleaned, err)
	}
	state.file = nil

	finalPath, err := safeFileSyncJoin(appDir, cleaned)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return fmt.Errorf("creating parent for %s: %w", cleaned, err)
	}
	mode := fs.FileMode(entry.GetMode() & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	if err := os.Chmod(state.tmpPath, mode); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", cleaned, err)
	}
	if err := os.Rename(state.tmpPath, finalPath); err != nil {
		return fmt.Errorf("installing %s: %w", cleaned, err)
	}
	if commit.MtimeUnixNano != nil {
		mtime := time.Unix(0, commit.GetMtimeUnixNano())
		if err := os.Chtimes(finalPath, mtime, mtime); err != nil {
			return fmt.Errorf("setting mtime for %s: %w", cleaned, err)
		}
	}
	state.tmpPath = ""
	delete(incoming, cleaned)
	return nil
}

func chmodFileSyncFile(appDir string, chmod *agentpb.FileSyncChmod) error {
	if chmod == nil {
		return fmt.Errorf("nil FileSyncChmod")
	}
	cleaned, err := validateFileSyncPath(chmod.GetPath())
	if err != nil {
		return fmt.Errorf("invalid chmod path %q: %w", chmod.GetPath(), err)
	}
	finalPath, err := safeFileSyncJoin(appDir, cleaned)
	if err != nil {
		return err
	}
	if err := os.Chmod(finalPath, fs.FileMode(chmod.GetMode()&0o777)); err != nil {
		return fmt.Errorf("chmod %s: %w", cleaned, err)
	}
	return nil
}

func deleteFileSyncPaths(appDir string, paths []string) error {
	for _, p := range paths {
		cleaned, err := validateFileSyncPath(p)
		if err != nil {
			return fmt.Errorf("invalid delete path %q: %w", p, err)
		}
		fullPath, err := safeFileSyncJoin(appDir, cleaned)
		if err != nil {
			return err
		}
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", cleaned, err)
		}
		pruneEmptyFileSyncDirs(appDir, filepath.Dir(fullPath))
	}
	return nil
}

func buildAgentFileSyncManifest(appDir string, includeHashes bool) (*agentpb.FileSyncManifest, error) {
	var entries []*agentpb.FileSyncEntry
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return &agentpb.FileSyncManifest{}, nil
	} else if err != nil {
		return nil, err
	}

	err := fs.WalkDir(os.DirFS(appDir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(path.Base(filepath.ToSlash(p)), ".sync-") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		var sum []byte
		if includeHashes {
			absPath := filepath.Join(appDir, p)
			var err error
			sum, err = hashFileSHA256(absPath)
			if err != nil {
				return err
			}
		}
		mtime := info.ModTime().UnixNano()
		entries = append(entries, &agentpb.FileSyncEntry{
			Path:          filepath.ToSlash(p),
			Size:          info.Size(),
			Sha256:        sum,
			Mode:          uint32(info.Mode().Perm()),
			MtimeUnixNano: &mtime,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &agentpb.FileSyncManifest{Files: entries}, nil
}

func sha256Prefix(sum []byte) string {
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return hex.EncodeToString(sum)
}

func hashFileSHA256(filename string) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func validateFileSyncPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("path must be relative")
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "", fmt.Errorf("path must refer to a file")
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == ".." {
			return "", fmt.Errorf("path must not contain '..' components")
		}
	}
	return cleaned, nil
}

func safeFileSyncJoin(base, rel string) (string, error) {
	cleaned, err := validateFileSyncPath(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, filepath.FromSlash(cleaned))
	relToBase, err := filepath.Rel(base, full)
	if err != nil {
		return "", err
	}
	if relToBase == "." || strings.HasPrefix(relToBase, "..") || filepath.IsAbs(relToBase) {
		return "", fmt.Errorf("path escapes file sync root")
	}
	return full, nil
}

func pruneEmptyFileSyncDirs(appDir, dir string) {
	for {
		rel, err := filepath.Rel(appDir, dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
