package commands

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

const (
	// Apple Container reports an empty context transfer as 2B. Keep the threshold
	// deliberately tiny to avoid diagnosing legitimate, heavily-.dockerignored
	// projects as the /tmp builder regression.
	appleContainerSuspiciousContextBytes = 2
	appleContainerContextFileScanLimit   = 128
	appleContainerContextLineLimit       = 64 << 10
)

var errAppleContainerContextScanDone = errors.New("apple-container context scan done")

type appleContainerContextStats struct {
	fileCount int
	truncated bool
}

type appleContainerBuildContextMonitor struct {
	contextPath string
	pathInTmp   bool
	stats       appleContainerContextStats

	line               []byte
	sawContextTransfer bool
	contextBytes       int64
}

func newAppleContainerBuildContextMonitor(contextPath string) *appleContainerBuildContextMonitor {
	return &appleContainerBuildContextMonitor{
		contextPath: contextPath,
		pathInTmp:   appleContainerPathInTmp(contextPath),
		stats:       scanAppleContainerBuildContext(contextPath),
	}
}

func (m *appleContainerBuildContextMonitor) wrapStream(w io.Writer) io.Writer {
	if m == nil {
		return w
	}
	return io.MultiWriter(w, m)
}

func (m *appleContainerBuildContextMonitor) Write(p []byte) (int, error) {
	m.line = append(m.line, p...)
	for {
		i := bytes.IndexByte(m.line, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(m.line[:i]), "\r")
		m.line = m.line[i+1:]
		m.observeLine(line)
	}
	if len(m.line) > appleContainerContextLineLimit {
		m.line = m.line[:0]
	}
	return len(p), nil
}

func (m *appleContainerBuildContextMonitor) observeLine(line string) {
	bytes, ok := tui.ParseBuildContextTransferBytes(line)
	if !ok {
		return
	}
	m.sawContextTransfer = true
	m.contextBytes = bytes
}

func (m *appleContainerBuildContextMonitor) wrapBuildError(err error) error {
	if err == nil || m == nil {
		return err
	}
	if diagnosis := m.diagnosis(); diagnosis != "" {
		return fmt.Errorf("%s: %w", diagnosis, err)
	}
	return err
}

func (m *appleContainerBuildContextMonitor) diagnosis() string {
	if m == nil || !m.pathInTmp || m.stats.fileCount == 0 || !m.sawContextTransfer || m.contextBytes > appleContainerSuspiciousContextBytes {
		return ""
	}
	files := fmt.Sprintf("%d files", m.stats.fileCount)
	if m.stats.fileCount == 1 {
		files = "1 file"
	}
	if m.stats.truncated {
		files = "at least " + files
	}
	return fmt.Sprintf("Apple Container transferred an empty build context (%s) even though %s contains %s. This matches a known apple-container issue with /tmp and /private/tmp paths; move the project to a non-/tmp directory and retry", tui.FormatBytes(m.contextBytes), m.contextPath, files)
}

func scanAppleContainerBuildContext(contextPath string) appleContainerContextStats {
	var stats appleContainerContextStats
	if contextPath == "" {
		return stats
	}
	err := filepath.WalkDir(contextPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		stats.fileCount++
		if stats.fileCount >= appleContainerContextFileScanLimit {
			stats.truncated = true
			return errAppleContainerContextScanDone
		}
		return nil
	})
	if err != nil && !errors.Is(err, errAppleContainerContextScanDone) {
		return appleContainerContextStats{}
	}
	return stats
}

func appleContainerPathInTmp(path string) bool {
	if isTmpPath(path) {
		return true
	}
	canonical, err := filepath.EvalSymlinks(path)
	return err == nil && isTmpPath(canonical)
}

func isTmpPath(path string) bool {
	clean := filepath.Clean(path)
	return clean == "/tmp" || strings.HasPrefix(clean, "/tmp/") || clean == "/private/tmp" || strings.HasPrefix(clean, "/private/tmp/")
}
