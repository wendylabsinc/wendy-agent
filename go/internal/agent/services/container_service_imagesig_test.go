package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"io"
	"net"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/shared/sigverify"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// containerImageMldsaKeypairForTest generates an ephemeral ML-DSA65 keypair
// and returns an enabled sigverify.Verifier pinned to the public key, plus a
// sign function for the private key. Mirrors mldsaKeypairForTest in
// agent_update_service_test.go (the Go binary-verify H2 task); duplicated
// here because that helper is unexported and this is a different _test.go
// translation unit within the same package, but kept intentionally identical
// so both call sites construct verifiers the same way.
func containerImageMldsaKeypairForTest(t *testing.T) (verifier *sigverify.Verifier, sign func(msg []byte) []byte) {
	t.Helper()
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pub.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "WENDY MLDSA65 PUBLIC KEY", Bytes: raw})

	v, err := sigverify.NewVerifier(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Enabled() {
		t.Fatal("expected verifier to be enabled with a pinned key")
	}

	sign = func(msg []byte) []byte {
		sig := make([]byte, mldsa65.SignatureSize)
		if err := mldsa65.SignTo(priv, msg, nil, false, sig); err != nil {
			t.Fatal(err)
		}
		return sig
	}
	return v, sign
}

// startContainerServerWithVerifier starts a ContainerService over bufconn
// with the given ContainerdClient, overriding the imageVerifier field
// directly (same within-package injection seam as AgentUpdateService's
// verifier).
func startContainerServerWithVerifier(t *testing.T, client ContainerdClient, verifier *sigverify.Verifier) (agentpb.WendyContainerServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	logger := zap.NewNop()
	svc := NewContainerService(logger, client)
	if verifier != nil {
		svc.imageVerifier = verifier
	}
	agentpb.RegisterWendyContainerServiceServer(srv, svc)

	go func() { _ = srv.Serve(lis) }()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cl := agentpb.NewWendyContainerServiceClient(conn)
	cleanup := func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
	return cl, cleanup
}

// drainRunContainerStream reads a RunContainer server-stream to completion,
// returning the terminal error (nil on a clean io.EOF).
func drainRunContainerStream(stream grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse]) error {
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TestNewContainerService_ImageVerifierIsSeparateFromAgentBinaryKey covers H2
// Task 9: container image verification must sit on its own key seam
// (imageVerifier), distinct from sigverify.DefaultVerifier (the Wendy
// build-embedded key used for the agent binary / OS artifacts). Images are
// per-org publisher artifacts and must eventually verify against a per-org
// key sourced from provisioning/PKI, never the global Wendy build key. The
// default must therefore be a disabled placeholder, not DefaultVerifier.
func TestNewContainerService_ImageVerifierIsSeparateFromAgentBinaryKey(t *testing.T) {
	svc := NewContainerService(zap.NewNop(), &mockContainerdClient{})

	if svc.imageVerifier == sigverify.DefaultVerifier {
		t.Fatal("imageVerifier must not be the same instance as sigverify.DefaultVerifier (the agent-binary/OS key); container images need a separate per-org publisher key seam")
	}
	if svc.imageVerifier.Enabled() {
		t.Fatal("imageVerifier must default to disabled until the per-org publisher key is wired from provisioning")
	}
}

// TestRunContainer_ImageSignatureVerification covers H2 Phase 4 Task 5: the
// image signature carried on RunContainerLayersRequest must be verified
// (ML-DSA65 over sha256(image_config)) before the image is assembled or the
// container is created.
func TestRunContainer_ImageSignatureVerification(t *testing.T) {
	verifier, sign := containerImageMldsaKeypairForTest(t)

	imageConfig := []byte(`{"config":{"Cmd":["/entrypoint"]}}`)
	sum := sha256.Sum256(imageConfig)
	digest := sum[:]

	baseReq := func(sig []byte) *agentpb.RunContainerLayersRequest {
		return &agentpb.RunContainerLayersRequest{
			ImageName: "test-image:latest",
			AppName:   "sig-test-app",
			Layers: []*agentpb.RunContainerLayerHeader{
				{Digest: "sha256:layerdigest", Size: 10, DiffId: "sha256:layerdiffid"},
			},
			ImageConfig:    imageConfig,
			ImageSignature: sig,
		}
	}

	t.Run("valid signature proceeds past verification", func(t *testing.T) {
		outputCh := make(chan ContainerOutput, 1)
		outputCh <- ContainerOutput{Done: true}
		close(outputCh)
		mock := &mockContainerdClient{startOutputCh: outputCh}

		client, cleanup := startContainerServerWithVerifier(t, mock, verifier)
		defer cleanup()

		sig := sign(digest)
		stream, err := client.RunContainer(context.Background(), baseReq(sig))
		if err != nil {
			t.Fatalf("RunContainer: %v", err)
		}
		if err := drainRunContainerStream(stream); err != nil {
			t.Fatalf("expected success, got: %v", err)
		}

		if mock.assembleImageCalls != 1 {
			t.Errorf("assembleImageCalls = %d, want 1 (valid signature must allow assembly)", mock.assembleImageCalls)
		}
	})

	t.Run("empty signature is rejected before assembly", func(t *testing.T) {
		mock := &mockContainerdClient{}
		client, cleanup := startContainerServerWithVerifier(t, mock, verifier)
		defer cleanup()

		stream, err := client.RunContainer(context.Background(), baseReq(nil))
		if err != nil {
			t.Fatalf("RunContainer: %v", err)
		}
		err = drainRunContainerStream(stream)
		if err == nil {
			t.Fatal("expected error for empty signature while verifier enabled")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v, want FailedPrecondition; err = %v", status.Code(err), err)
		}
		if mock.assembleImageCalls != 0 {
			t.Errorf("assembleImageCalls = %d, want 0 (unsigned image must not be assembled)", mock.assembleImageCalls)
		}
	})

	t.Run("tampered signature is rejected before assembly", func(t *testing.T) {
		mock := &mockContainerdClient{}
		client, cleanup := startContainerServerWithVerifier(t, mock, verifier)
		defer cleanup()

		sig := sign(digest)
		sig[0] ^= 0xFF // tamper

		stream, err := client.RunContainer(context.Background(), baseReq(sig))
		if err != nil {
			t.Fatalf("RunContainer: %v", err)
		}
		err = drainRunContainerStream(stream)
		if err == nil {
			t.Fatal("expected error for tampered signature")
		}
		if status.Code(err) != codes.DataLoss {
			t.Fatalf("code = %v, want DataLoss; err = %v", status.Code(err), err)
		}
		if mock.assembleImageCalls != 0 {
			t.Errorf("assembleImageCalls = %d, want 0 (tampered signature must not be assembled)", mock.assembleImageCalls)
		}
	})
}

// TestRunContainer_DisabledVerifierProceeds verifies that with the default
// (disabled, no pinned key) verifier, RunContainer behaves exactly as before
// this check existed: no signature required, assembly proceeds.
func TestRunContainer_DisabledVerifierProceeds(t *testing.T) {
	outputCh := make(chan ContainerOutput, 1)
	outputCh <- ContainerOutput{Done: true}
	close(outputCh)
	mock := &mockContainerdClient{startOutputCh: outputCh}

	// nil verifier override -> NewContainerService's default image verifier (sigverify.Disabled()).
	client, cleanup := startContainerServerWithVerifier(t, mock, nil)
	defer cleanup()

	stream, err := client.RunContainer(context.Background(), &agentpb.RunContainerLayersRequest{
		ImageName: "test-image:latest",
		AppName:   "no-sig-app",
		Layers: []*agentpb.RunContainerLayerHeader{
			{Digest: "sha256:layerdigest", Size: 10, DiffId: "sha256:layerdiffid"},
		},
		ImageConfig: []byte(`{}`),
		// ImageSignature intentionally omitted.
	})
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if err := drainRunContainerStream(stream); err != nil {
		t.Fatalf("expected success with disabled verifier, got: %v", err)
	}
	if mock.assembleImageCalls != 1 {
		t.Errorf("assembleImageCalls = %d, want 1 (disabled verifier must not block assembly)", mock.assembleImageCalls)
	}
}
