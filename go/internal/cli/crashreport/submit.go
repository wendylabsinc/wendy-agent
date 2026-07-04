package crashreport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Result describes the outcome of a submission. Exactly one of TrackingID or
// LocalFile is set.
type Result struct {
	TrackingID string
	StatusURL  string
	LocalFile  string
}

// SubmitHTTP POSTs the redacted bundle as JSON to the telemetry crashreports
// endpoint. On any failure it writes the same JSON to a local file and returns
// that path with a nil error — the reporter never produces a secondary error.
func SubmitHTTP(ctx context.Context, endpoint, anonymousID string, b Bundle, notifyOnFix bool) (Result, error) {
	payload := b.Payload(anonymousID, notifyOnFix)
	body, err := json.Marshal(payload)
	if err == nil && endpoint != "" {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, rerr := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
		if rerr == nil {
			req.Header.Set("Content-Type", "application/json")
			resp, derr := http.DefaultClient.Do(req)
			if derr == nil {
				defer resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					var out struct {
						TrackingID string `json:"tracking_id"`
						StatusURL  string `json:"status_url"`
					}
					if json.NewDecoder(resp.Body).Decode(&out) == nil && out.TrackingID != "" {
						return Result{TrackingID: out.TrackingID, StatusURL: out.StatusURL}, nil
					}
				}
			}
		}
	}
	path, ferr := writeLocalBundleJSON(body)
	if ferr != nil {
		return Result{}, ferr
	}
	return Result{LocalFile: path}, nil
}

func writeLocalBundleJSON(body []byte) (string, error) {
	dir, err := os.MkdirTemp("", "wendy-crashreport-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
