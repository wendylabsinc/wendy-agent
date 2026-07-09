// Package simutil holds simulation client helpers shared by the CLI commands
// and the MCP server (which must not import the commands package): local model
// tar-streaming for ImportModel, control-level and position parsing, and
// replay downloading.
package simutil

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

// ModelChunkSize is the data-chunk size for streaming model archives to the
// agent; small enough to stay well under gRPC message limits.
const ModelChunkSize = 64 * 1024

// ControlLevelName renders a simpb.ControlLevel as a short lowercase name
// (e.g. "motion").
func ControlLevelName(l simpb.ControlLevel) string {
	return strings.ToLower(strings.TrimPrefix(l.String(), "CONTROL_LEVEL_"))
}

// ParseControlLevel maps a control-level name to a simpb.ControlLevel.
func ParseControlLevel(s string) (simpb.ControlLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "task":
		return simpb.ControlLevel_CONTROL_LEVEL_TASK, nil
	case "motion":
		return simpb.ControlLevel_CONTROL_LEVEL_MOTION, nil
	case "joint":
		return simpb.ControlLevel_CONTROL_LEVEL_JOINT, nil
	case "physics":
		return simpb.ControlLevel_CONTROL_LEVEL_PHYSICS, nil
	default:
		return simpb.ControlLevel_CONTROL_LEVEL_UNSPECIFIED,
			fmt.Errorf("invalid control level %q: expected task, motion, joint, or physics", s)
	}
}

// ParsePosition parses an "x,y,z" position (meters) into a Pose with identity
// orientation. An empty string yields nil (backend default pose).
func ParsePosition(pos string) (*simpb.Pose, error) {
	if pos == "" {
		return nil, nil
	}
	parts := strings.Split(pos, ",")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid position %q: expected x,y,z", pos)
	}
	coords := make([]float64, 3)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid position %q: %q is not a number", pos, strings.TrimSpace(p))
		}
		coords[i] = v
	}
	// Identity orientation (qw=1); the proto also treats an all-zero
	// quaternion as identity, but being explicit costs nothing.
	return &simpb.Pose{X: coords[0], Y: coords[1], Z: coords[2], Qw: 1}, nil
}

// DownloadReplay fetches a replay from the agent's sim service and writes it
// to path, returning the number of bytes written.
func DownloadReplay(ctx context.Context, client agentpbv2.WendySimServiceClient, replayID, path string) (int64, error) {
	stream, err := client.GetReplay(ctx, &agentpbv2.GetReplayRequest{ReplayId: replayID})
	if err != nil {
		return 0, err
	}
	return WriteReplayFile(path, func() ([]byte, error) {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			return nil, recvErr
		}
		return chunk.GetData(), nil
	})
}

// WriteReplayFile drains recv (which returns io.EOF at end of stream) into
// path and returns the number of bytes written. A partial file is removed on
// error so a failed download never leaves a truncated replay behind.
func WriteReplayFile(path string, recv func() ([]byte, error)) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	fail := func(cause error) (int64, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return 0, cause
	}
	var written int64
	for {
		data, recvErr := recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return fail(recvErr)
		}
		n, writeErr := f.Write(data)
		written += int64(n)
		if writeErr != nil {
			return fail(writeErr)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return 0, err
	}
	return written, nil
}

// StreamLocalModel streams a local model as tar data chunks. A directory is
// tarred on the fly; a file is streamed as-is, transparently gunzipping
// .tar.gz/.tgz archives so the wire always carries a plain tar.
func StreamLocalModel(path string, send func([]byte) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(tarDirectory(path, pw))
		}()
		if err := sendDataChunks(pr, send); err != nil {
			_ = pr.CloseWithError(err)
			return err
		}
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	var r io.Reader = br
	// Accept gzipped archives: sniff the gzip magic and decompress so the
	// backend always receives a plain tar stream.
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("reading gzip archive: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	return sendDataChunks(r, send)
}

// tarDirectory writes a tar archive of dir to w. Entry names are relative to
// dir (forward-slashed); only directories and regular files are archived.
func tarDirectory(dir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // skip symlinks, sockets, etc.
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// sendDataChunks reads r and calls send with successive chunks of at most
// ModelChunkSize bytes.
func sendDataChunks(r io.Reader, send func([]byte) error) error {
	buf := make([]byte, ModelChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := send(chunk); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
