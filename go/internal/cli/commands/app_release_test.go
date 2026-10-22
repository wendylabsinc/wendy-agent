package commands

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// A release without a tag is refused before anything is sent: there is no
// implicit "latest". A malformed digest is refused the same way.
func TestAppReleaseRefusesImplicitLatest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, args := range map[string][]string{
		"no tag":       {"--digest", digest},
		"blank tag":    {"--tag", " ", "--digest", digest},
		"no digest":    {"--tag", "v1"},
		"short digest": {"--tag", "v1", "--digest", "sha256:abc"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newAppReleaseCmd()
			cmd.SetArgs(args)
			cmd.SetOut(new(strings.Builder))
			cmd.SetErr(new(strings.Builder))
			if err := cmd.Execute(); err == nil {
				t.Fatal("release accepted")
			}
		})
	}
	if _, err := newDeployRelease("app", ""); err == nil || !strings.Contains(err.Error(), "latest") {
		t.Fatalf("--record-release with an empty tag: err = %v", err)
	}
}

type releaseRecorder struct {
	cloudpb.UnimplementedDeploymentServiceServer
	created *cloudpb.CreateAppReleaseRequest
	updated *cloudpb.UpdateAppReleaseRequest
}

func (r *releaseRecorder) CreateAppRelease(_ context.Context, env *cloudpb.SignedRequest) (*cloudpb.AppRelease, error) {
	r.created = new(cloudpb.CreateAppReleaseRequest)
	if err := proto.Unmarshal(env.GetPayload(), r.created); err != nil {
		return nil, err
	}
	return &cloudpb.AppRelease{Id: "0b6f2c1e-8a3d-4c5e-9f00-1234567890ab"}, nil
}

func (r *releaseRecorder) UpdateAppRelease(_ context.Context, env *cloudpb.SignedRequest) (*cloudpb.AppRelease, error) {
	r.updated = new(cloudpb.UpdateAppReleaseRequest)
	return &cloudpb.AppRelease{}, proto.Unmarshal(env.GetPayload(), r.updated)
}

// A recorded release is created under its tag and then given the digest the
// deploy reported; the update repeats the tag because it replaces the name.
func TestRecordedReleaseCarriesTagAndDigest(t *testing.T) {
	key := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	principal := "spiffe://wendy.sh/tenant/" + deploymentTenant + "/operator/test"
	auth := &config.AuthConfig{Certificates: []config.CertificateInfo{{PrincipalURI: principal,
		PemCertificate: liteLeafPEM(t, key, principal, time.Now().Add(time.Hour), false),
		PemPrivateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	}}}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rec := &releaseRecorder{}
	srv := grpc.NewServer()
	cloudpb.RegisterDeploymentServiceServer(srv, rec)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	id, err := createAppRelease(ctx, conn, auth, deploymentTenant, "com.example.cam", "v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := setAppReleaseDigest(ctx, conn, auth, id, "v1.4.0", "sha256:feed"); err != nil {
		t.Fatal(err)
	}
	wantCreate := &cloudpb.CreateAppReleaseRequest{AppId: "com.example.cam", OrganizationId: deploymentTenant, Name: "v1.4.0"}
	wantUpdate := &cloudpb.UpdateAppReleaseRequest{Id: id, Name: "v1.4.0", ImageDigest: "sha256:feed"}
	if !proto.Equal(rec.created, wantCreate) || !proto.Equal(rec.updated, wantUpdate) {
		t.Fatalf("create = %v, update = %v", rec.created, rec.updated)
	}
}

// A recorded deploy says what it left behind: nothing on a path that cannot
// record, the release id when the digest never arrived, silence when complete.
func TestDeployReleaseOutcome(t *testing.T) {
	r, err := newDeployRelease("app", "v1")
	if err != nil {
		t.Fatal(err)
	}
	// No release was begun: a digest from the agent is dropped.
	noteImageDigest(withDeployRelease(context.Background(), r), "sha256:feed")
	if err := r.outcome(nil); err == nil || !strings.Contains(err.Error(), "nothing was recorded") {
		t.Fatalf("unsupported path: %v", err)
	}
	if err := r.outcome(context.Canceled); err != nil {
		t.Fatalf("failed deploy before any release: %v", err)
	}
	r.id = "0b6f2c1e-8a3d-4c5e-9f00-1234567890ab"
	if err := r.outcome(nil); err == nil || !strings.Contains(err.Error(), r.id) {
		t.Fatalf("release without digest: %v", err)
	}
	r.stored = true
	if err := r.outcome(context.Canceled); err != nil {
		t.Fatalf("completed release after Ctrl-C: %v", err)
	}
}
