package crashreport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/protobuf/encoding/protojson"
)

// Result describes the outcome of a submission. Exactly one of TrackingID or
// LocalFile is set.
type Result struct {
	TrackingID string
	StatusURL  string
	LocalFile  string
}

// Submit sends the bundle to the cloud. On any failure (including a nil client,
// an offline endpoint, or an Unimplemented server before the cloud side ships),
// it writes the bundle to a local file and returns that path with a nil error —
// the crash-reporter must never produce a secondary error.
func Submit(ctx context.Context, client cloudpb.DiagnosticsServiceClient, b Bundle) (Result, error) {
	if client != nil {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		resp, err := client.SubmitReport(callCtx, b.Request())
		if err == nil && resp.GetTrackingId() != "" {
			return Result{TrackingID: resp.GetTrackingId(), StatusURL: resp.GetStatusUrl()}, nil
		}
	}
	path, ferr := writeLocalBundle(b)
	if ferr != nil {
		return Result{}, ferr
	}
	return Result{LocalFile: path}, nil
}

// Subscribe registers interest in a report's fix.
func Subscribe(ctx context.Context, client cloudpb.DiagnosticsServiceClient, trackingID, apnsToken, topic string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no cloud connection available to subscribe")
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.Subscribe(callCtx, &cloudpb.SubscribeRequest{
		TrackingId: trackingID, ApnsDeviceToken: apnsToken, Topic: topic,
	})
	if err != nil {
		return "", err
	}
	return resp.GetSubscriptionId(), nil
}

// writeLocalBundle writes the redacted bundle as JSON to a temp file the user
// can attach to a GitHub issue.
func writeLocalBundle(b Bundle) (string, error) {
	dir, err := os.MkdirTemp("", "wendy-crashreport-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.json")
	data, err := protojson.Marshal(b.Request())
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
