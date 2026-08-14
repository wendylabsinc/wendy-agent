package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// volumeChunkSize is the payload per chunk message. gRPC's default maximum
// receive size is 4 MiB and the agent does not raise it, so a 4 MiB payload
// plus its path and digest fields is rejected by the server mid-transfer.
// Staying well under the cap keeps the push working without asking every agent
// to loosen a limit that protects it.
const volumeChunkSize = 2 * 1024 * 1024

// fileSyncV2Stream is the client side of a v2 file sync session.
type fileSyncV2Stream = grpc.BidiStreamingClient[agentpbv2.FileSyncRequest, agentpbv2.FileSyncResponse]

// volumeSpec addresses a location inside a persistent volume, written
// <volume>:<path>. The path is always relative to the volume root.
type volumeSpec struct {
	volume string
	path   string
}

// parseVolumeSpec splits <volume>:<path>. The path may be empty, which means
// the volume root — useful for listing.
func parseVolumeSpec(arg string) (volumeSpec, error) {
	volume, rest, found := strings.Cut(arg, ":")
	if !found {
		return volumeSpec{}, fmt.Errorf("expected <volume>:<path>, got %q", arg)
	}
	if volume == "" {
		return volumeSpec{}, fmt.Errorf("missing volume name in %q", arg)
	}
	if strings.Contains(volume, "/") {
		return volumeSpec{}, fmt.Errorf("volume name must not contain a path separator: %q", volume)
	}

	// Reject traversal on the raw path, before cleaning: path.Clean resolves
	// "../escape" against the leading slash and hands back "escape", which
	// looks harmless and is not what the user asked for.
	for _, component := range strings.Split(rest, "/") {
		if component == ".." {
			return volumeSpec{}, fmt.Errorf("path must not contain ..: %q", rest)
		}
	}
	clean := strings.Trim(path.Clean("/"+strings.TrimPrefix(rest, "/")), "/")
	if clean == "." {
		clean = ""
	}
	return volumeSpec{volume: volume, path: clean}, nil
}

func newVolumesPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push <local-path> <volume>:<remote-path>",
		Short: "Copy a file or directory into a persistent volume",
		Long: `Copy a local file or directory into a persistent volume on the device.

This puts an artifact — model weights, a calibration file, a map — on a running
device without rebuilding and redeploying its image. Whether the app notices a
new file is up to the app; push only delivers the bytes.

Files are written atomically, so an app reading the volume sees either the old
contents or the complete new ones, never a partial file. The sha256 the device
ended up with is reported so it can be compared rather than trusted.

Existing files at the destination are overwritten. Nothing else in the volume is
touched: push never deletes.`,
		Example: `  # Push a model into a volume, under models/
  wendy device volumes push ./sffa_yolo.onnx app-data:models/sffa_yolo.onnx

  # Push a whole directory
  wendy device volumes push ./calibration app-data:calibration`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := parseVolumeSpec(args[1])
			if err != nil {
				return err
			}
			if spec.path == "" {
				return fmt.Errorf("a destination path is required, e.g. %s:models/model.onnx", spec.volume)
			}
			ctx := cmd.Context()
			target, err := volumeSyncTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			return runVolumePush(ctx, target.Agent.FileSyncServiceV2, args[0], spec)
		},
	}
	return cmd
}

func newVolumesFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "files <volume>[:<path>]",
		Aliases: []string{"ls"},
		Short:   "List files in a persistent volume",
		Long: `List the files in a persistent volume, with the sha256 the device holds.

This exists so checking whether a push landed does not require opening a root
shell on the device.`,
		Example: `  wendy device volumes files app-data
  wendy device volumes files app-data:models`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := args[0]
			if !strings.Contains(arg, ":") {
				arg += ":"
			}
			spec, err := parseVolumeSpec(arg)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			target, err := volumeSyncTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			return runVolumeList(ctx, target.Agent.FileSyncServiceV2, spec)
		},
	}
}

func newVolumesRemoveFileCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rm <volume>:<path>",
		Short: "Delete a file from a persistent volume",
		Long: `Delete a single file from a persistent volume.

To delete a whole volume, use "volumes remove".`,
		Example: `  wendy device volumes rm app-data:models/old.onnx`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := parseVolumeSpec(args[0])
			if err != nil {
				return err
			}
			if spec.path == "" {
				return fmt.Errorf("a file path is required; use \"volumes remove %s\" to delete the whole volume", spec.volume)
			}
			if !force && !jsonOutput {
				confirmed, err := tui.Confirm(fmt.Sprintf("Delete %s from volume %q?", spec.path, spec.volume))
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			ctx := cmd.Context()
			target, err := volumeSyncTarget(ctx)
			if err != nil {
				return err
			}
			defer target.Close()
			return runVolumeRemoveFile(ctx, target.Agent.FileSyncServiceV2, spec)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

// volumeSyncTarget resolves a device connection that can serve file sync.
func volumeSyncTarget(ctx context.Context) (*SelectedDevice, error) {
	target, err := resolveTarget(ctx)
	if err != nil {
		return nil, err
	}
	if target.Agent == nil {
		target.Close()
		return nil, fmt.Errorf("volume file access requires a WendyOS device")
	}
	return target, nil
}

// pushEntry pairs a local file with its path inside the volume.
type pushEntry struct {
	localPath string
	// remotePath is relative to the session's path prefix.
	remotePath string
	entry      *agentpbv2.FileSyncEntry
}

func runVolumePush(ctx context.Context, client agentpbv2.WendyFileSyncServiceClient, localPath string, spec volumeSpec) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", localPath, err)
	}

	// The prefix is the directory the session works in; sending it separately
	// keeps the agent from hashing the rest of the volume to build a manifest.
	var prefix string
	var entries []pushEntry
	if info.IsDir() {
		prefix = spec.path
		entries, err = collectDirEntries(localPath)
	} else {
		prefix = path.Dir(spec.path)
		if prefix == "." {
			prefix = ""
		}
		entries, err = collectFileEntry(localPath, path.Base(spec.path), info)
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s contains no files to push", localPath)
	}

	stream, err := client.SyncFiles(ctx)
	if err != nil {
		return fmt.Errorf("opening file sync stream: %w", err)
	}

	manifest := make([]*agentpbv2.FileSyncEntry, 0, len(entries))
	for _, e := range entries {
		manifest = append(manifest, e.entry)
	}
	if err := stream.Send(&agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     spec.volume,
			PathPrefix: prefix,
			Manifest:   &agentpbv2.FileSyncManifest{Files: manifest},
		}},
	}); err != nil {
		return streamSendError(stream, err, "starting file sync")
	}

	remote, err := recvManifest(stream)
	if err != nil {
		return err
	}

	// Skip anything the device already holds byte for byte. Re-uploading 50 MB
	// over a site link because a retry lost track is the failure this avoids.
	var todo []pushEntry
	var skipped int
	for _, e := range entries {
		if existing, ok := remote[e.remotePath]; ok &&
			existing.GetSize() == e.entry.GetSize() &&
			hex.EncodeToString(existing.GetSha256()) == hex.EncodeToString(e.entry.GetSha256()) {
			skipped++
			continue
		}
		todo = append(todo, e)
	}

	if err := sendPushEntries(stream, todo); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing file sync stream: %w", err)
	}
	if err := drainToComplete(stream); err != nil {
		return err
	}

	return reportPush(spec, prefix, todo, skipped)
}

func sendPushEntries(stream fileSyncV2Stream, todo []pushEntry) error {
	isTTY := term.IsTerminal(int(os.Stdout.Fd())) && !jsonOutput
	started := time.Now()
	var sentTotal int64

	for i, e := range todo {
		f, err := os.Open(e.localPath)
		if err != nil {
			return fmt.Errorf("opening %s: %w", e.localPath, err)
		}

		hasher := sha256.New()
		buf := make([]byte, volumeChunkSize)
		var sent int64
		var sequence uint64

		sendChunk := func(data []byte) error {
			hasher.Write(data)
			sent += int64(len(data))
			sentTotal += int64(len(data))
			return stream.Send(&agentpbv2.FileSyncRequest{
				RequestType: &agentpbv2.FileSyncRequest_Chunk{Chunk: &agentpbv2.FileSyncChunk{
					Path:           e.remotePath,
					Data:           data,
					Sequence:       sequence,
					CumulativeSize: sent,
					Sha256:         hasher.Sum(nil),
				}},
			})
		}

		if e.entry.GetSize() == 0 {
			if err := sendChunk(nil); err != nil {
				f.Close()
				return streamSendError(stream, err, "sending "+e.remotePath)
			}
		} else {
			for {
				n, readErr := f.Read(buf)
				if n > 0 {
					if err := sendChunk(buf[:n]); err != nil {
						f.Close()
						return streamSendError(stream, err, "sending "+e.remotePath)
					}
					sequence++
					printFileSyncProgress(isTTY, e.remotePath, sent, e.entry.GetSize(),
						sentTotal, time.Since(started), i+1, len(todo))
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					f.Close()
					return fmt.Errorf("reading %s: %w", e.localPath, readErr)
				}
			}
		}
		f.Close()

		if err := stream.Send(&agentpbv2.FileSyncRequest{
			RequestType: &agentpbv2.FileSyncRequest_Commit{Commit: &agentpbv2.FileSyncCommit{
				Path:   e.remotePath,
				Sha256: e.entry.GetSha256(),
				Size:   e.entry.GetSize(),
			}},
		}); err != nil {
			return streamSendError(stream, err, "committing "+e.remotePath)
		}

		// The ack is the device confirming the bytes are in place, digest
		// verified, under their final name.
		if err := recvAck(stream, e.remotePath); err != nil {
			return err
		}
	}
	return nil
}

func runVolumeList(ctx context.Context, client agentpbv2.WendyFileSyncServiceClient, spec volumeSpec) error {
	stream, err := client.SyncFiles(ctx)
	if err != nil {
		return fmt.Errorf("opening file sync stream: %w", err)
	}
	if err := stream.Send(&agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     spec.volume,
			PathPrefix: spec.path,
			Manifest:   &agentpbv2.FileSyncManifest{},
		}},
	}); err != nil {
		return fmt.Errorf("listing volume: %w", err)
	}
	remote, err := recvManifest(stream)
	if err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing file sync stream: %w", err)
	}
	if err := drainToComplete(stream); err != nil {
		return err
	}

	paths := make([]string, 0, len(remote))
	for p := range remote {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	if jsonOutput {
		type jsonFile struct {
			Path      string `json:"path"`
			SizeBytes int64  `json:"sizeBytes"`
			Size      string `json:"size"`
			Sha256    string `json:"sha256"`
			Mode      string `json:"mode"`
		}
		files := make([]jsonFile, 0, len(paths))
		for _, p := range paths {
			e := remote[p]
			files = append(files, jsonFile{
				Path:      p,
				SizeBytes: e.GetSize(),
				Size:      formatBytes(e.GetSize()),
				Sha256:    hex.EncodeToString(e.GetSha256()),
				Mode:      fmt.Sprintf("%04o", e.GetMode()),
			})
		}
		data, err := json.MarshalIndent(files, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if len(paths) == 0 {
		if spec.path == "" {
			fmt.Printf("Volume %q is empty.\n", spec.volume)
		} else {
			fmt.Printf("No files under %s in volume %q.\n", spec.path, spec.volume)
		}
		return nil
	}

	headers := []string{"Path", "Size", "Mode", "SHA256"}
	var rows [][]string
	for _, p := range paths {
		e := remote[p]
		digest := hex.EncodeToString(e.GetSha256())
		rows = append(rows, []string{
			p,
			formatBytes(e.GetSize()),
			fmt.Sprintf("%04o", e.GetMode()),
			digest[:12],
		})
	}
	fmt.Print(tui.RenderTable(headers, rows))
	return nil
}

func runVolumeRemoveFile(ctx context.Context, client agentpbv2.WendyFileSyncServiceClient, spec volumeSpec) error {
	stream, err := client.SyncFiles(ctx)
	if err != nil {
		return fmt.Errorf("opening file sync stream: %w", err)
	}
	if err := stream.Send(&agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   spec.volume,
			Manifest: &agentpbv2.FileSyncManifest{},
		}},
	}); err != nil {
		return fmt.Errorf("starting file sync: %w", err)
	}
	if _, err := recvManifest(stream); err != nil {
		return err
	}
	if err := stream.Send(&agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Delete{Delete: &agentpbv2.FileSyncDelete{
			Paths: []string{spec.path},
		}},
	}); err != nil {
		return fmt.Errorf("deleting %s: %w", spec.path, err)
	}
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing file sync stream: %w", err)
	}
	if err := drainToComplete(stream); err != nil {
		return err
	}

	if jsonOutput {
		data, err := json.MarshalIndent(map[string]string{
			"volume":  spec.volume,
			"path":    spec.path,
			"deleted": "true",
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Deleted %s from volume %q.\n", spec.path, spec.volume)
	return nil
}

// streamSendError turns a failed Send into the reason the server gave. gRPC
// reports a server-side rejection to the sender as io.EOF, so the actual status
// is only visible by reading the stream — without this, a rejected push
// surfaces as "EOF" and tells the user nothing.
func streamSendError(stream fileSyncV2Stream, sendErr error, what string) error {
	if errors.Is(sendErr, io.EOF) {
		if _, recvErr := stream.Recv(); recvErr != nil && !errors.Is(recvErr, io.EOF) {
			return fmt.Errorf("%s: %w", what, recvErr)
		}
	}
	return fmt.Errorf("%s: %w", what, sendErr)
}

// recvManifest reads the manifest the agent opens with, indexed by path.
func recvManifest(stream fileSyncV2Stream) (map[string]*agentpbv2.FileSyncEntry, error) {
	resp, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("reading device manifest: %w", err)
	}
	manifest, ok := resp.GetResponseType().(*agentpbv2.FileSyncResponse_Manifest)
	if !ok {
		return nil, fmt.Errorf("expected a manifest from the device, got %T", resp.GetResponseType())
	}
	byPath := make(map[string]*agentpbv2.FileSyncEntry, len(manifest.Manifest.GetFiles()))
	for _, e := range manifest.Manifest.GetFiles() {
		byPath[e.GetPath()] = e
	}
	return byPath, nil
}

func recvAck(stream fileSyncV2Stream, wantPath string) error {
	resp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("waiting for %s to be committed: %w", wantPath, err)
	}
	ack, ok := resp.GetResponseType().(*agentpbv2.FileSyncResponse_Ack)
	if !ok {
		return fmt.Errorf("expected an ack for %s, got %T", wantPath, resp.GetResponseType())
	}
	if got := ack.Ack.GetPath(); got != wantPath {
		return fmt.Errorf("device acked %q, expected %q", got, wantPath)
	}
	return nil
}

// drainToComplete waits for the agent's end-of-session message. Reading it is
// what surfaces a server-side error that arrived after the last ack.
func drainToComplete(stream fileSyncV2Stream) error {
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if _, done := resp.GetResponseType().(*agentpbv2.FileSyncResponse_Complete); done {
			return nil
		}
	}
}

func reportPush(spec volumeSpec, prefix string, pushed []pushEntry, skipped int) error {
	if jsonOutput {
		type jsonPushed struct {
			Path      string `json:"path"`
			SizeBytes int64  `json:"sizeBytes"`
			Sha256    string `json:"sha256"`
		}
		files := make([]jsonPushed, 0, len(pushed))
		for _, e := range pushed {
			files = append(files, jsonPushed{
				Path:      path.Join(prefix, e.remotePath),
				SizeBytes: e.entry.GetSize(),
				Sha256:    hex.EncodeToString(e.entry.GetSha256()),
			})
		}
		data, err := json.MarshalIndent(map[string]any{
			"volume":  spec.volume,
			"pushed":  files,
			"skipped": skipped,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	for _, e := range pushed {
		// Report the digest the device verified, so the caller can compare
		// rather than trust: subtly corrupt weights load fine and are quietly
		// wrong, which is the worst failure mode available here.
		fmt.Printf("Pushed %s to %s:%s (%s, sha256:%s)\n",
			e.localPath, spec.volume, path.Join(prefix, e.remotePath),
			formatBytes(e.entry.GetSize()), hex.EncodeToString(e.entry.GetSha256()))
	}
	if skipped > 0 {
		fmt.Printf("%d file(s) already up to date.\n", skipped)
	}
	return nil
}

func collectFileEntry(localPath, remoteName string, info os.FileInfo) ([]pushEntry, error) {
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", localPath)
	}
	digest, size, err := hashLocalFile(localPath)
	if err != nil {
		return nil, err
	}
	return []pushEntry{{
		localPath:  localPath,
		remotePath: remoteName,
		entry: &agentpbv2.FileSyncEntry{
			Path:   remoteName,
			Size:   size,
			Sha256: digest,
			Mode:   uint32(info.Mode().Perm()),
		},
	}}, nil
}

func collectDirEntries(root string) ([]pushEntry, error) {
	var entries []pushEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Symlinks are skipped rather than followed: what they point at may not
		// exist on the device, and pushing the target's bytes under the link's
		// name would silently change the layout the app sees.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		digest, size, err := hashLocalFile(p)
		if err != nil {
			return err
		}
		remote := filepath.ToSlash(rel)
		entries = append(entries, pushEntry{
			localPath:  p,
			remotePath: remote,
			entry: &agentpbv2.FileSyncEntry{
				Path:   remote,
				Size:   size,
				Sha256: digest,
				Mode:   uint32(info.Mode().Perm()),
			},
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].remotePath < entries[j].remotePath })
	return entries, nil
}

func hashLocalFile(path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", path, err)
	}
	return h.Sum(nil), size, nil
}
