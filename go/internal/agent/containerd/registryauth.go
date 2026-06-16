package containerd

import (
	"strings"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// dockerHubAliases are the hosts containerd may contact for Docker Hub images.
var dockerHubAliases = map[string]bool{
	"docker.io":            true,
	"index.docker.io":      true,
	"registry-1.docker.io": true,
}

// registryHostMatches reports whether a credential configured for `configured`
// should be presented to a request to `requested`. An empty `configured`
// matches nothing, so credentials are never presented to an unknown host.
func registryHostMatches(requested, configured string) bool {
	if configured == "" {
		return false
	}
	if strings.EqualFold(requested, configured) {
		return true
	}
	return dockerHubAliases[strings.ToLower(requested)] && dockerHubAliases[strings.ToLower(configured)]
}

// authorizerResolver builds a docker resolver that presents the supplied
// credentials only to the configured registry host. It returns nil when auth is
// empty, signalling the caller to fall back to the default anonymous resolver.
func authorizerResolver(auth *agentpb.RegistryAuth) remotes.Resolver {
	if auth == nil || (auth.GetUsername() == "" && auth.GetPassword() == "") {
		return nil
	}
	configured := auth.GetRegistryHost()
	authorizer := docker.NewDockerAuthorizer(docker.WithAuthCreds(func(host string) (string, string, error) {
		if registryHostMatches(host, configured) {
			return auth.GetUsername(), auth.GetPassword(), nil
		}
		return "", "", nil
	}))
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: docker.ConfigureDefaultRegistries(docker.WithAuthorizer(authorizer)),
	})
}
