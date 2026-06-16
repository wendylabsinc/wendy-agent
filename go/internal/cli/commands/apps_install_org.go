package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// dockerConfigAuth reads ~/.docker/config.json (honoring DOCKER_CONFIG) and
// returns credentials for host if present.
func dockerConfigAuth(host string) (*agentpb.RegistryAuth, bool) {
	base := os.Getenv("DOCKER_CONFIG")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		base = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(base, "config.json"))
	if err != nil {
		return nil, false
	}
	var parsed struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, false
	}
	entry, ok := parsed.Auths[host]
	if !ok {
		return nil, false
	}
	user, pass := entry.Username, entry.Password
	if entry.Auth != "" {
		if dec, derr := base64.StdEncoding.DecodeString(entry.Auth); derr == nil {
			if i := strings.IndexByte(string(dec), ':'); i >= 0 {
				user = string(dec)[:i]
				pass = string(dec)[i+1:]
			}
		}
	}
	if user == "" && pass == "" {
		return nil, false
	}
	return &agentpb.RegistryAuth{RegistryHost: host, Username: user, Password: pass}, true
}

// orgApp is an app release belonging to the logged-in org.
type orgApp struct {
	Name  string
	Image string
}

// listOrgApps returns the org's app releases from the cloud, best-effort. It
// returns (nil, nil) when the user is not logged in so the caller can still
// show the curated catalog offline.
func listOrgApps(ctx context.Context) ([]orgApp, error) {
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return nil, nil
	}
	auth := &cfg.Auth[0]
	if len(auth.Certificates) == 0 {
		return nil, nil
	}
	orgID := int32(auth.Certificates[0].OrganizationID)

	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil, fmt.Errorf("connecting to cloud: %w", err)
	}
	defer conn.Close()

	stream, err := cloudpb.NewDeploymentServiceClient(conn).ListAppReleases(ctx, &cloudpb.ListAppReleasesRequest{
		OrganizationId: orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("listing app releases: %w", err)
	}
	seen := map[string]bool{}
	var apps []orgApp
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("receiving app releases: %w", rerr)
		}
		for _, r := range resp.GetAppReleases() {
			id := r.GetAppId()
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			apps = append(apps, orgApp{Name: id})
		}
	}
	return apps, nil
}
