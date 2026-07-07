package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveAppManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/immich/manifest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"app_id":"immich","secrets":["db_password"],
			"services":[
			  {"name":"postgres","image":"ghcr.io/immich-app/postgres:14",
			   "env":{"POSTGRES_PASSWORD":"${secret:db_password}"},
			   "volumes":[{"name":"pg","path":"/var/lib/postgresql/data"}]},
			  {"name":"server","image":"ghcr.io/immich-app/immich-server:release",
			   "env":{"DB_HOSTNAME":"postgres","DB_PASSWORD":"${secret:db_password}"},
			   "ports":[{"host":2283,"container":2283,"proto":"tcp"}],
			   "dependsOn":["postgres"]}
			]}`))
	}))
	defer srv.Close()

	m, err := resolveAppManifest(context.Background(), srv.URL, "immich")
	if err != nil {
		t.Fatalf("resolveAppManifest: %v", err)
	}
	if m.AppID != "immich" || len(m.Services) != 2 {
		t.Fatalf("got app_id=%q services=%d", m.AppID, len(m.Services))
	}
	if m.Services[1].Ports[0].Host != 2283 {
		t.Fatalf("server host port = %d, want 2283", m.Services[1].Ports[0].Host)
	}
	if got := m.Services[0].Env["POSTGRES_PASSWORD"]; got != "${secret:db_password}" {
		t.Fatalf("env not preserved: %q", got)
	}
}

func TestResolveAppManifestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := resolveAppManifest(context.Background(), srv.URL, "nope"); err == nil {
		t.Fatal("expected error for 404")
	}
}
