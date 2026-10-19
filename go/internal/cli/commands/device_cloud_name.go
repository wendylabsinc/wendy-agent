package commands

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

var connectUnresolvedCloudNameFn = connectToCloudAgentExpecting

// An explicit Cloud display name or UUID need not exist in local DNS. After
// local name resolution fails, look up that same selection in Cloud. Addresses
// and typed transport selectors keep their original connection semantics.
func connectUnresolvedCloudName(ctx context.Context, device string, cause error, opts ...resolveOption) (*grpcclient.AgentConnection, error, bool) {
	if device == "" || strings.ContainsAny(device, ".:/[]") || net.ParseIP(device) != nil ||
		cause == nil || ctx.Err() != nil || cloudFallbackDisabled("") ||
		stopConnectionRecovery(cause) || errors.Is(cause, errProvisionedAgentUnauthorized) ||
		errors.Is(cause, errTLSHandshakeRejected) || !isResolverMiss(cause) {
		return nil, cause, false
	}
	if _, cloud := cloudDeviceConfigFromContext(ctx); cloud {
		return nil, cause, false
	}
	cfg, err := config.Load()
	if err != nil || len(cfg.Auth) == 0 {
		return nil, cause, false
	}
	expected := expectedIdentityFor(device)
	conn, err := connectUnresolvedCloudNameFn(ctx, "", cloudDefaultSelector(device, expected), "", expected)
	if err != nil {
		// Cloud's not-found, offline and duplicate-name errors name the next
		// useful action. Do not replace them with the earlier DNS failure.
		return nil, err, true
	}
	options := resolveConfig{excludeProviderKeys: make(map[string]bool)}
	for _, option := range opts {
		option(&options)
	}
	if !options.suppressUpdateCheck && !options.readOnlyMonitoring {
		conn, err = checkAndOfferUpdateFn(ctx, conn)
	}
	return conn, err, true
}
