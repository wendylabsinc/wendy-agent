package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// menderUpdater drives the mender-update client, kept as the fallback OS A/B
// update backend for devices the in-house wendyos-update engine does not yet
// support. It owns the Jetson rootfs-A/B EFI-variable glue (enableJetsonRootfsAB);
// for the wendyos-update path that is handled by its platform connector.
type menderUpdater struct {
	logger *zap.Logger
}

func newMenderUpdater(logger *zap.Logger) menderUpdater {
	return menderUpdater{logger: logger}
}

func (m menderUpdater) name() string { return updaterNameMender }

// delegatesHealthcheck is false: mender has no /etc/wendyos-update/health.d, so
// the agent's boot-time gate keeps owning the post-reboot healthchecks for it.
func (m menderUpdater) delegatesHealthcheck() bool { return false }

func (m menderUpdater) commitCommand() string { return "mender-update" }

func (m menderUpdater) available() bool {
	_, found := resolveMenderBinary()
	return found
}

// detect for mender is just binary presence: mender has no per-board connector
// probe, so a present binary means it can drive any WendyOS mender target.
func (m menderUpdater) detect() bool { return m.available() }

func (m menderUpdater) install(ctx context.Context, artifactURL string, onProgress func(string, int32)) error {
	if err := enableJetsonRootfsAB(m.logger); err != nil {
		return fmt.Errorf("Jetson A/B setup failed: %v", err)
	}

	onProgress("downloading", 0)
	cmdName, found := resolveMenderBinary()
	if !found {
		return errors.New("mender-update binary not found")
	}

	cmd := exec.CommandContext(ctx, cmdName, "install", artifactURL)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start mender: %v", err)
	}

	// Stream progress by scanning mender's output in real time. Mender writes
	// structured log lines to stderr; stdout may carry additional info. We
	// merge both and parse for phase transitions and percentage patterns.
	//
	// Download progress occupies 0-80% of the overall bar; install 80-95%;
	// 95-100% is reserved for finalization (sent by the caller).
	phase := "downloading"
	lastPercent := int32(0)

	// Retain the tail of mender's output so a non-zero exit can report the real
	// cause (e.g. an incompatible device type) instead of a bare "exit status 1".
	outputTail := newLineRing(menderErrorTailLines)

	scanLines := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			outputTail.push(line)
			lower := strings.ToLower(line)
			m.logger.Debug("mender output", zap.String("line", line))

			switch {
			case strings.Contains(lower, "installing") || strings.Contains(lower, "writing artifact"):
				if phase != "installing" {
					phase = "installing"
					onProgress(phase, 80)
					lastPercent = 80
				}
			case strings.Contains(lower, "download complete") || strings.Contains(lower, "download finished"):
				if phase == "downloading" {
					onProgress("downloading", 80)
					lastPercent = 80
				}
			}

			if mt := menderProgressRe.FindStringSubmatch(line); len(mt) > 1 {
				if pct, perr := strconv.Atoi(mt[1]); perr == nil && pct >= 0 && pct <= 100 {
					var overall int32
					if phase == "downloading" {
						overall = int32(pct) * 80 / 100
					} else {
						overall = 80 + int32(pct)*15/100
					}
					if overall > lastPercent {
						lastPercent = overall
						onProgress(phase, overall)
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			m.logger.Warn("mender output scan error", zap.Error(err))
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scanLines(stderr) }()
	go func() { defer wg.Done(); scanLines(stdout) }()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		m.logger.Error("mender install failed", zap.Error(err), zap.Strings("output_tail", outputTail.tail()))
		return errors.New(formatMenderFailure(err, outputTail.tail()))
	}
	return nil
}

func (m menderUpdater) commit() oshealth.MenderResult {
	binary, found := resolveMenderBinary()
	if !found {
		return oshealth.MenderResult{Status: oshealth.MenderUnavailable}
	}
	return runUpdaterCommit(m.logger, binary, "commit")
}

func (m menderUpdater) rollback() oshealth.MenderResult {
	binary, found := resolveMenderBinary()
	if !found {
		return oshealth.MenderResult{Status: oshealth.MenderUnavailable}
	}
	return runUpdaterCommit(m.logger, binary, "rollback")
}
