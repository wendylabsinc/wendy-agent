package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const macOSBetaUnsupportedSuffix = "is not available in the current Wendy Agent for macOS beta."

func macOSBetaUnsupportedFeatureError(ctx context.Context, agent agentpb.WendyAgentServiceClient, err error, feature string) error {
	if agent == nil || status.Code(err) != codes.Unimplemented {
		return nil
	}

	resp, versionErr := agent.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if versionErr != nil || !strings.EqualFold(resp.GetOs(), "darwin") {
		return nil
	}

	return fmt.Errorf("%s %s", feature, macOSBetaUnsupportedSuffix)
}
