package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
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

func TestSubstituteSecretsSharedValue(t *testing.T) {
	secrets, err := generateSecrets([]string{"db_password"})
	if err != nil {
		t.Fatalf("generateSecrets: %v", err)
	}
	pg, err := substituteSecrets(map[string]string{"POSTGRES_PASSWORD": "${secret:db_password}"}, secrets)
	if err != nil {
		t.Fatalf("substituteSecrets pg: %v", err)
	}
	srv, err := substituteSecrets(map[string]string{"DB_PASSWORD": "${secret:db_password}", "DB_HOSTNAME": "postgres"}, secrets)
	if err != nil {
		t.Fatalf("substituteSecrets srv: %v", err)
	}
	if pg["POSTGRES_PASSWORD"] == "${secret:db_password}" || pg["POSTGRES_PASSWORD"] == "" {
		t.Fatalf("secret not substituted: %q", pg["POSTGRES_PASSWORD"])
	}
	if pg["POSTGRES_PASSWORD"] != srv["DB_PASSWORD"] {
		t.Fatalf("shared secret differs across services: %q vs %q", pg["POSTGRES_PASSWORD"], srv["DB_PASSWORD"])
	}
	if srv["DB_HOSTNAME"] != "postgres" {
		t.Fatalf("non-secret value mangled: %q", srv["DB_HOSTNAME"])
	}
}

func TestSubstituteSecretsMissingName(t *testing.T) {
	if _, err := substituteSecrets(map[string]string{"X": "${secret:absent}"}, map[string]string{}); err == nil {
		t.Fatal("expected error for missing secret name")
	}
}

func TestBuildServiceInstallMultiService(t *testing.T) {
	m := appManifest{
		AppID:   "immich",
		Secrets: []string{"db_password"},
		Services: []manifestService{
			{Name: "server", Image: "ghcr.io/immich-app/immich-server:release",
				Env:       map[string]string{"DB_PASSWORD": "${secret:db_password}", "DB_HOSTNAME": "postgres"},
				Volumes:   []manifestVolume{{Name: "upload", Path: "/data"}},
				Ports:     []manifestPort{{Host: 2283, Container: 2283, Proto: "tcp"}},
				DependsOn: []string{"postgres"}},
			{Name: "postgres", Image: "ghcr.io/immich-app/postgres:14",
				Env:     map[string]string{"POSTGRES_PASSWORD": "${secret:db_password}"},
				Volumes: []manifestVolume{{Name: "pg", Path: "/var/lib/postgresql/data"}}},
		},
	}
	order, reqs, err := buildServiceInstall(m)
	if err != nil {
		t.Fatalf("buildServiceInstall: %v", err)
	}
	// postgres has no deps; server depends on postgres -> postgres first.
	if len(order) != 2 || order[0] != "postgres" || order[1] != "server" {
		t.Fatalf("topo order = %v, want [postgres server]", order)
	}

	srv := reqs["server"]
	if srv.AppName != "immich_server" {
		t.Fatalf("server AppName = %q, want immich_server", srv.AppName)
	}
	if srv.ImageName != "ghcr.io/immich-app/immich-server:release" {
		t.Fatalf("server image = %q", srv.ImageName)
	}
	// Env carries the substituted secret and the literal hostname.
	var dbPass, dbHost string
	for _, kv := range srv.Env {
		if strings.HasPrefix(kv, "DB_PASSWORD=") {
			dbPass = strings.TrimPrefix(kv, "DB_PASSWORD=")
		}
		if strings.HasPrefix(kv, "DB_HOSTNAME=") {
			dbHost = strings.TrimPrefix(kv, "DB_HOSTNAME=")
		}
	}
	if dbHost != "postgres" {
		t.Fatalf("DB_HOSTNAME = %q, want postgres", dbHost)
	}
	if dbPass == "" || strings.Contains(dbPass, "${secret") {
		t.Fatalf("DB_PASSWORD not substituted: %q", dbPass)
	}

	// AppConfig: isolated, ServiceName set, Services fully populated, port+volume entitlements.
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(srv.AppConfig, &cfg); err != nil {
		t.Fatalf("unmarshal AppConfig: %v", err)
	}
	if cfg.Isolation != "isolated" {
		t.Fatalf("isolation = %q, want isolated", cfg.Isolation)
	}
	if cfg.ServiceName != "server" {
		t.Fatalf("ServiceName = %q", cfg.ServiceName)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("Services len = %d, want 2 (agent needs >1 to inject /etc/hosts)", len(cfg.Services))
	}
	var haveNet, havePersist bool
	for _, e := range cfg.Entitlements {
		if e.Type == appconfig.EntitlementNetwork && len(e.Ports) == 1 && e.Ports[0].Host == 2283 {
			haveNet = true
		}
		if e.Type == appconfig.EntitlementPersist && e.Name == "upload" && e.Path == "/data" {
			havePersist = true
		}
	}
	if !haveNet || !havePersist {
		t.Fatalf("missing entitlements: net=%v persist=%v", haveNet, havePersist)
	}

	// The shared secret is identical in postgres.
	var pgPass string
	for _, kv := range reqs["postgres"].Env {
		if strings.HasPrefix(kv, "POSTGRES_PASSWORD=") {
			pgPass = strings.TrimPrefix(kv, "POSTGRES_PASSWORD=")
		}
	}
	if pgPass != dbPass {
		t.Fatalf("shared secret differs: server=%q postgres=%q", dbPass, pgPass)
	}
}

func TestBuildServiceInstallSingleService(t *testing.T) {
	m := appManifest{
		AppID:    "jellyfin",
		Services: []manifestService{{Name: "jellyfin", Image: "docker.io/jellyfin/jellyfin:latest"}},
	}
	order, reqs, err := buildServiceInstall(m)
	if err != nil {
		t.Fatalf("buildServiceInstall: %v", err)
	}
	if len(order) != 1 {
		t.Fatalf("order = %v", order)
	}
	r := reqs["jellyfin"]
	if r.AppName != "jellyfin" {
		t.Fatalf("AppName = %q, want jellyfin (plain, no service suffix)", r.AppName)
	}
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(r.AppConfig, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Isolation != "" || cfg.ServiceName != "" {
		t.Fatalf("single service must not set isolation/serviceName: iso=%q svc=%q", cfg.Isolation, cfg.ServiceName)
	}
}
