package crashreport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("anonymous_id") != "anon-1" {
			t.Errorf("missing anonymous_id: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"fixed":[{"tracking_id":"WDY-ABC123","fixed_in_release":"v1.4.0"}]}`))
	}))
	defer srv.Close()
	got, err := FetchStatus(context.Background(), srv.URL, "anon-1")
	if err != nil || len(got) != 1 || got[0].TrackingID != "WDY-ABC123" || got[0].FixedInRelease != "v1.4.0" {
		t.Fatalf("FetchStatus = %+v, err=%v", got, err)
	}
}
