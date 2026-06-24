//go:build linux

package services

import (
	"errors"
	"io"
	"sync"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// DumpKernelLog reads the current kernel ring buffer (/dev/kmsg) once and
// streams the parsed records to the client. It is a one-shot inspection dump:
// records are NOT PII-redacted and there is no DPIA gate, because this is a
// local diagnostic for an operator connected to their own device — distinct
// from the OTel kernel-log streaming path (CollectDmesgLogs).
func (s *AgentService) DumpKernelLog(_ *agentpb.DumpKernelLogRequest, stream grpc.ServerStreamingServer[agentpb.DumpKernelLogResponse]) error {
	s.logger.Info("DumpKernelLog started")

	r, err := openKmsgForSnapshot(s.logger)
	if err != nil {
		return status.Errorf(codes.Unavailable, "kernel log unavailable: %v", err)
	}
	defer r.Close()

	count := 0
	err = streamKmsgSnapshot(r, dumpKernelLogBatchSize, func(recs []*agentpb.KernelLogRecord) error {
		count += len(recs)
		return stream.Send(&agentpb.DumpKernelLogResponse{Records: recs})
	})
	// A non-blocking /dev/kmsg fd reports EAGAIN once the buffer is drained;
	// that is the normal end of a snapshot, not an error.
	if errors.Is(err, syscall.EAGAIN) {
		err = nil
	}
	if err != nil {
		s.logger.Warn("DumpKernelLog ended with error", zap.Int("records", count), zap.Error(err))
		return err
	}
	s.logger.Info("DumpKernelLog completed", zap.Int("records", count))
	return nil
}

// kmsgSnapshotReader reads /dev/kmsg records via raw syscalls. It deliberately
// bypasses *os.File and the Go runtime poller: a pollable fd opened O_NONBLOCK
// and wrapped by os.File would park the goroutine on EAGAIN waiting for the
// next kernel message, instead of returning EAGAIN to signal end-of-buffer.
// Raw unix.Read returns EAGAIN immediately, which is exactly the snapshot
// terminator we want.
type kmsgSnapshotReader struct {
	fd        int
	closeOnce sync.Once
}

func (k *kmsgSnapshotReader) Read(p []byte) (int, error) {
	// /dev/kmsg returns exactly one record per read(); the caller's buffer must
	// be large enough to hold it. streamKmsgSnapshot uses a 256 KiB buffer,
	// well above the kernel's per-record limit.
	return unix.Read(k.fd, p)
}

func (k *kmsgSnapshotReader) Close() error {
	var err error
	k.closeOnce.Do(func() { err = unix.Close(k.fd) })
	return err
}

// openKmsgForSnapshot opens /dev/kmsg non-blocking, verifies it is the genuine
// kernel message device, and positions the read at the oldest available record
// so the full retained buffer is dumped. The same char-device and device-number
// hardening as CollectDmesgLogs guards against a bind-mount substituting another
// device or a regular file.
func openKmsgForSnapshot(logger *zap.Logger) (io.ReadCloser, error) {
	fd, err := unix.Open("/dev/kmsg", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		_ = unix.Close(fd)
		return nil, errors.New("/dev/kmsg is not a character device")
	}
	if maj, min := unix.Major(st.Rdev), unix.Minor(st.Rdev); maj != 1 || min != 11 {
		_ = unix.Close(fd)
		logger.Warn("dmesg dump: /dev/kmsg has unexpected device numbers",
			zap.Uint32("major", maj), zap.Uint32("minor", min))
		return nil, errors.New("/dev/kmsg has unexpected device numbers")
	}

	// Seek to the first (oldest) available record. Opening /dev/kmsg already
	// positions here, but the explicit seek documents intent and is harmless.
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &kmsgSnapshotReader{fd: fd}, nil
}
