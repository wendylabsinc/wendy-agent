package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

const maxReadChunk = 1 << 20 // 1 MiB

type VolumeFsService struct {
	agentpbv2.UnimplementedWendyVolumeFsServiceServer
	logger *zap.Logger
}

func NewVolumeFsService(logger *zap.Logger) *VolumeFsService {
	return &VolumeFsService{logger: logger}
}

// osErrToStatus maps filesystem errors to gRPC status codes.
func osErrToStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, os.ErrPermission):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, os.ErrExist):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, unix.ENOSPC):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func fileInfoToAttr(fi os.FileInfo, symlinkTarget string) *agentpbv2.Attr {
	t := agentpbv2.FileType_FILE_TYPE_REGULAR
	switch {
	case fi.IsDir():
		t = agentpbv2.FileType_FILE_TYPE_DIR
	case fi.Mode()&os.ModeSymlink != 0:
		t = agentpbv2.FileType_FILE_TYPE_SYMLINK
	}
	return &agentpbv2.Attr{
		Type:          t,
		Size:          fi.Size(),
		Mode:          uint32(fi.Mode().Perm()),
		MtimeUnixNano: fi.ModTime().UnixNano(),
		SymlinkTarget: symlinkTarget,
	}
}

func (s *VolumeFsService) Stat(_ context.Context, req *agentpbv2.StatRequest) (*agentpbv2.StatResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	var target string
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ = os.Readlink(full)
	}
	return &agentpbv2.StatResponse{Attr: fileInfoToAttr(fi, target)}, nil
}

func (s *VolumeFsService) ReadDir(_ context.Context, req *agentpbv2.ReadDirRequest) (*agentpbv2.ReadDirResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	out := make([]*agentpbv2.DirEntry, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		var target string
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ = os.Readlink(filepath.Join(full, e.Name()))
		}
		out = append(out, &agentpbv2.DirEntry{Name: e.Name(), Attr: fileInfoToAttr(fi, target)})
	}
	return &agentpbv2.ReadDirResponse{Entries: out}, nil
}

func (s *VolumeFsService) Read(_ context.Context, req *agentpbv2.ReadRequest) (*agentpbv2.ReadResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	n := int(req.GetLength())
	if n <= 0 || n > maxReadChunk {
		n = maxReadChunk
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := f.ReadAt(buf, req.GetOffset())
	eof := errors.Is(err, io.EOF)
	if err != nil && !eof {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.ReadResponse{Data: buf[:read], Eof: eof}, nil
}

func (s *VolumeFsService) StatFs(_ context.Context, req *agentpbv2.StatFsRequest) (*agentpbv2.StatFsResponse, error) {
	root, err := volumeRoot(req.GetVolume())
	if err != nil {
		return nil, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.StatFsResponse{
		TotalBytes: st.Blocks * uint64(st.Bsize),
		FreeBytes:  st.Bavail * uint64(st.Bsize),
	}, nil
}
