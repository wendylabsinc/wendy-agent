//go:build linux

package services

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// DumpKernelLog streams the kernel ring buffer (/dev/kmsg) to the client.
//
// With follow enabled (the default when the request omits the field), it
// replays the buffered records and then keeps following new kernel messages
// until the client disconnects, like `dmesg -w`. With follow=false it streams
// the buffered records and completes once the buffer is drained, like `dmesg`.
//
// Records are NOT PII-redacted and there is no DPIA gate, because this is a
// local diagnostic for an operator connected to their own device — distinct
// from the OTel kernel-log streaming path (CollectDmesgLogs).
func (s *AgentService) DumpKernelLog(req *agentpb.DumpKernelLogRequest, stream grpc.ServerStreamingServer[agentpb.DumpKernelLogResponse]) error {
	// Default to follow: an unset field (req.Follow == nil) means the caller
	// did not opt out, so we tail. Only an explicit follow=false is one-shot.
	// Read the pointer field directly — GetFollow() collapses unset to false.
	follow := req.Follow == nil || *req.Follow
	s.logger.Info("DumpKernelLog started", zap.Bool("follow", follow))

	r, err := openKmsg(s.logger, follow)
	if err != nil {
		return status.Errorf(codes.Unavailable, "kernel log unavailable: %v", err)
	}
	defer r.Close()

	// Follow mode blocks waiting for new kernel messages; closing the fd when
	// the stream's context ends (client disconnect / ctrl-c) unwinds the parked
	// Read so the RPC returns. Only follow mode needs this, and only follow
	// mode can do it safely: r is an *os.File there, whose Close serializes
	// against an in-flight Read via an internal refcount. The snapshot path
	// wraps a raw fd (no such protection) and terminates on EAGAIN by itself,
	// so it must NOT have a concurrent closer racing unix.Read for that fd.
	if follow {
		stop := context.AfterFunc(stream.Context(), func() { _ = r.Close() })
		defer stop()
	}

	count := 0
	err = streamKmsgSnapshot(r, dumpKernelLogBatchSize, func(recs []*agentpb.KernelLogRecord) error {
		count += len(recs)
		return stream.Send(&agentpb.DumpKernelLogResponse{Records: recs})
	})
	switch {
	case errors.Is(err, syscall.EAGAIN):
		// A non-blocking /dev/kmsg fd reports EAGAIN once the buffer is drained;
		// in snapshot mode that is the normal end, not an error.
		err = nil
	case follow && errors.Is(err, os.ErrClosed) && stream.Context().Err() != nil:
		// Follow mode was cancelled: our context-cancel closer shut the
		// *os.File, so the parked Read unwound with ErrClosed. That is the
		// expected end of a cancelled follow, not a real error.
		err = nil
	}
	if err != nil {
		s.logger.Warn("DumpKernelLog ended with error", zap.Int("records", count), zap.Error(err))
		return err
	}
	s.logger.Info("DumpKernelLog completed", zap.Int("records", count), zap.Bool("follow", follow))
	return nil
}

// openKmsg opens /dev/kmsg for either a one-shot snapshot or a follow stream.
// Snapshot mode (follow=false) uses a non-blocking fd whose reads surface
// EAGAIN at end-of-buffer; follow mode (follow=true) uses a blocking *os.File
// whose Read parks until new messages arrive and unwinds when the fd is closed.
func openKmsg(logger *zap.Logger, follow bool) (io.ReadCloser, error) {
	if follow {
		return openKmsgForFollow(logger)
	}
	return openKmsgForSnapshot(logger)
}

// openKmsgForFollow opens /dev/kmsg as an *os.File positioned at the oldest
// available record, so the dump replays the buffer and then follows new
// messages. The Go runtime poller backs *os.File on a (pollable) char device,
// so Read parks the goroutine on the netpoller — returning buffered records,
// then waiting for new ones — and a Close on the fd unblocks it. That is how
// DumpKernelLog cancels the follow stream on client disconnect.
func openKmsgForFollow(logger *zap.Logger) (io.ReadCloser, error) {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		return nil, err
	}
	// Verify the device through SyscallConn rather than f.Fd(): Fd() switches
	// the descriptor to blocking mode and detaches it from the runtime poller,
	// which would stop Close() from interrupting the parked follow Read and
	// break cancellation. SyscallConn.Control leaves the poller registration
	// intact.
	sc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	var verifyErr error
	if ctrlErr := sc.Control(func(fd uintptr) {
		verifyErr = verifyKmsgDevice(int(fd), logger)
	}); ctrlErr != nil {
		_ = f.Close()
		return nil, ctrlErr
	}
	if verifyErr != nil {
		_ = f.Close()
		return nil, verifyErr
	}
	return f, nil
}

// verifyKmsgDevice applies the same char-device and device-number hardening as
// CollectDmesgLogs, guarding against a bind-mount substituting another device or
// a regular file for /dev/kmsg.
func verifyKmsgDevice(fd int, logger *zap.Logger) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return errors.New("/dev/kmsg is not a character device")
	}
	if maj, min := unix.Major(st.Rdev), unix.Minor(st.Rdev); maj != 1 || min != 11 {
		logger.Warn("dmesg dump: /dev/kmsg has unexpected device numbers",
			zap.Uint32("major", maj), zap.Uint32("minor", min))
		return errors.New("/dev/kmsg has unexpected device numbers")
	}
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
	//
	// unix.Read reports n == -1 on error (e.g. EAGAIN when the buffer is
	// drained). io.Reader requires n >= 0; streamKmsgSnapshot clamps the count
	// at the bufio.Scanner boundary so the real error surfaces.
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

	if err := verifyKmsgDevice(fd, logger); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	// Seek to the first (oldest) available record. Opening /dev/kmsg already
	// positions here, but the explicit seek documents intent and is harmless.
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &kmsgSnapshotReader{fd: fd}, nil
}
