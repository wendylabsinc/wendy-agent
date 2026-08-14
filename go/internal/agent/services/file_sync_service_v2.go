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

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// sha256Len is the byte length of every digest on the wire. Sizes are checked
// explicitly rather than trusted, so a truncated digest fails as a bad request
// instead of silently comparing equal to a truncated computed digest.
const sha256Len = 32

// fileSyncTempPrefix marks in-progress transfers. It matches the prefix the
// macOS agent uses so both agents produce the same on-disk shape, and manifest
// walks skip it: a partially transferred file must never be advertised as
// present, or a client would diff against a file that does not exist yet.
const fileSyncTempPrefix = ".WENDY-"

// fileSyncDiskReserve is the free space the agent refuses to consume. A device
// that fills its root partition cannot apply agent or OS updates, so a push is
// rejected while there is still room to recover rather than after the disk is
// full.
const fileSyncDiskReserve = 64 << 20 // 64 MiB

// FileSyncServiceV2 syncs files into a device's persistent volumes.
//
// The protocol is the one the CLI already speaks for macOS-target deploys:
// the client opens with a manifest, the agent replies with its own, and the
// client sends only what differs. This implementation serves persistent
// volumes (/var/lib/wendy/volumes/<name>) rather than app sync roots, which is
// what makes it possible to put a large artifact — a model, a calibration
// file, a map — on a running device without rebuilding its image.
type FileSyncServiceV2 struct {
	agentpbv2.UnimplementedWendyFileSyncServiceServer
	logger *zap.Logger

	// Seams (overridden in tests).
	volumesDir string
	availBytes func(path string) (int64, bool)
}

// FileSyncOption adjusts the service at construction.
type FileSyncOption func(*FileSyncServiceV2)

// WithVolumesDir overrides the directory persistent volumes live in. Exported
// so tests outside this package — the CLI's end-to-end sync tests, which drive
// the real client against the real service — can point it at a temp directory.
func WithVolumesDir(dir string) FileSyncOption {
	return func(s *FileSyncServiceV2) { s.volumesDir = dir }
}

// NewFileSyncServiceV2 builds the service against the real volumes directory.
func NewFileSyncServiceV2(logger *zap.Logger, opts ...FileSyncOption) *FileSyncServiceV2 {
	svc := &FileSyncServiceV2{
		logger:     logger,
		volumesDir: volumesDir,
		availBytes: availableBytes,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// syncSession is the per-stream state. It exists so the deferred cleanup of an
// interrupted transfer has one owner: every error path must remove the temp
// file, and a client that disconnects mid-file is the common case, not the
// exceptional one.
type syncSession struct {
	svc *FileSyncServiceV2
	// volumeRoot is the volume directory: the containment base for every path
	// in the session, and the path free space is measured against. It always
	// exists, unlike the prefix subtree, which a push may be creating.
	volumeRoot string
	// prefix is the validated subtree paths are relative to, slash-separated
	// and possibly empty.
	prefix string
	// manifestByPath is what the client declared it intends to send. Every
	// chunk, commit and chmod is checked against it, so the session can never
	// be talked into writing a file the opening manifest did not describe.
	manifestByPath map[string]*agentpbv2.FileSyncEntry
	finalized      map[string]bool
	active         *fileTransfer
}

type fileTransfer struct {
	path        string
	entry       *agentpbv2.FileSyncEntry
	destination string
	tempPath    string
	file        *os.File
	hasher      hash.Hash
	received    int64
	nextSeq     uint64
}

// SyncFiles serves one sync session. The first message selects the sync root;
// everything after it is interpreted relative to that root.
func (s *FileSyncServiceV2) SyncFiles(stream grpc.BidiStreamingServer[agentpbv2.FileSyncRequest, agentpbv2.FileSyncResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "stream closed before FileSyncStart")
		}
		return err
	}
	start, ok := first.GetRequestType().(*agentpbv2.FileSyncRequest_Start)
	if !ok {
		return status.Errorf(codes.InvalidArgument, "first message must be FileSyncStart, got %T", first.GetRequestType())
	}

	volumeRoot, prefix, err := s.resolveRoot(start.Start)
	if err != nil {
		return err
	}

	session := &syncSession{
		svc:        s,
		volumeRoot: volumeRoot,
		prefix:     prefix,
		finalized:  make(map[string]bool),
	}
	defer session.cleanupActive()

	session.manifestByPath, err = session.buildManifestLookup(start.Start.GetManifest().GetFiles())
	if err != nil {
		return err
	}

	// Reply with what is already here so the client can send only what differs.
	agentManifest, err := buildAgentManifest(session.scanRoot())
	if err != nil {
		return err
	}
	if err := stream.Send(&agentpbv2.FileSyncResponse{
		ResponseType: &agentpbv2.FileSyncResponse_Manifest{
			Manifest: &agentpbv2.FileSyncManifest{Files: agentManifest},
		},
	}); err != nil {
		return err
	}

	s.logger.Info("FileSyncStart",
		zap.String("volume", start.Start.GetVolume()),
		zap.String("path_prefix", start.Start.GetPathPrefix()),
		zap.Int("client_files", len(start.Start.GetManifest().GetFiles())),
		zap.Int("agent_files", len(agentManifest)),
	)

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := session.handle(msg, stream); err != nil {
			return err
		}
	}

	// A stream that ends mid-file is an error, not a silent partial write: the
	// deferred cleanup removes the temp file and the client learns the push
	// did not land.
	if session.active != nil {
		return status.Errorf(codes.InvalidArgument, "stream ended without commit for %q", session.active.path)
	}

	return stream.Send(&agentpbv2.FileSyncResponse{
		ResponseType: &agentpbv2.FileSyncResponse_Complete{Complete: &agentpbv2.FileSyncComplete{}},
	})
}

// resolveRoot maps a FileSyncStart onto the volume directory it addresses and
// the validated path prefix within it.
//
// The volume root is the only containment base the session ever uses. Resolving
// paths against the prefix directory instead would let a symlink planted at the
// prefix redefine what "inside" means, which is precisely the escape the
// containment check exists to stop.
func (s *FileSyncServiceV2) resolveRoot(start *agentpbv2.FileSyncStart) (volumeRoot, prefix string, err error) {
	volume := start.GetVolume()
	switch {
	case volume == "" && start.GetAppId() != "":
		// App sync roots are the macOS agent's native-app deploy path. On
		// Linux apps run as containers and their writable state is a volume,
		// so there is no app root to sync into.
		return "", "", status.Error(codes.Unimplemented,
			"this agent syncs into persistent volumes; set FileSyncStart.volume (app_id sync roots are macOS-only)")
	case volume == "":
		return "", "", status.Error(codes.InvalidArgument, "FileSyncStart.volume is required")
	case start.GetAppId() != "":
		return "", "", status.Error(codes.InvalidArgument, "FileSyncStart.volume and app_id are mutually exclusive")
	}

	// A volume name is a single directory name, never a path.
	if volume != filepath.Base(volume) || volume == "." || volume == ".." || strings.ContainsRune(volume, os.PathSeparator) {
		return "", "", status.Errorf(codes.InvalidArgument, "invalid volume name %q", volume)
	}

	volumeRoot = filepath.Join(s.volumesDir, volume)
	info, err := os.Stat(volumeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// Deliberately not created on demand. Volumes come into existence
			// when an app declares a persist entitlement; auto-creating one
			// here would make a typo look like a successful push into a
			// directory no app will ever read.
			return "", "", status.Errorf(codes.NotFound,
				"volume %q does not exist on this device (volumes are created by an app's persist entitlement)", volume)
		}
		return "", "", status.Errorf(codes.Internal, "stat volume %q: %v", volume, err)
	}
	if !info.IsDir() {
		return "", "", status.Errorf(codes.FailedPrecondition, "volume %q is not a directory", volume)
	}

	prefix = start.GetPathPrefix()
	if prefix == "" {
		return volumeRoot, "", nil
	}
	// Validate the prefix now — including symlink resolution — so the manifest
	// walk cannot be pointed at a directory outside the volume. The directory
	// itself is deliberately not created: a session that only reads the
	// manifest (a listing) must not leave a directory behind on the device.
	if _, err := validatedDestination(volumeRoot, prefix); err != nil {
		return "", "", err
	}
	return volumeRoot, filepath.ToSlash(prefix), nil
}

func (s *syncSession) handle(msg *agentpbv2.FileSyncRequest, stream grpc.BidiStreamingServer[agentpbv2.FileSyncRequest, agentpbv2.FileSyncResponse]) error {
	switch m := msg.GetRequestType().(type) {
	case *agentpbv2.FileSyncRequest_Chunk:
		return s.handleChunk(m.Chunk)
	case *agentpbv2.FileSyncRequest_Commit:
		return s.handleCommit(m.Commit, stream)
	case *agentpbv2.FileSyncRequest_Chmod:
		return s.handleChmod(m.Chmod, stream)
	case *agentpbv2.FileSyncRequest_Delete:
		return s.handleDelete(m.Delete)
	case *agentpbv2.FileSyncRequest_Start:
		return status.Error(codes.InvalidArgument, "duplicate FileSyncStart in stream")
	default:
		return status.Errorf(codes.InvalidArgument, "unexpected message %T", msg.GetRequestType())
	}
}

func (s *syncSession) handleChunk(chunk *agentpbv2.FileSyncChunk) error {
	entry, err := s.manifestEntry(chunk.GetPath())
	if err != nil {
		return err
	}
	if s.finalized[chunk.GetPath()] {
		return status.Errorf(codes.InvalidArgument, "path already finalized: %q", chunk.GetPath())
	}
	if len(chunk.GetSha256()) != sha256Len {
		return status.Errorf(codes.InvalidArgument, "chunk sha256 must be %d bytes for %q", sha256Len, chunk.GetPath())
	}
	if len(chunk.GetData()) == 0 && entry.GetSize() > 0 {
		return status.Errorf(codes.InvalidArgument, "zero-length chunk for non-empty file %q", chunk.GetPath())
	}

	if s.active == nil {
		if err := s.beginTransfer(chunk.GetPath(), entry); err != nil {
			return err
		}
	} else if s.active.path != chunk.GetPath() {
		// Interleaving files would mean several open temp files and no clear
		// owner for cleanup; the client sends one file at a time.
		return status.Errorf(codes.InvalidArgument,
			"cannot switch from %q to %q mid-transfer", s.active.path, chunk.GetPath())
	}

	t := s.active
	firstEmptyChunk := t.received == 0 && t.entry.GetSize() == 0 && t.nextSeq == 0
	if t.received >= t.entry.GetSize() && !firstEmptyChunk {
		return status.Errorf(codes.InvalidArgument, "extra chunk after declared size for %q", chunk.GetPath())
	}
	if chunk.GetSequence() != t.nextSeq {
		return status.Errorf(codes.InvalidArgument,
			"unexpected chunk sequence for %q: want %d, got %d", chunk.GetPath(), t.nextSeq, chunk.GetSequence())
	}
	size := t.received + int64(len(chunk.GetData()))
	if size > t.entry.GetSize() {
		return status.Errorf(codes.InvalidArgument,
			"chunk for %q exceeds declared size %d", chunk.GetPath(), t.entry.GetSize())
	}

	// Hash before writing so a corrupt chunk never reaches the disk. The
	// running digest is verified per chunk, which is what turns a silent
	// truncation on a flaky link into a loud failure.
	t.hasher.Write(chunk.GetData())
	if size != chunk.GetCumulativeSize() {
		return status.Errorf(codes.DataLoss,
			"cumulative size mismatch for %q: want %d, got %d", chunk.GetPath(), size, chunk.GetCumulativeSize())
	}
	if !bytes.Equal(t.hasher.Sum(nil), chunk.GetSha256()) {
		return status.Errorf(codes.DataLoss, "chunk sha256 mismatch for %q", chunk.GetPath())
	}

	if _, err := t.file.Write(chunk.GetData()); err != nil {
		return status.Errorf(codes.Internal, "writing %q: %v", chunk.GetPath(), err)
	}
	t.received = size
	t.nextSeq++
	return nil
}

// beginTransfer opens the temp file for a new file transfer, after checking
// there is room for it.
func (s *syncSession) beginTransfer(path string, entry *agentpbv2.FileSyncEntry) error {
	destination, err := s.resolve(path)
	if err != nil {
		return err
	}
	if err := s.svc.checkSpace(s.volumeRoot, entry.GetSize()); err != nil {
		return err
	}
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return status.Errorf(codes.Internal, "creating directory for %q: %v", path, err)
	}
	// The temp file sits next to its destination so the commit is a rename
	// within one filesystem, and carries the target digest so a crashed
	// transfer is identifiable rather than anonymous garbage.
	temp := filepath.Join(dir, fileSyncTempPrefix+hex.EncodeToString(entry.GetSha256())+"~"+filepath.Base(destination))
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return status.Errorf(codes.Internal, "creating temp file for %q: %v", path, err)
	}
	s.active = &fileTransfer{
		path:        path,
		entry:       entry,
		destination: destination,
		tempPath:    temp,
		file:        f,
		hasher:      sha256.New(),
	}
	return nil
}

func (s *syncSession) handleCommit(commit *agentpbv2.FileSyncCommit, stream grpc.BidiStreamingServer[agentpbv2.FileSyncRequest, agentpbv2.FileSyncResponse]) error {
	t := s.active
	if t == nil {
		return status.Errorf(codes.InvalidArgument, "no active transfer to commit for %q", commit.GetPath())
	}
	if t.path != commit.GetPath() {
		return status.Errorf(codes.InvalidArgument,
			"commit path %q does not match active transfer %q", commit.GetPath(), t.path)
	}
	if len(commit.GetSha256()) != sha256Len {
		return status.Errorf(codes.InvalidArgument, "commit sha256 must be %d bytes for %q", sha256Len, commit.GetPath())
	}
	if commit.GetSize() != t.entry.GetSize() {
		return status.Errorf(codes.InvalidArgument,
			"commit size mismatch for %q: manifest %d, commit %d", commit.GetPath(), t.entry.GetSize(), commit.GetSize())
	}
	if !bytes.Equal(commit.GetSha256(), t.entry.GetSha256()) {
		return status.Errorf(codes.InvalidArgument, "commit sha256 does not match manifest for %q", commit.GetPath())
	}
	if t.received != t.entry.GetSize() {
		return status.Errorf(codes.DataLoss,
			"size mismatch for %q: want %d, got %d", commit.GetPath(), t.entry.GetSize(), t.received)
	}
	if !bytes.Equal(t.hasher.Sum(nil), t.entry.GetSha256()) {
		return status.Errorf(codes.DataLoss, "sha256 mismatch for %q", commit.GetPath())
	}

	if err := s.finishTransfer(t); err != nil {
		return err
	}
	s.active = nil
	s.finalized[commit.GetPath()] = true

	s.svc.logger.Info("file committed",
		zap.String("path", commit.GetPath()),
		zap.Int64("size", commit.GetSize()),
	)
	return stream.Send(&agentpbv2.FileSyncResponse{
		ResponseType: &agentpbv2.FileSyncResponse_Ack{Ack: &agentpbv2.FileSyncAck{Path: commit.GetPath()}},
	})
}

// finishTransfer makes the transferred bytes visible at their destination,
// atomically. An app that watches the volume must never observe a partial
// file: it either sees the old bytes or the complete new ones.
func (s *syncSession) finishTransfer(t *fileTransfer) error {
	if err := t.file.Sync(); err != nil {
		return status.Errorf(codes.Internal, "fsync %q: %v", t.path, err)
	}
	if err := t.file.Close(); err != nil {
		return status.Errorf(codes.Internal, "closing %q: %v", t.path, err)
	}
	t.file = nil

	if err := os.Chmod(t.tempPath, fileMode(t.entry.GetMode())); err != nil {
		return status.Errorf(codes.Internal, "chmod %q: %v", t.path, err)
	}
	if err := os.Rename(t.tempPath, t.destination); err != nil {
		return status.Errorf(codes.Internal, "renaming %q into place: %v", t.path, err)
	}
	if err := fsyncDir(filepath.Dir(t.destination)); err != nil {
		return status.Errorf(codes.Internal, "%v", err)
	}
	return nil
}

func (s *syncSession) handleChmod(chmod *agentpbv2.FileSyncChmod, stream grpc.BidiStreamingServer[agentpbv2.FileSyncRequest, agentpbv2.FileSyncResponse]) error {
	if s.active != nil {
		return status.Error(codes.InvalidArgument, "cannot change mode while a transfer is active")
	}
	entry, err := s.manifestEntry(chmod.GetPath())
	if err != nil {
		return err
	}
	if s.finalized[chmod.GetPath()] {
		return status.Errorf(codes.InvalidArgument, "path already finalized: %q", chmod.GetPath())
	}
	// A mode-only update is the client asserting the content is already
	// correct. Verifying size and digest against the manifest keeps that
	// assertion honest rather than letting it silently apply to other bytes.
	if chmod.GetSize() != entry.GetSize() || !bytes.Equal(chmod.GetSha256(), entry.GetSha256()) || chmod.GetMode() != entry.GetMode() {
		return status.Errorf(codes.InvalidArgument, "mode update does not match manifest for %q", chmod.GetPath())
	}
	destination, err := s.resolve(chmod.GetPath())
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err != nil {
		if os.IsNotExist(err) {
			return status.Errorf(codes.NotFound, "cannot change mode, %q does not exist", chmod.GetPath())
		}
		return status.Errorf(codes.Internal, "stat %q: %v", chmod.GetPath(), err)
	}
	if err := os.Chmod(destination, fileMode(chmod.GetMode())); err != nil {
		return status.Errorf(codes.Internal, "chmod %q: %v", chmod.GetPath(), err)
	}
	s.finalized[chmod.GetPath()] = true
	return stream.Send(&agentpbv2.FileSyncResponse{
		ResponseType: &agentpbv2.FileSyncResponse_Ack{Ack: &agentpbv2.FileSyncAck{Path: chmod.GetPath()}},
	})
}

func (s *syncSession) handleDelete(del *agentpbv2.FileSyncDelete) error {
	if s.active != nil {
		return status.Error(codes.InvalidArgument, "cannot delete while a transfer is active")
	}
	seen := make(map[string]bool, len(del.GetPaths()))
	for _, p := range del.GetPaths() {
		if seen[p] {
			return status.Errorf(codes.InvalidArgument, "duplicate delete path %q", p)
		}
		seen[p] = true
		if s.finalized[p] {
			return status.Errorf(codes.InvalidArgument, "path already finalized: %q", p)
		}
		destination, err := s.resolve(p)
		if err != nil {
			return err
		}
		// Only regular files: a delete must not be able to remove a whole
		// subtree of a volume, which is what `volumes remove` is for.
		info, err := os.Lstat(destination)
		if err != nil {
			if os.IsNotExist(err) {
				s.finalized[p] = true
				continue
			}
			return status.Errorf(codes.Internal, "stat %q: %v", p, err)
		}
		if info.IsDir() {
			return status.Errorf(codes.InvalidArgument, "%q is a directory", p)
		}
		if err := os.Remove(destination); err != nil {
			return status.Errorf(codes.Internal, "removing %q: %v", p, err)
		}
		if err := fsyncDir(filepath.Dir(destination)); err != nil {
			return status.Errorf(codes.Internal, "%v", err)
		}
		s.finalized[p] = true
		s.svc.logger.Info("file deleted", zap.String("path", p))
	}
	return nil
}

// resolve maps a session-relative path onto an absolute destination, checked
// for containment inside the volume root.
func (s *syncSession) resolve(rel string) (string, error) {
	if s.prefix == "" {
		return validatedDestination(s.volumeRoot, rel)
	}
	// Validate the client's component on its own first, so a traversal is
	// reported as such instead of being silently absorbed by the join.
	if _, err := validatedDestination(s.volumeRoot, rel); err != nil {
		return "", err
	}
	return validatedDestination(s.volumeRoot, path.Join(s.prefix, rel))
}

// scanRoot is the directory the agent manifest describes.
func (s *syncSession) scanRoot() string {
	if s.prefix == "" {
		return s.volumeRoot
	}
	return filepath.Join(s.volumeRoot, filepath.FromSlash(s.prefix))
}

func (s *syncSession) manifestEntry(path string) (*agentpbv2.FileSyncEntry, error) {
	entry, ok := s.manifestByPath[path]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "no manifest entry for %q", path)
	}
	return entry, nil
}

// buildManifestLookup indexes the opening manifest, rejecting anything that
// could not be written safely before a single byte is accepted.
func (s *syncSession) buildManifestLookup(entries []*agentpbv2.FileSyncEntry) (map[string]*agentpbv2.FileSyncEntry, error) {
	lookup := make(map[string]*agentpbv2.FileSyncEntry, len(entries))
	for _, e := range entries {
		if _, err := s.resolve(e.GetPath()); err != nil {
			return nil, err
		}
		if len(e.GetSha256()) != sha256Len {
			return nil, status.Errorf(codes.InvalidArgument, "manifest sha256 must be %d bytes for %q", sha256Len, e.GetPath())
		}
		if e.GetSize() < 0 {
			return nil, status.Errorf(codes.InvalidArgument, "negative size for %q", e.GetPath())
		}
		if lookup[e.GetPath()] != nil {
			return nil, status.Errorf(codes.InvalidArgument, "duplicate manifest entry for %q", e.GetPath())
		}
		lookup[e.GetPath()] = e
	}
	return lookup, nil
}

func (s *syncSession) cleanupActive() {
	if s.active == nil {
		return
	}
	if s.active.file != nil {
		_ = s.active.file.Close()
	}
	_ = os.Remove(s.active.tempPath)
	s.active = nil
}

// checkSpace refuses a transfer that would leave the filesystem too full to
// recover. When free space cannot be determined the transfer proceeds: a
// missing statfs is not a reason to make the device unusable for pushes.
func (s *FileSyncServiceV2) checkSpace(root string, size int64) error {
	avail, ok := s.availBytes(root)
	if !ok {
		return nil
	}
	if avail-size < fileSyncDiskReserve {
		return status.Errorf(codes.ResourceExhausted,
			"not enough space: %d bytes free, need %d plus a %d byte reserve",
			avail, size, int64(fileSyncDiskReserve))
	}
	return nil
}

// buildAgentManifest walks root and describes every regular file in it.
func buildAgentManifest(root string) ([]*agentpbv2.FileSyncEntry, error) {
	var entries []*agentpbv2.FileSyncEntry

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks and devices: the manifest describes bytes this service
		// owns, and following a link out of the volume would report a file the
		// client could then be told to overwrite.
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), fileSyncTempPrefix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		digest, size, err := hashFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, &agentpbv2.FileSyncEntry{
			Path:   filepath.ToSlash(rel),
			Size:   size,
			Sha256: digest,
			Mode:   uint32(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, status.Errorf(codes.Internal, "building manifest: %v", err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	return entries, nil
}

func hashFile(path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil, 0, err
	}
	return h.Sum(nil), size, nil
}

// validatedDestination resolves a client-supplied relative path against root
// and guarantees the result stays inside it.
//
// Containment is checked after resolving symlinks on the deepest existing
// ancestor, not just lexically: a volume is writable by the app that owns it,
// so a link planted inside the volume pointing at /etc is a path an attacker
// can actually create, and a purely lexical check would follow it.
func validatedDestination(root, rel string) (string, error) {
	if rel == "" {
		return "", status.Error(codes.InvalidArgument, "path must not be empty")
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", status.Errorf(codes.InvalidArgument, "path must be relative: %q", rel)
	}
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		switch component {
		case "", ".", "..":
			return "", status.Errorf(codes.InvalidArgument, "path must not contain empty, . or .. components: %q", rel)
		}
	}

	destination := filepath.Join(root, filepath.FromSlash(rel))

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", status.Errorf(codes.Internal, "resolving sync root: %v", err)
	}

	// EvalSymlinks needs an existing path, and the destination usually does not
	// exist yet. Walk up to the deepest ancestor that does and check that.
	ancestor := destination
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", status.Errorf(codes.Internal, "resolving path %q: %v", rel, err)
	}
	if resolvedAncestor != resolvedRoot && !strings.HasPrefix(resolvedAncestor, resolvedRoot+string(os.PathSeparator)) {
		return "", status.Errorf(codes.InvalidArgument, "path escapes the sync root: %q", rel)
	}
	return destination, nil
}

// fileMode maps a manifest mode onto permission bits, defaulting to 0644 for
// clients that leave it unset. Only permission bits are honoured — setuid and
// friends are never applied to a file arriving over the wire.
func fileMode(mode uint32) os.FileMode {
	perm := os.FileMode(mode).Perm()
	if perm == 0 {
		return 0o644
	}
	return perm
}

// fsyncDir flushes a directory entry so a rename or unlink survives power
// loss. Edge devices lose power without warning, which is exactly when a
// half-applied update is worst.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return fmt.Errorf("fsync dir: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close dir after fsync: %w", closeErr)
	}
	return nil
}
