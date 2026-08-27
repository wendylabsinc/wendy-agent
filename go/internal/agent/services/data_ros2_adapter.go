package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	"golang.org/x/sys/unix"
)

type ros2DataAdapter struct{ service *ROS2Service }

func newROS2DataAdapter(service *ROS2Service) dataCaptureAdapter {
	return &ros2DataAdapter{service: service}
}

func ros2DataSource(sc ros2SC) data.Source {
	rmw := sc.rmw
	if rmw == "" {
		rmw = "default"
	}
	return data.Source{ID: fmt.Sprintf("ros2:%s:domain-%d", safeCaptureName(rmw), sc.domainID), Kind: "ros2", ClockDomain: "ROSBAG2_STORAGE/ROS_MESSAGE_HEADER/SIM_TIME", Healthy: true, Detail: fmt.Sprintf("%s DDS domain %d", rmw, sc.domainID)}
}

func (a *ros2DataAdapter) Discover(ctx context.Context) []data.Source {
	scs, err := a.service.resolveSidecars(ctx, nil)
	if err != nil {
		return nil
	}
	out := make([]data.Source, 0, len(scs))
	for _, sc := range scs {
		out = append(out, ros2DataSource(sc))
	}
	return out
}

func (a *ros2DataAdapter) Start(ctx context.Context, session data.CaptureSession, selected []data.Source) (runningDataCapture, error) {
	wanted := make(map[string]data.Source)
	for _, source := range selected {
		if strings.HasPrefix(source.ID, "ros2:") {
			wanted[source.ID] = source
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	scs, err := a.service.resolveSidecars(ctx, nil)
	if err != nil {
		return nil, err
	}
	group := &ros2CaptureGroup{}
	for _, sc := range scs {
		source := ros2DataSource(sc)
		if _, ok := wanted[source.ID]; !ok {
			continue
		}
		capture, err := a.startOne(ctx, session, source, sc)
		if err != nil {
			_, _ = group.Stop(context.Background())
			return nil, fmt.Errorf("%s: %w", source.ID, err)
		}
		group.captures = append(group.captures, capture)
		delete(wanted, source.ID)
	}
	if len(wanted) != 0 {
		_, _ = group.Stop(context.Background())
		return nil, errors.New("ROS 2 graph changed during recorder startup")
	}
	return group, nil
}

func (a *ros2DataAdapter) startOne(ctx context.Context, session data.CaptureSession, source data.Source, sc ros2SC) (*ros2Capture, error) {
	name := safeCaptureName("wendy-" + session.ID + "-" + source.ID)
	staging := filepath.Join(a.service.bagDir, name)
	destination := filepath.Join(session.Directory, "ros2", safeCaptureName(source.ID))
	if _, err := os.Stat(staging); err == nil {
		return nil, fmt.Errorf("staging bag %s already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return nil, err
	}
	clockFile, err := os.OpenFile(filepath.Join(session.Directory, "ros2", safeCaptureName(source.ID)+"-clock_samples.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	recordCtx, cancel := context.WithCancel(context.Background())
	c := &ros2Capture{service: a.service, session: session, source: source, sc: sc, staging: staging, destination: destination, clockFile: clockFile, ctx: recordCtx, cancel: cancel, recordDone: make(chan ros2ExecResult, 1), samplerDone: make(chan struct{})}
	go func() {
		var output bytes.Buffer
		code, execErr := a.service.runtime.ExecROS2(recordCtx, ROS2ExecOptions{DomainID: sc.domainID, SidecarName: sc.name, Args: []string{"bag", "record", "-o", staging, "-a"}}, &output, &output)
		c.recordDone <- ros2ExecResult{code: code, err: execErr, output: output.String()}
	}()
	go c.sampleClocks()

	select {
	case result := <-c.recordDone:
		cancel()
		<-c.samplerDone
		clockFile.Close()
		return nil, fmt.Errorf("rosbag2 exited before recording (code %d): %s", result.code, summarizeROS2Output(result.err, result.output))
	case <-ctx.Done():
		cancel()
		result := <-c.recordDone
		<-c.samplerDone
		clockFile.Close()
		return nil, errors.Join(ctx.Err(), result.err)
	case <-time.After(750 * time.Millisecond):
		return c, nil
	}
}

type ros2ExecResult struct {
	code   int
	err    error
	output string
}

type ros2CaptureGroup struct{ captures []*ros2Capture }

func (g *ros2CaptureGroup) Stop(ctx context.Context) ([]data.CaptureResult, error) {
	var out []data.CaptureResult
	var errs []error
	for i := len(g.captures) - 1; i >= 0; i-- {
		r, err := g.captures[i].Stop(ctx)
		out = append(out, r...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

type ros2Capture struct {
	service              *ROS2Service
	session              data.CaptureSession
	source               data.Source
	sc                   ros2SC
	staging, destination string
	clockFile            *os.File
	clockMu              sync.Mutex
	ctx                  context.Context
	cancel               context.CancelFunc
	recordDone           chan ros2ExecResult
	samplerDone          chan struct{}
	stopOnce             sync.Once
	stopResult           []data.CaptureResult
	stopErr              error
	samples              uint64
	maxError             int64
	discontinuities      uint64
	simClock             bool
}

func (c *ros2Capture) writeClockSample(value any) {
	b, _ := json.Marshal(value)
	c.clockMu.Lock()
	if _, err := c.clockFile.Write(append(b, '\n')); err == nil {
		c.samples++
	}
	c.clockMu.Unlock()
}

func (c *ros2Capture) sampleClocks() {
	defer close(c.samplerDone)
	// Presence of /clock changes the interpretation of message header stamps,
	// but never turns them into UTC. Preserve its raw sequence with receipt time.
	if topics, err := c.service.runIn(c.ctx, c.sc, "topic", "list"); err == nil {
		for _, topic := range strings.Fields(topics) {
			if topic == "/clock" {
				c.simClock = true
				break
			}
		}
	}
	var simDone chan struct{}
	if c.simClock && c.ctx.Err() == nil {
		simDone = make(chan struct{})
		go func() {
			defer close(simDone)
			writer := &rosClockWriter{capture: c}
			_, _ = c.service.runtime.ExecROS2(c.ctx, ROS2ExecOptions{DomainID: c.sc.domainID, SidecarName: c.sc.name, Args: []string{"topic", "echo", "/clock"}}, writer, io.Discard)
		}()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sample, err := data.CaptureUTCClockSample()
		if err == nil {
			errNanos := (sample.BootAfterNanos - sample.BootBeforeNanos + 1) / 2
			if errNanos > c.maxError {
				c.maxError = errNanos
			}
			c.writeClockSample(struct {
				Kind         string           `json:"kind"`
				EpisodeNanos int64            `json:"episode_nanos"`
				Sample       data.ClockSample `json:"sample"`
			}{"host_realtime_sandwich", sample.BootBeforeNanos + (sample.BootAfterNanos-sample.BootBeforeNanos)/2 - c.session.RequestBootNanos, sample})
		}
		select {
		case <-c.ctx.Done():
			if simDone != nil {
				<-simDone
			}
			return
		case <-ticker.C:
		}
	}
}

type rosClockWriter struct {
	capture  *ros2Capture
	mu       sync.Mutex
	buf      string
	sec      int64
	haveSec  bool
	last     int64
	haveLast bool
}

func (w *rosClockWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		if strings.HasPrefix(line, "sec:") {
			w.sec, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "sec:")), 10, 64)
			w.haveSec = true
		}
		if strings.HasPrefix(line, "nanosec:") && w.haveSec {
			nsec, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "nanosec:")), 10, 64)
			if err != nil {
				continue
			}
			stamp := w.sec*int64(time.Second) + nsec
			if w.haveLast && stamp < w.last {
				w.capture.discontinuities++
			}
			w.last, w.haveLast = stamp, true
			_, receipt, _, _ := data.CaptureReceipt()
			w.capture.writeClockSample(struct {
				Kind         string `json:"kind"`
				EpisodeNanos int64  `json:"episode_nanos"`
				SimTimeNanos int64  `json:"sim_time_nanos"`
			}{"ros_clock", receipt - w.capture.session.RequestBootNanos, stamp})
		}
	}
	return len(p), nil
}

func (c *ros2Capture) Stop(context.Context) ([]data.CaptureResult, error) {
	c.stopOnce.Do(func() {
		c.cancel()
		execResult := <-c.recordDone
		<-c.samplerDone
		c.clockMu.Lock()
		_ = c.clockFile.Sync()
		_ = c.clockFile.Close()
		c.clockMu.Unlock()
		if execResult.err != nil && !strings.Contains(execResult.err.Error(), context.Canceled.Error()) {
			c.stopErr = fmt.Errorf("rosbag2 stopped with code %d: %s", execResult.code, summarizeROS2Output(execResult.err, execResult.output))
		}
		if _, statErr := os.Stat(c.staging); statErr != nil {
			c.stopErr = errors.Join(c.stopErr, fmt.Errorf("rosbag2 output missing: %w", statErr))
		} else if moveErr := moveCaptureDirectory(c.staging, c.destination); moveErr != nil {
			c.stopErr = errors.Join(c.stopErr, moveErr)
		}
		mapping := data.ClockMapping{ID: "ros-host-realtime-sandwich-1", SourceClockDomain: "ROSBAG2_STORAGE_TIME", CanonicalClock: "CLOCK_BOOTTIME", MaxErrorNanos: c.maxError, Samples: c.samples, Algorithm: "sampled-realtime-boottime-sandwich-v1"}
		if c.simClock {
			mapping.Discontinuity = "ROS /clock retained as an independent, potentially resetting domain"
		}
		c.stopResult = []data.CaptureResult{{SourceID: c.source.ID, DropAccounting: "unavailable", Discontinuities: c.discontinuities, Mappings: []data.ClockMapping{mapping}}}
	})
	return c.stopResult, c.stopErr
}

func summarizeROS2Output(err error, output string) string {
	message := strings.TrimSpace(output)
	if len(message) > 512 {
		message = message[:512]
	}
	if message != "" {
		return message
	}
	if err != nil {
		return err.Error()
	}
	return "no diagnostic output"
}

func moveCaptureDirectory(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, unix.EXDEV) {
		return err
	}
	if err := copyCaptureDirectory(source, destination); err != nil {
		return err
	}
	return os.RemoveAll(source)
}

func copyCaptureDirectory(source, destination string) error {
	if err := os.Mkdir(destination, 0o750); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.Mkdir(target, 0o750)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rosbag contains non-regular entry %s", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		if syncErr := out.Sync(); copyErr == nil {
			copyErr = syncErr
		}
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		in.Close()
		return copyErr
	})
}
