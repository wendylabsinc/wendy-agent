package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// appManifest is the JSON returned by GET /v1/apps/{app_id}/manifest. A manifest
// describes one or more services that together make up an installable app.
type appManifest struct {
	AppID    string            `json:"app_id"`
	Secrets  []string          `json:"secrets,omitempty"`
	Services []manifestService `json:"services"`
}

type manifestService struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Env       map[string]string `json:"env,omitempty"`
	Volumes   []manifestVolume  `json:"volumes,omitempty"`
	Ports     []manifestPort    `json:"ports,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
}

type manifestVolume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type manifestPort struct {
	Host      uint16 `json:"host"`
	Container uint16 `json:"container"`
	Proto     string `json:"proto,omitempty"`
}

// resolveAppManifest asks the AppStore API for the multi-service manifest of an
// app id. Single-image apps come back as a one-service manifest.
func resolveAppManifest(ctx context.Context, base, appID string) (appManifest, error) {
	endpoint := fmt.Sprintf("%s/v1/apps/%s/manifest", base, url.PathEscape(appID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return appManifest{}, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return appManifest{}, fmt.Errorf("contacting the AppStore: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return appManifest{}, fmt.Errorf("app %q is not in the AppStore", appID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return appManifest{}, fmt.Errorf("AppStore returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out appManifest
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return appManifest{}, fmt.Errorf("parsing AppStore manifest: %w", err)
	}
	if len(out.Services) == 0 {
		return appManifest{}, fmt.Errorf("AppStore returned an empty manifest for %q", appID)
	}
	return out, nil
}
