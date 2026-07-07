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
	"sort"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
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

// buildServiceInstall turns a resolved manifest into an ordered set of
// per-service CreateContainerRequests. Services are ordered so every service
// follows its dependsOn entries (create+start must happen in this order so each
// service's /etc/hosts already resolves its dependencies). Secrets are generated
// once and shared across services by name.
func buildServiceInstall(m appManifest) ([]string, map[string]*agentpb.CreateContainerRequest, error) {
	secrets, err := generateSecrets(m.Secrets)
	if err != nil {
		return nil, nil, err
	}

	// Build the appconfig.ServiceConfig map for topo ordering and for the agent's
	// group awareness (len(Services) > 1 triggers /etc/hosts injection).
	svcConfigs := make(map[string]*appconfig.ServiceConfig, len(m.Services))
	byName := make(map[string]manifestService, len(m.Services))
	for _, s := range m.Services {
		svcConfigs[s.Name] = &appconfig.ServiceConfig{DependsOn: s.DependsOn}
		byName[s.Name] = s
	}

	order, err := appconfig.ServiceTopoOrder(svcConfigs)
	if err != nil {
		return nil, nil, err
	}

	multi := len(m.Services) > 1
	reqs := make(map[string]*agentpb.CreateContainerRequest, len(m.Services))
	for _, name := range order {
		svc := byName[name]

		env, err := substituteSecrets(svc.Env, secrets)
		if err != nil {
			return nil, nil, err
		}
		envList := make([]string, 0, len(env))
		for k, v := range env {
			envList = append(envList, k+"="+v)
		}
		sort.Strings(envList) // stable order for reproducible requests/tests

		var entitlements []appconfig.Entitlement
		if len(svc.Ports) > 0 {
			ports := make([]appconfig.PortMapping, 0, len(svc.Ports))
			for _, p := range svc.Ports {
				ports = append(ports, appconfig.PortMapping{Host: p.Host, Container: p.Container})
			}
			entitlements = append(entitlements, appconfig.Entitlement{
				Type:  appconfig.EntitlementNetwork,
				Ports: ports,
			})
		}
		for _, v := range svc.Volumes {
			entitlements = append(entitlements, appconfig.Entitlement{
				Type: appconfig.EntitlementPersist,
				Name: v.Name,
				Path: v.Path,
			})
		}

		cfg := &appconfig.AppConfig{
			AppID:        m.AppID,
			Entitlements: entitlements,
		}
		if multi {
			cfg.ServiceName = name
			cfg.Isolation = "isolated"
			cfg.Services = svcConfigs
		}

		cfgData, err := json.Marshal(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("marshaling config for service %s: %w", name, err)
		}

		reqs[name] = &agentpb.CreateContainerRequest{
			ImageName:     normalizeImageRef(svc.Image),
			AppName:       cfg.ContainerName(),
			AppConfig:     cfgData,
			Env:           envList,
			RestartPolicy: &agentpb.RestartPolicy{Mode: agentpb.RestartPolicyMode_UNLESS_STOPPED},
		}
	}
	return order, reqs, nil
}
