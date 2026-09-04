package commands

import (
	"context"
	"errors"
	"io"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// runLogSubscription adds native OTLP application logs to an attached
// `wendy run`. Container stdout and stderr continue to travel over the
// container RPC and are written raw by their existing code paths.
type runLogSubscription struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// runCycleOwnsLogSubscription reports whether an individual deploy cycle
// should subscribe to native OTel logs. Attached watch owns one subscription
// for the whole session so preserved services remain visible between cycles;
// opening another here would render every native record twice.
func runCycleOwnsLogSubscription(opts runOptions) bool {
	return !opts.deploy && !opts.detach && !opts.isWatch()
}

func startRunLogSubscription(ctx context.Context, conn *grpcclient.AgentConnection, appName string, output io.Writer, onError func(error)) *runLogSubscription {
	if conn == nil || conn.TelemetryService == nil || appName == "" {
		return nil
	}

	streamCtx, cancel := context.WithCancel(ctx)
	lastN := int32(0)
	stream, err := conn.TelemetryService.StreamLogs(streamCtx, &agentpb.StreamLogsRequest{
		AppName: &appName,
		LastN:   &lastN,
	})
	if err != nil {
		cancel()
		if onError != nil {
			onError(err)
		}
		return nil
	}

	sub := &runLogSubscription{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(sub.done)
		for {
			resp, recvErr := stream.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) && streamCtx.Err() == nil && onError != nil {
					onError(recvErr)
				}
				return
			}
			writeRunLogResponse(output, resp)
		}
	}()
	return sub
}

func (s *runLogSubscription) stop() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// writeRunLogResponse writes only native application records. The agent also
// adapts stdout/stderr into OTLP under the wendy.container scope; those records
// are deliberately skipped because the container RPC already preserves and
// displays their original bytes and stream identity.
func writeRunLogResponse(w io.Writer, resp *agentpb.StreamLogsResponse) {
	if resp == nil || resp.GetIsHistory() {
		return
	}
	logs := resp.GetLogs()
	if logs == nil {
		return
	}
	for _, rl := range logs.GetResourceLogs() {
		service := resourceServiceName(rl.GetResource())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				if isContainerStreamLog(sl.GetScope(), lr) {
					continue
				}
				writeLogRecord(w, service, lr)
			}
		}
	}
}

func isContainerStreamLog(scope *commonpb.InstrumentationScope, record *logspb.LogRecord) bool {
	if scope.GetName() != "wendy.container" {
		return false
	}
	for _, attr := range record.GetAttributes() {
		if attr.GetKey() != "stream" {
			continue
		}
		switch attr.GetValue().GetStringValue() {
		case "stdout", "stderr":
			return true
		}
	}
	return false
}

func runLogStreamWarning(err error) {
	cliNotice("Notice: native application logs unavailable (%v)", err)
}
