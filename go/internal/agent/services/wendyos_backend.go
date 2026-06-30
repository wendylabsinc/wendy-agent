package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// wendyOSUpdater drives the in-house wendyos-update engine
// (github.com/wendylabsinc/wendyos-update), the primary OS A/B update backend.
// Its CLI mirrors mender-update's verbs: `commit` reports "nothing pending"
// with exit 2, the differences being structured JSON-lines progress on stdout
// and a richer install reject code (exit 3 = artifact rejected). A verify
// failure surfaces as exit 4 from `commit` (not install), handled by the gate.
type wendyOSUpdater struct {
	logger *zap.Logger
}

func newWendyOSUpdater(logger *zap.Logger) wendyOSUpdater {
	return wendyOSUpdater{logger: logger}
}

func (w wendyOSUpdater) name() string { return updaterNameWendyOS }

// delegatesHealthcheck is true: wendyos-update runs /etc/wendyos-update/health.d
// inside `commit`, so the agent gate skips its own CheckAll and acts on the
// commit verdict instead.
func (w wendyOSUpdater) delegatesHealthcheck() bool { return true }

func (w wendyOSUpdater) commitCommand() string { return "wendyos-update" }

func (w wendyOSUpdater) available() bool {
	_, found := resolveWendyOSBinary()
	return found
}

// wendyOSDetectTimeout bounds the `status` connector probe used by detect().
const wendyOSDetectTimeout = 10 * time.Second

// detect reports whether wendyos-update can update this device. The binary
// must be present AND `wendyos-update status` must succeed: status resolves the
// platform connector, so a clean exit confirms a supported board. On
// unsupported boards it errors and the agent falls back to mender.
func (w wendyOSUpdater) detect() bool {
	binary, found := resolveWendyOSBinary()
	if !found {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), wendyOSDetectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "status", "--json")
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	if err := cmd.Run(); err != nil {
		w.logger.Debug("wendyos-update status probe failed; backend unavailable for this device",
			zap.Error(err))
		return false
	}
	return true
}

func (w wendyOSUpdater) install(ctx context.Context, artifactURL string, onProgress func(string, int32)) error {
	binary, found := resolveWendyOSBinary()
	if !found {
		return errors.New("wendyos-update binary not found")
	}

	onProgress("downloading", 0)
	cmd := exec.CommandContext(ctx, binary, "install", artifactURL)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	// stdout carries machine-readable JSON-lines progress; stderr carries
	// human logs, whose tail we keep so a non-zero exit reports the real cause.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start wendyos-update: %v", err)
	}

	outputTail := newLineRing(menderErrorTailLines)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			if phase, percent, ok := parseWendyOSProgress(scanner.Text()); ok {
				onProgress(phase, percent)
			}
		}
		if err := scanner.Err(); err != nil {
			w.logger.Warn("wendyos-update progress scan error", zap.Error(err))
		}
	}()
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			outputTail.push(line)
			w.logger.Debug("wendyos-update output", zap.String("line", line))
		}
		if err := scanner.Err(); err != nil {
			w.logger.Warn("wendyos-update output scan error", zap.Error(err))
		}
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		code := exitCodeOf(err)
		msg := wendyOSInstallErrorMessage(code, outputTail.tail())
		w.logger.Error("wendyos-update install failed",
			zap.Int("exit_code", code), zap.Error(err), zap.Strings("output_tail", outputTail.tail()))
		return errors.New(msg)
	}
	return nil
}

func (w wendyOSUpdater) commit() oshealth.MenderResult {
	binary, found := resolveWendyOSBinary()
	if !found {
		return oshealth.MenderResult{Status: oshealth.MenderUnavailable}
	}
	return runUpdaterCommit(w.logger, binary, "commit")
}

func (w wendyOSUpdater) rollback() oshealth.MenderResult {
	binary, found := resolveWendyOSBinary()
	if !found {
		return oshealth.MenderResult{Status: oshealth.MenderUnavailable}
	}
	return runUpdaterCommit(w.logger, binary, "rollback")
}

// resolveWendyOSBinary finds the wendyos-update binary, preferring PATH and
// then probing absolute candidates (mirrors resolveMenderBinary).
func resolveWendyOSBinary() (string, bool) {
	candidates := []string{
		"wendyos-update",
		"/usr/local/sbin/wendyos-update",
		"/usr/local/bin/wendyos-update",
		"/usr/sbin/wendyos-update",
		"/usr/bin/wendyos-update",
		"/sbin/wendyos-update",
		"/bin/wendyos-update",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, true
		}
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil {
				return c, true
			}
		}
	}
	return "", false
}

// wendyOSProgressLine is one JSON-lines progress record on wendyos-update's
// stdout, e.g. {"phase":"write","percent":42,"msg":"..."}.
type wendyOSProgressLine struct {
	Phase   string `json:"phase"`
	Percent int32  `json:"percent"`
}

// parseWendyOSProgress decodes one stdout line into (phase, percent). It
// returns ok=false for blank lines, non-JSON lines, the `status --json` object
// (no phase), and malformed JSON, so callers can scan stdout uniformly.
func parseWendyOSProgress(line string) (string, int32, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return "", 0, false
	}
	var p wendyOSProgressLine
	if err := json.Unmarshal([]byte(line), &p); err != nil {
		return "", 0, false
	}
	if p.Phase == "" {
		return "", 0, false
	}
	return p.Phase, p.Percent, true
}

// wendyOSInstallErrorMessage builds the user-facing error for a failed
// wendyos-update install and appends the captured tail of the tool's stderr.
// install rejects an artifact (incompatible device, bad checksum/digest, size
// mismatch, or malformed) with exit 3; every other failure uses the generic
// message. Exit 4 ("verify failed") is a commit-time code that install never
// emits — the gate's commit path handles it — so it is not special-cased here.
func wendyOSInstallErrorMessage(exitCode int, tail []string) string {
	reason := "wendyos-update install failed"
	if exitCode == 3 {
		reason = "wendyos-update install failed: the artifact was rejected (incompatible device, bad checksum, or malformed)"
	}
	if len(tail) > 0 {
		return reason + "\nwendyos-update output:\n" + strings.Join(tail, "\n")
	}
	return reason
}
