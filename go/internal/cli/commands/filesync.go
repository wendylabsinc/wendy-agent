package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"golang.org/x/term"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fileSyncEntry pairs an absolute local path with its effective remote destination.
//   - If localPath is a regular file, remotePath is the full agent-relative path.
//   - If localPath is a directory, remotePath is the agent-relative prefix (may be empty).
type fileSyncEntry struct {
	localPath  string // absolute path on the developer's machine (file or dir)
	remotePath string // agent-relative path (full path for file; prefix for dir)
}

type fileSyncOptions struct {
	checksum bool
}

const (
	fileSyncChunkSize             = 4 * 1024 * 1024
	fileSyncChecksumEnv           = "WENDY_FILE_SYNC_CHECKSUM"
	fileSyncQuickCheckFeatureName = "file-sync-quick-check"
)

// buildLocalManifest walks root (a directory) and returns a FileSyncEntry for
// every regular file found: path relative to root, cheap metadata, and
// optionally SHA256 bytes. Symlinks and non-regular files are skipped.
func buildLocalManifest(root string, includeHash bool) ([]*agentpb.FileSyncEntry, error) {
	var entries []*agentpb.FileSyncEntry

	err := fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		entry := fileSyncManifestEntry(path, info)
		if includeHash {
			absPath := filepath.Join(root, path)
			sum, err := hashLocalFileSHA256(absPath)
			if err != nil {
				return err
			}
			entry.Sha256 = sum
		}

		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func fileSyncManifestEntry(path string, info fs.FileInfo) *agentpb.FileSyncEntry {
	mtime := info.ModTime().UnixNano()
	return &agentpb.FileSyncEntry{
		Path:          path,
		Size:          info.Size(),
		Mode:          uint32(info.Mode().Perm()),
		MtimeUnixNano: &mtime,
	}
}

func hashLocalFileSHA256(filename string) ([]byte, error) {
	h := sha256.New()
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filename, err)
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("hashing %s: %w", filename, err)
	}
	return append([]byte(nil), h.Sum(nil)...), nil
}

type modeOnlyChange struct {
	path    string
	oldMode uint32
	newMode uint32
	entry   *agentpb.FileSyncEntry
}

type manifestDiff struct {
	contentTransfers []string
	modeOnly         []modeOnlyChange
	staleRemote      []string
	quickSkipped     int
}

// diffManifests classifies files into content transfers, mode-only changes,
// quick-check skips, and stale remote files. In the default mode it follows
// rsync's normal quick check: path + size + mtime + mode. In checksum mode, or
// when talking to an older agent that lacks mtime metadata, it falls back to
// SHA256 comparison.
func diffManifests(local, remote *agentpb.FileSyncManifest, checksum bool) manifestDiff {
	remoteByPath := make(map[string]*agentpb.FileSyncEntry, len(remote.GetFiles()))
	for _, e := range remote.GetFiles() {
		remoteByPath[e.Path] = e
	}

	localPaths := make(map[string]struct{}, len(local.GetFiles()))
	var diff manifestDiff
	for _, e := range local.GetFiles() {
		localPaths[e.Path] = struct{}{}
		remoteEntry, ok := remoteByPath[e.Path]
		if !ok || remoteEntry == nil {
			diff.contentTransfers = append(diff.contentTransfers, e.Path)
			continue
		}

		sameContent := false
		usedQuickCheck := false
		if !checksum && hasFileSyncMtime(e) && hasFileSyncMtime(remoteEntry) {
			usedQuickCheck = true
			sameContent = remoteEntry.Size == e.Size && remoteEntry.GetMtimeUnixNano() == e.GetMtimeUnixNano()
		} else {
			sameContent = remoteEntry.Size == e.Size && len(remoteEntry.Sha256) == sha256.Size && len(e.Sha256) == sha256.Size && bytes.Equal(remoteEntry.Sha256, e.Sha256)
		}

		sameMode := remoteEntry.Mode == e.Mode
		switch {
		case !sameContent:
			diff.contentTransfers = append(diff.contentTransfers, e.Path)
		case !sameMode:
			diff.modeOnly = append(diff.modeOnly, modeOnlyChange{
				path:    e.Path,
				oldMode: remoteEntry.Mode,
				newMode: e.Mode,
				entry:   e,
			})
		case usedQuickCheck:
			diff.quickSkipped++
		}
	}

	for _, e := range remote.GetFiles() {
		if _, ok := localPaths[e.Path]; !ok {
			diff.staleRemote = append(diff.staleRemote, e.Path)
		}
	}

	sort.Strings(diff.contentTransfers)
	sort.Slice(diff.modeOnly, func(i, j int) bool {
		return diff.modeOnly[i].path < diff.modeOnly[j].path
	})
	sort.Strings(diff.staleRemote)
	return diff
}

func hasFileSyncMtime(e *agentpb.FileSyncEntry) bool {
	return e != nil && e.MtimeUnixNano != nil
}

func sendFileSyncRequest(stream agentpb.WendyFileSyncService_SyncFilesClient, req *agentpb.FileSyncRequest) error {
	if err := stream.Send(req); err != nil {
		return explainFileSyncSendError(stream, err)
	}
	return nil
}

func explainFileSyncSendError(stream agentpb.WendyFileSyncService_SyncFilesClient, sendErr error) error {
	if !errors.Is(sendErr, io.EOF) {
		return sendErr
	}

	resp, recvErr := stream.Recv()
	if recvErr != nil {
		if errors.Is(recvErr, io.EOF) {
			return fmt.Errorf("stream closed by agent without error details")
		}
		return fmt.Errorf("stream closed by agent: %w", recvErr)
	}
	if resp == nil {
		return fmt.Errorf("stream closed by agent after empty response")
	}
	return fmt.Errorf("stream closed by agent after unexpected %T response", resp.GetResponseType())
}

// syncFiles drives a complete SyncFiles session:
//  1. Builds the combined local manifest from all entries.
//  2. Exchanges it with the agent (agent replies with its own manifest).
//  3. Diffs the two manifests.
//  4. Transfers only what changed, streaming in 4 MiB chunks.
//  5. Waits for FileSyncComplete.
//
// Progress is printed to stdout when there is something to transfer.
func syncFiles(
	ctx context.Context,
	conn *grpcclient.AgentConnection,
	appID string,
	entries []fileSyncEntry,
	opts ...fileSyncOptions,
) error {
	checksumMode := fileSyncChecksumMode()
	if len(opts) > 0 && opts[0].checksum {
		checksumMode = true
	}
	quickCheck := !checksumMode && fileSyncAgentSupportsQuickCheck(ctx, conn)
	includeHashes := checksumMode || !quickCheck

	// Build the combined local manifest and a map from agent-relative path → local abs path.
	manifestStart := time.Now()
	localManifest, localFiles, err := buildCombinedManifest(entries, includeHashes)
	if err != nil {
		return fmt.Errorf("building local manifest: %w", err)
	}
	manifestDuration := time.Since(manifestStart)
	hashedFiles := countManifestHashes(localManifest)

	// Open bidi stream.
	stream, err := conn.FileSyncService.SyncFiles(ctx)
	if err != nil {
		return fmt.Errorf("opening SyncFiles stream: %w", err)
	}

	// Send FileSyncStart with the local manifest.
	if err := sendFileSyncRequest(stream, &agentpb.FileSyncRequest{
		RequestType: &agentpb.FileSyncRequest_Start{
			Start: &agentpb.FileSyncStart{
				AppId:    appID,
				Manifest: localManifest,
				Checksum: checksumMode,
			},
		},
	}); err != nil {
		return fmt.Errorf("sending FileSyncStart: %w", err)
	}

	// Receive FileSyncManifest from agent.
	resp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving agent manifest: %w", err)
	}
	agentManifestMsg, ok := resp.ResponseType.(*agentpb.FileSyncResponse_Manifest)
	if !ok {
		return fmt.Errorf("expected FileSyncManifest, got %T", resp.ResponseType)
	}

	// Compute diff.
	diffStart := time.Now()
	diff := diffManifests(localManifest, agentManifestMsg.Manifest, checksumMode)
	diffDuration := time.Since(diffStart)

	if len(diff.contentTransfers) > 0 && !includeHashes {
		newHashes, err := ensureLocalHashes(localManifest, localFiles, diff.contentTransfers)
		if err != nil {
			return fmt.Errorf("hashing changed files: %w", err)
		}
		hashedFiles += newHashes
	}

	if len(diff.contentTransfers) == 0 && len(diff.modeOnly) == 0 && len(diff.staleRemote) == 0 {
		if err := stream.CloseSend(); err != nil {
			return fmt.Errorf("closing stream: %w", err)
		}
		resp, err := stream.Recv()
		if err != nil && err != io.EOF {
			return fmt.Errorf("receiving complete: %w", err)
		}
		if err == nil {
			if _, ok := resp.ResponseType.(*agentpb.FileSyncResponse_Complete); !ok {
				return fmt.Errorf("expected FileSyncComplete, got %T", resp.ResponseType)
			}
		}
		if len(localManifest.GetFiles()) > 0 || len(agentManifestMsg.Manifest.GetFiles()) > 0 {
			printFileSyncSummary(localManifest, manifestDuration, diffDuration, diff.quickSkipped, hashedFiles, 0)
			cliLogln("Files up to date.")
		}
		return nil
	}

	// Compute total bytes to transfer for progress display.
	localByPath := make(map[string]*agentpb.FileSyncEntry, len(localManifest.GetFiles()))
	for _, e := range localManifest.GetFiles() {
		localByPath[e.Path] = e
	}
	var totalBytes int64
	for _, path := range diff.contentTransfers {
		if e, ok := localByPath[path]; ok {
			totalBytes += e.Size
		}
	}

	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	fileCount := len(diff.contentTransfers)
	fileIdx := 0
	var sentBytes int64
	transferStart := time.Now()

	if fileCount > 0 {
		cliLogln("Syncing files...")
	}

	// Transfer each file.
	for _, agentPath := range diff.contentTransfers {
		localPath, ok := localFiles[agentPath]
		if !ok {
			return fmt.Errorf("no local path for %q", agentPath)
		}

		entry, ok := localByPath[agentPath]
		if !ok {
			return fmt.Errorf("no manifest entry for %q", agentPath)
		}

		f, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("opening %s: %w", localPath, err)
		}

		h := sha256.New()
		buf := make([]byte, fileSyncChunkSize)
		var fileSent int64
		var sequence uint64
		fileDisplayName := agentPath

		sendChunk := func(data []byte) error {
			if _, err := h.Write(data); err != nil {
				return err
			}
			fileSent += int64(len(data))
			sentBytes += int64(len(data))
			return sendFileSyncRequest(stream, &agentpb.FileSyncRequest{
				RequestType: &agentpb.FileSyncRequest_Chunk{
					Chunk: &agentpb.FileSyncChunk{
						Path:           agentPath,
						Data:           data,
						Sequence:       sequence,
						CumulativeSize: fileSent,
						Sha256:         h.Sum(nil),
					},
				},
			})
		}

		if entry.Size == 0 {
			if err := sendChunk(nil); err != nil {
				f.Close()
				return fmt.Errorf("sending empty chunk for %s: %w", agentPath, err)
			}
		} else {
			for {
				n, readErr := f.Read(buf)
				if n > 0 {
					if err := sendChunk(buf[:n]); err != nil {
						f.Close()
						return fmt.Errorf("sending chunk for %s: %w", agentPath, err)
					}
					printFileSyncProgress(isTTY, fileDisplayName, fileSent, entry.Size,
						sentBytes, time.Since(transferStart), fileIdx+1, fileCount)
					sequence++
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					f.Close()
					return fmt.Errorf("reading %s: %w", localPath, readErr)
				}
			}
		}
		f.Close()

		streamingHash := h.Sum(nil)
		if fileSent != entry.Size {
			return fmt.Errorf("file %q changed during transfer (manifest size %d, streamed %d)",
				agentPath, entry.Size, fileSent)
		}
		if !bytes.Equal(streamingHash, entry.Sha256) {
			return fmt.Errorf("file %q changed during transfer", agentPath)
		}

		if err := sendFileSyncRequest(stream, &agentpb.FileSyncRequest{
			RequestType: &agentpb.FileSyncRequest_Commit{
				Commit: &agentpb.FileSyncCommit{
					Path:          agentPath,
					Sha256:        entry.Sha256,
					Size:          entry.Size,
					MtimeUnixNano: entry.MtimeUnixNano,
				},
			},
		}); err != nil {
			return fmt.Errorf("sending commit for %s: %w", agentPath, err)
		}

		resp, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("receiving ack for %s: %w", agentPath, err)
		}
		ack, ok := resp.ResponseType.(*agentpb.FileSyncResponse_Ack)
		if !ok {
			return fmt.Errorf("expected FileSyncAck for %s, got %T", agentPath, resp.ResponseType)
		}
		if ack.Ack.Path != agentPath {
			return fmt.Errorf("ack path mismatch: expected %q, got %q", agentPath, ack.Ack.Path)
		}

		fileIdx++
		if isTTY && entry.Size > 0 {
			fmt.Print("\n")
		}
	}

	for _, change := range diff.modeOnly {
		cliLogln("mode changed: %s %04o -> %04o", change.path, change.oldMode, change.newMode)
		if err := sendFileSyncRequest(stream, &agentpb.FileSyncRequest{
			RequestType: &agentpb.FileSyncRequest_Chmod{
				Chmod: &agentpb.FileSyncChmod{
					Path:          change.path,
					Mode:          change.entry.Mode,
					Size:          change.entry.Size,
					Sha256:        change.entry.Sha256,
					MtimeUnixNano: change.entry.MtimeUnixNano,
				},
			},
		}); err != nil {
			return fmt.Errorf("sending mode update for %s: %w", change.path, err)
		}

		resp, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("receiving ack for %s: %w", change.path, err)
		}
		ack, ok := resp.ResponseType.(*agentpb.FileSyncResponse_Ack)
		if !ok {
			return fmt.Errorf("expected FileSyncAck for %s, got %T", change.path, resp.ResponseType)
		}
		if ack.Ack.Path != change.path {
			return fmt.Errorf("ack path mismatch: expected %q, got %q", change.path, ack.Ack.Path)
		}
	}

	if len(diff.staleRemote) > 0 {
		for _, path := range diff.staleRemote {
			cliLogln("deleted: %s", path)
		}
		if err := sendFileSyncRequest(stream, &agentpb.FileSyncRequest{
			RequestType: &agentpb.FileSyncRequest_Delete{
				Delete: &agentpb.FileSyncDelete{Paths: append([]string(nil), diff.staleRemote...)},
			},
		}); err != nil {
			return fmt.Errorf("sending delete request: %w", err)
		}
	}

	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing stream: %w", err)
	}

	resp, err = stream.Recv()
	if err != nil && err != io.EOF {
		return fmt.Errorf("receiving complete: %w", err)
	}
	if err == nil {
		if _, ok := resp.ResponseType.(*agentpb.FileSyncResponse_Complete); !ok {
			return fmt.Errorf("expected FileSyncComplete, got %T", resp.ResponseType)
		}
	}

	if fileCount > 0 {
		cliLogln("Total: %s in %d file(s)", humanize.Bytes(uint64(totalBytes)), fileCount)
	}
	if len(localManifest.GetFiles()) > 0 || len(agentManifestMsg.Manifest.GetFiles()) > 0 {
		printFileSyncSummary(localManifest, manifestDuration, diffDuration, diff.quickSkipped, hashedFiles, sentBytes)
	}
	return nil
}

// buildCombinedManifest assembles the local manifest from all fileSyncEntry values
// and returns:
//   - the combined FileSyncManifest for the FileSyncStart message
//   - a map from agent-relative path → absolute local path (for chunk transfer)
func buildCombinedManifest(entries []fileSyncEntry, includeHash bool) (*agentpb.FileSyncManifest, map[string]string, error) {
	var files []*agentpb.FileSyncEntry
	localFiles := make(map[string]string)

	for _, e := range entries {
		info, err := os.Stat(e.localPath)
		if err != nil {
			return nil, nil, fmt.Errorf("stat %s: %w", e.localPath, err)
		}

		if !info.IsDir() {
			// Single file: compute the entry directly.
			agentPath := e.remotePath
			entry := fileSyncManifestEntry(agentPath, info)
			if includeHash {
				sum, err := hashLocalFileSHA256(e.localPath)
				if err != nil {
					return nil, nil, err
				}
				entry.Sha256 = sum
			}

			files = append(files, entry)
			localFiles[agentPath] = e.localPath
		} else {
			// Directory: walk and prefix paths.
			subEntries, err := buildLocalManifest(e.localPath, includeHash)
			if err != nil {
				return nil, nil, fmt.Errorf("building manifest for %s: %w", e.localPath, err)
			}
			for _, se := range subEntries {
				relPath := se.Path
				var agentPath string
				if e.remotePath != "" {
					agentPath = e.remotePath + "/" + relPath
				} else {
					agentPath = relPath
				}
				se.Path = agentPath
				files = append(files, se)
				localFiles[agentPath] = filepath.Join(e.localPath, relPath)
			}
		}
	}

	return &agentpb.FileSyncManifest{Files: files}, localFiles, nil
}

func ensureLocalHashes(manifest *agentpb.FileSyncManifest, localFiles map[string]string, paths []string) (int, error) {
	entries := make(map[string]*agentpb.FileSyncEntry, len(manifest.GetFiles()))
	for _, e := range manifest.GetFiles() {
		entries[e.GetPath()] = e
	}

	var hashed int
	for _, p := range paths {
		entry := entries[p]
		if entry == nil || len(entry.Sha256) == sha256.Size {
			continue
		}
		localPath, ok := localFiles[p]
		if !ok {
			return hashed, fmt.Errorf("no local path for %q", p)
		}
		sum, err := hashLocalFileSHA256(localPath)
		if err != nil {
			return hashed, err
		}
		entry.Sha256 = sum
		hashed++
	}
	return hashed, nil
}

func countManifestHashes(manifest *agentpb.FileSyncManifest) int {
	count := 0
	for _, e := range manifest.GetFiles() {
		if len(e.GetSha256()) == sha256.Size {
			count++
		}
	}
	return count
}

func fileSyncChecksumMode() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(fileSyncChecksumEnv)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func fileSyncAgentSupportsQuickCheck(ctx context.Context, conn *grpcclient.AgentConnection) bool {
	if conn == nil || conn.AgentService == nil {
		return false
	}
	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return false
	}
	for _, feature := range resp.GetFeatureset() {
		if feature == fileSyncQuickCheckFeatureName {
			return true
		}
	}
	return false
}

func printFileSyncSummary(manifest *agentpb.FileSyncManifest, manifestDuration, diffDuration time.Duration, quickSkipped, hashedFiles int, transferredBytes int64) {
	cliLogln("File sync: stat manifest %d file(s) in %s; diff in %s; quick-check skipped %d; hashed %d; transferred %s.",
		len(manifest.GetFiles()),
		manifestDuration.Round(time.Millisecond),
		diffDuration.Round(time.Millisecond),
		quickSkipped,
		hashedFiles,
		humanize.Bytes(uint64(transferredBytes)),
	)
}

// printFileSyncProgress prints a single-line progress update for the current file.
// On a TTY it overwrites the current line; otherwise it prints a new line.
func printFileSyncProgress(isTTY bool, name string, fileSent, fileTotal, totalSent int64, elapsed time.Duration, fileIdx, fileCount int) {
	pct := 0.0
	if fileTotal > 0 {
		pct = float64(fileSent) / float64(fileTotal) * 100
	}

	// Truncate long names.
	displayName := name
	const maxNameLen = 32
	if len(displayName) > maxNameLen {
		displayName = "..." + displayName[len(displayName)-maxNameLen+3:]
	}

	line := fmt.Sprintf("  %-36s %8s / %-8s %5.1f%% %9s   [%d/%d]",
		displayName,
		humanize.Bytes(uint64(fileSent)),
		humanize.Bytes(uint64(fileTotal)),
		pct,
		formatTransferRate(totalSent, elapsed),
		fileIdx, fileCount,
	)

	if isTTY {
		fmt.Printf("\r\033[2K%s", line)
	} else {
		fmt.Println(line)
	}
}

func formatTransferRate(bytesSent int64, elapsed time.Duration) string {
	if bytesSent <= 0 || elapsed <= 0 {
		return "0 B/s"
	}
	rate := float64(bytesSent) / elapsed.Seconds()
	if rate < 1 {
		return "<1 B/s"
	}
	return humanize.Bytes(uint64(rate)) + "/s"
}

func effectiveRemotePath(filePath, to string) string {
	return appconfig.EffectiveFileSyncDestination(filePath, to)
}
