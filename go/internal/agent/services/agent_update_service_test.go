package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/shared/sigverify"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// mldsaKeypairForTest generates an ephemeral ML-DSA65 keypair and returns an
// enabled sigverify.Verifier pinned to the public key, plus a sign function
// for the private key. The PEM block type mirrors the one sigverify.NewVerifier
// expects ("WENDY MLDSA65 PUBLIC KEY"); it is reconstructed here rather than
// via an exported sigverify helper since none is exposed outside the package.
func mldsaKeypairForTest(t *testing.T) (verifier *sigverify.Verifier, sign func(msg []byte) []byte) {
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

// startAgentUpdateServer starts an AgentUpdateService over bufconn, targeting
// a temp file as the "executable" so commitBinaryUpdate's rename lands
// somewhere inspectable. It returns the client, the exec path, and a cleanup
// func.
func startAgentUpdateServer(t *testing.T, verifier *sigverify.Verifier) (agentpbv2.WendyAgentUpdateServiceClient, string, func()) {
	t.Helper()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "wendy-agent")
	if err := os.WriteFile(execPath, []byte("original-binary-contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	logger := zap.NewNop()
	svc := NewAgentUpdateService(logger, &AgentInstaller{})
	if verifier != nil {
		svc.verifier = verifier
	}
	// The real resolveExecPath() resolves os.Executable() (the running test
	// binary itself); overriding it here points commitBinaryUpdate's rename
	// at a disposable temp file instead so a "valid signature" test case
	// doesn't clobber the test binary mid-run.
	svc.execPathResolver = func() (string, os.FileMode, error) {
		return execPath, 0o755, nil
	}
	// The real scheduleAgentRestartExit calls os.Exit(0) shortly after a
	// successful commit; a no-op keeps the "valid signature installs" case
	// from killing the test process.
	svc.restartFn = func() {}
	agentpbv2.RegisterWendyAgentUpdateServiceServer(srv, svc)

	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	client := agentpbv2.NewWendyAgentUpdateServiceClient(conn)
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return client, execPath, cleanup
}

// sendUpdateStream drives an UpdateAgent bidi stream: a single chunk carrying
// binaryData, followed by an Update control command carrying sha256Hash and
// signature. It returns the terminal error from Recv (nil on success).
func sendUpdateStream(t *testing.T, stream grpc.BidiStreamingClient[agentpbv2.UpdateAgentRequest, agentpbv2.UpdateAgentResponse], binaryData []byte, sha256Hash string, signature []byte) error {
	t.Helper()

	if err := stream.Send(&agentpbv2.UpdateAgentRequest{
		RequestType: &agentpbv2.UpdateAgentRequest_Chunk_{
			Chunk: &agentpbv2.UpdateAgentRequest_Chunk{Data: binaryData},
		},
	}); err != nil {
		t.Fatalf("send chunk: %v", err)
	}

	if err := stream.Send(&agentpbv2.UpdateAgentRequest{
		RequestType: &agentpbv2.UpdateAgentRequest_Control{
			Control: &agentpbv2.UpdateAgentRequest_ControlCommand{
				Command: &agentpbv2.UpdateAgentRequest_ControlCommand_Update_{
					Update: &agentpbv2.UpdateAgentRequest_ControlCommand_Update{
						Sha256:    sha256Hash,
						Signature: signature,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("send control: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	_, err := stream.Recv()
	return err
}

func TestUpdateAgent_SignatureVerification(t *testing.T) {
	verifier, sign := mldsaKeypairForTest(t)

	binaryData := []byte("new-agent-binary-contents-for-signature-test")
	sum := sha256.Sum256(binaryData)
	digest := sum[:]
	sha256Hash := hex.EncodeToString(digest)

	t.Run("valid signature installs", func(t *testing.T) {
		client, execPath, cleanup := startAgentUpdateServer(t, verifier)
		defer cleanup()

		stream, err := client.UpdateAgent(context.Background())
		if err != nil {
			t.Fatalf("UpdateAgent: %v", err)
		}

		sig := sign(digest)
		if err := sendUpdateStream(t, stream, binaryData, sha256Hash, sig); err != nil {
			t.Fatalf("expected success, got: %v", err)
		}

		installed, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatalf("reading installed binary: %v", err)
		}
		if string(installed) != string(binaryData) {
			t.Fatalf("installed binary = %q, want %q", installed, binaryData)
		}
	})

	t.Run("empty signature is rejected and not installed", func(t *testing.T) {
		client, execPath, cleanup := startAgentUpdateServer(t, verifier)
		defer cleanup()

		stream, err := client.UpdateAgent(context.Background())
		if err != nil {
			t.Fatalf("UpdateAgent: %v", err)
		}

		err = sendUpdateStream(t, stream, binaryData, sha256Hash, nil)
		if err == nil {
			t.Fatal("expected error for empty signature while verifier enabled")
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v, want FailedPrecondition; err = %v", status.Code(err), err)
		}

		installed, readErr := os.ReadFile(execPath)
		if readErr != nil {
			t.Fatalf("reading exec path: %v", readErr)
		}
		if string(installed) == string(binaryData) {
			t.Fatal("binary was installed despite missing signature")
		}
	})

	t.Run("tampered signature is rejected and not installed", func(t *testing.T) {
		client, execPath, cleanup := startAgentUpdateServer(t, verifier)
		defer cleanup()

		stream, err := client.UpdateAgent(context.Background())
		if err != nil {
			t.Fatalf("UpdateAgent: %v", err)
		}

		sig := sign(digest)
		sig[0] ^= 0xFF // tamper

		err = sendUpdateStream(t, stream, binaryData, sha256Hash, sig)
		if err == nil {
			t.Fatal("expected error for tampered signature")
		}
		if status.Code(err) != codes.DataLoss {
			t.Fatalf("code = %v, want DataLoss; err = %v", status.Code(err), err)
		}

		installed, readErr := os.ReadFile(execPath)
		if readErr != nil {
			t.Fatalf("reading exec path: %v", readErr)
		}
		if string(installed) == string(binaryData) {
			t.Fatal("binary was installed despite tampered signature")
		}
	})
}
