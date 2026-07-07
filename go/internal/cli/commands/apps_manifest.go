package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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

// secretRefPattern matches ${secret:NAME} tokens in manifest env values.
var secretRefPattern = regexp.MustCompile(`\$\{secret:([a-zA-Z0-9_]+)\}`)

// generateSecrets returns one random 32-hex-character value per (deduplicated)
// name. Values are stable within a single install so services sharing a secret
// name receive the same value.
func generateSecrets(names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	for _, name := range names {
		if _, ok := out[name]; ok {
			continue
		}
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generating secret %q: %w", name, err)
		}
		out[name] = hex.EncodeToString(buf)
	}
	return out, nil
}

// substituteSecrets replaces every ${secret:NAME} token in the env values with
// the corresponding generated secret. It errors if a referenced name has no
// generated value (a manifest bug). The input map is not modified.
func substituteSecrets(env map[string]string, secrets map[string]string) (map[string]string, error) {
	if env == nil {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		var subErr error
		out[k] = secretRefPattern.ReplaceAllStringFunc(v, func(match string) string {
			name := secretRefPattern.FindStringSubmatch(match)[1]
			val, ok := secrets[name]
			if !ok {
				subErr = fmt.Errorf("env %q references unknown secret %q", k, name)
				return match
			}
			return val
		})
		if subErr != nil {
			return nil, subErr
		}
	}
	return out, nil
}
