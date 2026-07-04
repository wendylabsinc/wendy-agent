package crashreport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// FixedReport is a tracking id whose crash was fixed in a given release.
type FixedReport struct {
	TrackingID     string `json:"tracking_id"`
	FixedInRelease string `json:"fixed_in_release"`
}

// FetchStatus asks the telemetry status endpoint which of this install's
// subscribed reports are now fixed. Best-effort; returns nil on any failure.
func FetchStatus(ctx context.Context, endpoint, anonymousID string) ([]FixedReport, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	u := endpoint + "?anonymous_id=" + url.QueryEscape(anonymousID)
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}
	var out struct {
		Fixed []FixedReport `json:"fixed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Fixed, nil
}
