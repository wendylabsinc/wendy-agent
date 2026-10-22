package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/cli/cloudrequest"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// Release identity (apps spec section 1-2): a release is a tag naming one
// image digest. Both are always given, so there is never an implicit "latest".
// The contract carries one tag per release (AppRelease.name).

var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage this project's app in Wendy Cloud",
	}
	cmd.AddCommand(newAppReleaseCmd())
	return cmd
}

func newAppReleaseCmd() *cobra.Command {
	var tag, digest, cloudGRPC string
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Record a release of this project's app in Wendy Cloud",
		Long: "Records a release of the app in wendy.json: a tag naming one image digest.\n" +
			"Both --tag and --digest are required; a release is never implicitly 'latest'.\n" +
			"To record a deploy as it happens, use 'wendy run --record-release <tag>' instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateReleaseTag(tag); err != nil {
				return usageErrorf("%v", err)
			}
			if !imageDigestPattern.MatchString(digest) {
				return usageErrorf("--digest must be sha256:<64 lowercase hex>, got %q", digest)
			}
			appCfg, err := appconfig.LoadFromFile("wendy.json")
			if err != nil {
				return err
			}
			auth, err := pickAuthEntry(cloudGRPC)
			if err != nil {
				return err
			}
			if len(auth.Certificates) == 0 || auth.Certificates[0].TenantUUID() == "" {
				return fmt.Errorf("wendy app release needs an operator session; run 'wendy auth login'")
			}
			conn, err := dialCloudGRPC(auth)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := cloudContext(cmd.Context(), auth)
			if err != nil {
				return err
			}
			id, err := createAppRelease(ctx, conn, auth, auth.Certificates[0].TenantUUID(), appCfg.AppID, tag)
			if err != nil {
				return err
			}
			if err := setAppReleaseDigest(ctx, conn, auth, id, tag, digest); err != nil {
				return fmt.Errorf("release %s was created without its digest: %w", id, err)
			}
			cliLogln("Recorded release %s of %s: %s = %s", id, appCfg.AppID, tag, digest)
			return nil
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "Release tag, e.g. v1.4.0 (required)")
	cmd.Flags().StringVar(&digest, "digest", "", "Image manifest digest, sha256:<hex> (required)")
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint of the session to use")
	return cmd
}

func validateReleaseTag(tag string) error {
	if strings.TrimSpace(tag) == "" {
		return fmt.Errorf("a release tag is required (--tag); there is no implicit 'latest'")
	}
	return nil
}

func createAppRelease(ctx context.Context, conn grpc.ClientConnInterface, auth *config.AuthConfig, tenant, appID, tag string) (string, error) {
	release := new(cloudpb.AppRelease)
	err := cloudrequest.Invoke(ctx, conn, auth, cloudpb.DeploymentService_CreateAppRelease_FullMethodName,
		&cloudpb.CreateAppReleaseRequest{AppId: appID, OrganizationId: tenant, Name: tag, ReleaseType: cloudpb.ReleaseType_CONTAINER}, release)
	if err != nil {
		return "", fmt.Errorf("creating release %s of %s: %w", tag, appID, err)
	}
	return release.GetId(), nil
}

// setAppReleaseDigest records the digest. UpdateAppRelease replaces the name
// too, so the tag is sent again.
func setAppReleaseDigest(ctx context.Context, conn grpc.ClientConnInterface, auth *config.AuthConfig, id, tag, digest string) error {
	return cloudrequest.Invoke(ctx, conn, auth, cloudpb.DeploymentService_UpdateAppRelease_FullMethodName,
		&cloudpb.UpdateAppReleaseRequest{Id: id, Name: tag, ImageDigest: digest}, new(cloudpb.AppRelease))
}

// deployRelease is a release recorded for one `wendy run --record-release`:
// created before the deploy, so the container carries its id, and completed
// with the digest the agent reports when the container is created.
type deployRelease struct {
	id, tag string
	auth    *config.AuthConfig
	digest  imageDigestSink
}

// startDeployRelease creates the release in the device's own organization,
// with the operator session that deploys to it.
func startDeployRelease(ctx context.Context, conn *grpcclient.AgentConnection, appID, tag string) (*deployRelease, error) {
	if err := validateReleaseTag(tag); err != nil {
		return nil, usageErrorf("%v", err)
	}
	rel := &deployRelease{tag: tag}
	err := registerDeviceApps(ctx, deploymentProvisioningService{conn}, []string{appID}, config.Load,
		func(ctx context.Context, auth *config.AuthConfig, device *agentpb.ProvisionedResponse, _ []string) error {
			identity, err := certs.ParsePrincipal(device.GetPrincipalUri())
			if err != nil || identity.TenantUUID == "" {
				return fmt.Errorf("--record-release needs a device enrolled with Wendy Cloud v2")
			}
			return rel.withCloud(ctx, auth, func(ctx context.Context, cc grpc.ClientConnInterface) (err error) {
				rel.auth = auth
				rel.id, err = createAppRelease(ctx, cc, auth, identity.TenantUUID, appID, tag)
				return err
			})
		})
	if err != nil {
		return nil, err
	}
	if rel.id == "" {
		return nil, fmt.Errorf("--record-release needs a device enrolled with Wendy Cloud")
	}
	return rel, nil
}

// finish stores the digest the agent reported on the release.
func (r *deployRelease) finish(ctx context.Context) error {
	digest := r.digest.get()
	if digest == "" {
		return fmt.Errorf("release %s was created, but this deploy path or agent reported no image digest; it stays without one", r.id)
	}
	if err := r.withCloud(ctx, r.auth, func(ctx context.Context, cc grpc.ClientConnInterface) error {
		return setAppReleaseDigest(ctx, cc, r.auth, r.id, r.tag, digest)
	}); err != nil {
		return fmt.Errorf("release %s was created without its digest: %w", r.id, err)
	}
	cliLogln("Recorded release %s: %s = %s", r.id, r.tag, digest)
	return nil
}

func (r *deployRelease) withCloud(ctx context.Context, auth *config.AuthConfig, call func(context.Context, grpc.ClientConnInterface) error) error {
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, err = cloudContext(ctx, auth)
	if err != nil {
		return err
	}
	return call(ctx, conn)
}

// imageDigestSink receives the image digest from whichever create path the
// deploy takes, carried on the deploy's context so no path changes signature.
type imageDigestSink struct {
	mu     sync.Mutex
	digest string
}

func (s *imageDigestSink) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.digest
}

type imageDigestSinkKey struct{}

func withImageDigestSink(ctx context.Context, sink *imageDigestSink) context.Context {
	return context.WithValue(ctx, imageDigestSinkKey{}, sink)
}

// noteImageDigest hands a create response's digest to the deploy's sink, if
// it has one.
func noteImageDigest(ctx context.Context, digest string) {
	if sink, ok := ctx.Value(imageDigestSinkKey{}).(*imageDigestSink); ok && digest != "" {
		sink.mu.Lock()
		sink.digest = digest
		sink.mu.Unlock()
	}
}
