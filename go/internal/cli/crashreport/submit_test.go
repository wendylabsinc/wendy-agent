package crashreport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"anonymous_id":"anon-9"`) {
			t.Errorf("missing anonymous_id: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tracking_id":"WDY-ABC123","status_url":"https://x/WDY-ABC123"}`))
	}))
	defer srv.Close()

	res, err := SubmitHTTP(context.Background(), srv.URL, "anon-9", Bundle{ErrorClass: "other"}, true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TrackingID != "WDY-ABC123" || res.StatusURL == "" {
		t.Errorf("bad result: %+v", res)
	}
}

func TestSubmitHTTPFallsBackToFile(t *testing.T) {
	// Unreachable endpoint → local-file fallback, nil error.
	res, err := SubmitHTTP(context.Background(), "http://127.0.0.1:0", "anon-9", Bundle{ErrorClass: "other"}, false)
	if err != nil {
		t.Fatalf("fallback must not error: %v", err)
	}
	if res.LocalFile == "" || res.TrackingID != "" {
		t.Errorf("expected local-file fallback, got %+v", res)
	}
}
