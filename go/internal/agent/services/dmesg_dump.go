package services

import (
	"bufio"
	"errors"
	"io"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// dumpKernelLogBatchSize is the number of kernel records carried in a single
// DumpKernelLogResponse. Batching keeps individual gRPC messages small
// regardless of how large the kernel ring buffer is.
const dumpKernelLogBatchSize = 256

// contractReader adapts a reader that may violate the io.Reader contract by
// returning a negative count (notably unix.Read, which reports n == -1 on
// errors such as EAGAIN). bufio.Scanner treats a negative count as fatal and
// aborts with ErrBadReadCount ("Read returned impossible count"), masking the
// underlying error. Clamping to 0 lets the real error surface — for the kmsg
// snapshot this is the EAGAIN that signals end-of-buffer.
type contractReader struct{ r io.Reader }

func (c *contractReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n < 0 {
		n = 0
	}
	return n, err
}

// streamKmsgSnapshot reads /dev/kmsg-formatted records from r, parses each with
// parseKmsgLine, and invokes emit with batches of at most batchSize records.
// It is a one-shot snapshot: it reads until r reports EOF (or a read error) and
// does not follow new records.
//
// The slice passed to emit is reused across calls — callers that retain records
// beyond the emit call must copy it. emit's error aborts the scan and is
// returned. The scanner's own error (other than a recovered oversized-record
// case) is returned as-is so the caller can decide how to treat it (e.g. a
// non-blocking /dev/kmsg fd surfaces EAGAIN at end-of-buffer).
func streamKmsgSnapshot(r io.Reader, batchSize int, emit func([]*agentpb.KernelLogRecord) error) error {
	if batchSize < 1 {
		batchSize = 1
	}
	scanner := bufio.NewScanner(&contractReader{r: r})
	scanner.Buffer(make([]byte, 0, 8192), 256*1024)

	batch := make([]*agentpb.KernelLogRecord, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					// A single oversized record must not abort the dump. The
					// kernel advances /dev/kmsg's read position per-record, so
					// recreating the scanner resumes at the next record.
					scanner = bufio.NewScanner(&contractReader{r: r})
					scanner.Buffer(make([]byte, 0, 8192), 256*1024)
					continue
				}
				// Flush what we have, then surface the read error.
				if ferr := flush(); ferr != nil {
					return ferr
				}
				return err
			}
			break
		}

		line := scanner.Text()
		// Skip empty lines and continuation lines (kmsg continuation records
		// begin with a space).
		if len(line) == 0 || line[0] == ' ' {
			continue
		}
		level, message, tsUS, ok := parseKmsgLine(line)
		if !ok {
			continue
		}
		batch = append(batch, &agentpb.KernelLogRecord{
			TimestampUs: tsUS,
			Level:       int32(level),
			Message:     message,
		})
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}

	return flush()
}
