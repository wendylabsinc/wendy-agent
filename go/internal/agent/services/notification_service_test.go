package services

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	systempb "github.com/wendylabsinc/wendy/go/proto/gen/systempb"
)

type recordingNotificationSender struct {
	mu       sync.Mutex
	requests []*cloudpb.CreateNotificationV2Request
}

func (s *recordingNotificationSender) CreateNotificationV2(
	_ context.Context,
	request *cloudpb.CreateNotificationV2Request,
) (*cloudpb.CreateNotificationV2Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	return &cloudpb.CreateNotificationV2Response{RecipientCount: 1}, nil
}

type deadlineNotificationSender struct {
	deadline time.Time
	has      bool
}

func (s *deadlineNotificationSender) CreateNotificationV2(
	ctx context.Context,
	_ *cloudpb.CreateNotificationV2Request,
) (*cloudpb.CreateNotificationV2Response, error) {
	s.deadline, s.has = ctx.Deadline()
	return &cloudpb.CreateNotificationV2Response{}, nil
}

func validSystemNotificationRequest() *systempb.SendRequest {
	return &systempb.SendRequest{
		Audience: &systempb.NotificationAudience{
			Audience: &systempb.NotificationAudience_OrganizationRole{
				OrganizationRole: systempb.OrganizationRole_ORGANIZATION_ROLE_MEMBER,
			},
		},
		Title:    "Fire detected",
		Body:     "Camera 2 detected smoke",
		Severity: systempb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL,
		DeepLink: "wendy://live?camera=camera-2",
		SourceId: "firewatch:incident-42",
	}
}

func TestSystemNotificationServiceBindsTrustedAppIdentityAndMapsTransport(t *testing.T) {
	sender := &recordingNotificationSender{}
	service := NewSystemNotificationService("com.example.firewatch", sender)

	response, err := service.Send(context.Background(), validSystemNotificationRequest())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if response.GetRecipientCount() != 1 {
		t.Fatalf("recipient count = %d, want 1", response.GetRecipientCount())
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.requests) != 1 {
		t.Fatalf("Cloud requests = %d, want 1", len(sender.requests))
	}
	request := sender.requests[0]
	if request.GetSourceAppId() != "com.example.firewatch" {
		t.Fatalf("source_app_id = %q, want trusted app identity", request.GetSourceAppId())
	}
	if request.GetOrganizationId() != 0 || request.OrganizationId != nil {
		t.Fatal("System request must not supply organization identity")
	}
	if request.GetAudience().GetOrganizationRole() != cloudpb.OrganizationRole_ORGANIZATION_ROLE_MEMBER {
		t.Fatalf("audience role = %v", request.GetAudience().GetOrganizationRole())
	}
	if request.GetSeverity() != cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL {
		t.Fatalf("severity = %v", request.GetSeverity())
	}
}

func proofCloudNotificationRequest() *cloudpb.CreateNotificationV2Request {
	sourceAppID := "com.example.firewatch"
	return &cloudpb.CreateNotificationV2Request{
		Audience: &cloudpb.NotificationAudience{
			Audience: &cloudpb.NotificationAudience_UserId{UserId: "user-1"},
		},
		Title:       "Fire",
		Body:        "Smoke detected",
		Severity:    cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL,
		DeepLink:    "wendy://live?camera=camera-2",
		SourceId:    "incident-42",
		SourceAppId: &sourceAppID,
	}
}

func deviceProofTestKeyPEM(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func TestCanonicalNotificationDeviceProofIsDeterministic(t *testing.T) {
	const timestamp = int64(1_753_267_200)
	request := proofCloudNotificationRequest()
	first, uri, timestampText, err := canonicalNotificationDeviceProof(request, 17, 42, timestamp)
	if err != nil {
		t.Fatalf("canonicalNotificationDeviceProof() error = %v", err)
	}
	second, _, _, err := canonicalNotificationDeviceProof(proto.Clone(request).(*cloudpb.CreateNotificationV2Request), 17, 42, timestamp)
	if err != nil {
		t.Fatalf("second canonicalNotificationDeviceProof() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("canonical proof bytes changed for the same semantic protobuf request")
	}
	requestBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	prefix := deviceProofDomain + "\x00" + deviceProofFullMethod + "\x00" + uri + "\x00" + timestampText + "\x00"
	if !bytes.Equal(first, append([]byte(prefix), requestBytes...)) {
		t.Fatal("canonical proof does not match the versioned NUL-framed contract")
	}
	digest := sha256.Sum256(first)
	if got, want := hex.EncodeToString(digest[:]), "411f1803e97b25a4a27e715ccdaae840633ea789a24bcafac383444815ed6548"; got != want {
		t.Fatalf("canonical proof SHA-256 = %s, want fixture %s", got, want)
	}
}

func TestNotificationDeviceProofSignatureAndTamperResistance(t *testing.T) {
	const timestamp = int64(1_753_267_200)
	key, keyPEM := deviceProofTestKeyPEM(t)
	request := proofCloudNotificationRequest()
	baseCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		deviceProofURIHeader, "forged",
		deviceProofTimestampHeader, "0",
		deviceProofSignatureHeader, "forged",
		"x-unrelated", "preserved",
	))
	ctx, err := notificationDeviceProofContext(baseCtx, request, 17, 42, timestamp, keyPEM, rand.Reader)
	if err != nil {
		t.Fatalf("notificationDeviceProofContext() error = %v", err)
	}
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("proof metadata missing")
	}
	if got := md.Get(deviceProofURIHeader); len(got) != 1 || got[0] != "urn:wendy:org:17:asset:42" {
		t.Fatalf("device URI metadata = %v", got)
	}
	if got := md.Get("x-unrelated"); len(got) != 1 || got[0] != "preserved" {
		t.Fatalf("unrelated outgoing metadata = %v", got)
	}
	if got := md.Get(deviceProofTimestampHeader); len(got) != 1 || got[0] != "1753267200" {
		t.Fatalf("timestamp metadata = %v", got)
	}
	signatureValues := md.Get(deviceProofSignatureHeader)
	if len(signatureValues) != 1 || strings.Contains(signatureValues[0], "=") {
		t.Fatalf("signature metadata is not one unpadded base64url value: %v", signatureValues)
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureValues[0])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	canonical, _, _, err := canonicalNotificationDeviceProof(request, 17, 42, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], signature) {
		t.Fatal("valid device proof signature did not verify")
	}

	tamperedRequest := proto.Clone(request).(*cloudpb.CreateNotificationV2Request)
	tamperedRequest.Body = "different body"
	for name, tamperedCanonical := range map[string][]byte{
		"request":   mustCanonicalNotificationProof(t, tamperedRequest, 17, 42, timestamp),
		"identity":  mustCanonicalNotificationProof(t, request, 17, 43, timestamp),
		"timestamp": mustCanonicalNotificationProof(t, request, 17, 42, timestamp+1),
	} {
		t.Run(name, func(t *testing.T) {
			tamperedDigest := sha256.Sum256(tamperedCanonical)
			if ecdsa.VerifyASN1(&key.PublicKey, tamperedDigest[:], signature) {
				t.Fatal("signature verified after proof tampering")
			}
		})
	}
}

func mustCanonicalNotificationProof(
	t *testing.T,
	request *cloudpb.CreateNotificationV2Request,
	orgID, assetID int32,
	timestamp int64,
) []byte {
	t.Helper()
	canonical, _, _, err := canonicalNotificationDeviceProof(request, orgID, assetID, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestSystemNotificationServiceBoundsCloudForwardingDeadline(t *testing.T) {
	t.Run("adds default deadline", func(t *testing.T) {
		sender := &deadlineNotificationSender{}
		started := time.Now()
		if _, err := NewSystemNotificationService("com.example.app", sender).Send(context.Background(), validSystemNotificationRequest()); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if !sender.has {
			t.Fatal("Cloud forwarding context has no deadline")
		}
		minimum := started.Add(notificationForwardTimeout - time.Second)
		maximum := time.Now().Add(notificationForwardTimeout)
		if sender.deadline.Before(minimum) || sender.deadline.After(maximum) {
			t.Fatalf("forwarding deadline = %v, want approximately %v", sender.deadline, started.Add(notificationForwardTimeout))
		}
	})

	t.Run("preserves shorter caller deadline", func(t *testing.T) {
		sender := &deadlineNotificationSender{}
		callerDeadline := time.Now().Add(time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
		defer cancel()
		if _, err := NewSystemNotificationService("com.example.app", sender).Send(ctx, validSystemNotificationRequest()); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
		if !sender.has || !sender.deadline.Equal(callerDeadline) {
			t.Fatalf("forwarding deadline = %v, want caller deadline %v", sender.deadline, callerDeadline)
		}
	})
}

func TestSystemNotificationServiceValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*systempb.SendRequest)
	}{
		{name: "missing audience", mutate: func(r *systempb.SendRequest) { r.Audience = nil }},
		{name: "invalid team", mutate: func(r *systempb.SendRequest) {
			r.Audience = &systempb.NotificationAudience{Audience: &systempb.NotificationAudience_OrgTeamId{OrgTeamId: 0}}
		}},
		{name: "blank title", mutate: func(r *systempb.SendRequest) { r.Title = "" }},
		{name: "control body", mutate: func(r *systempb.SendRequest) { r.Body = "bad\nbody" }},
		{name: "unspecified severity", mutate: func(r *systempb.SendRequest) {
			r.Severity = systempb.NotificationSeverity_NOTIFICATION_SEVERITY_UNSPECIFIED
		}},
		{name: "relative deep link", mutate: func(r *systempb.SendRequest) { r.DeepLink = "/live" }},
		{name: "wrong deep link scheme", mutate: func(r *systempb.SendRequest) { r.DeepLink = "https://example.com/live" }},
		{name: "deep link userinfo", mutate: func(r *systempb.SendRequest) { r.DeepLink = "wendy://user@live" }},
		{name: "deep link without host", mutate: func(r *systempb.SendRequest) { r.DeepLink = "wendy:///live" }},
		{name: "unsafe source id", mutate: func(r *systempb.SendRequest) { r.SourceId = "has spaces" }},
		{name: "oversized metadata", mutate: func(r *systempb.SendRequest) {
			r.Metadata, _ = structpb.NewStruct(map[string]any{"value": strings.Repeat("x", maxNotificationMetadataBytes+1)})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validSystemNotificationRequest()
			test.mutate(request)
			_, err := NewSystemNotificationService("com.example.app", &recordingNotificationSender{}).Send(context.Background(), request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("Send() code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
			}
		})
	}
}

func TestSystemNotificationServiceRateLimit(t *testing.T) {
	service := NewSystemNotificationService("com.example.app", &recordingNotificationSender{})
	for i := 0; i < 10; i++ {
		if _, err := service.Send(context.Background(), validSystemNotificationRequest()); err != nil {
			t.Fatalf("Send() %d error = %v", i, err)
		}
	}
	if _, err := service.Send(context.Background(), validSystemNotificationRequest()); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("eleventh Send() code = %v, want ResourceExhausted", status.Code(err))
	}
}

func TestAppSystemAPISocketManagerSharesPerAppAndIsolatesIdentity(t *testing.T) {
	oldRoot := AppSystemAPISocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-system-api-")
	if err != nil {
		t.Fatal(err)
	}
	AppSystemAPISocketRootPath = root
	t.Cleanup(func() {
		AppSystemAPISocketRootPath = oldRoot
		_ = os.RemoveAll(root)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &recordingNotificationSender{}
	manager := NewAppSystemAPISocketManager(ctx, zap.NewNop(), sender)

	appADir, err := manager.Ensure("com.example.a", "api", []string{SystemAPICapabilityNotifications})
	if err != nil {
		t.Fatalf("Ensure(app A/api) error = %v", err)
	}
	appAWorkerDir, err := manager.Ensure("com.example.a", "worker", []string{SystemAPICapabilityNotifications})
	if err != nil {
		t.Fatalf("Ensure(app A/worker) error = %v", err)
	}
	if appADir != appAWorkerDir {
		t.Fatalf("multi-service app directories differ: %q != %q", appADir, appAWorkerDir)
	}
	appBDir, err := manager.Ensure("com.example.b", "", []string{SystemAPICapabilityNotifications})
	if err != nil {
		t.Fatalf("Ensure(app B) error = %v", err)
	}
	if appADir == appBDir {
		t.Fatal("different apps shared a System API directory")
	}

	for _, test := range []struct {
		directory string
		wantAppID string
	}{
		{directory: appADir, wantAppID: "com.example.a"},
		{directory: appBDir, wantAppID: "com.example.b"},
	} {
		conn, err := grpc.NewClient(
			"unix://"+filepath.Join(test.directory, SystemAPISocketFilename),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial %s: %v", test.wantAppID, err)
		}
		client := systempb.NewNotificationServiceClient(conn)
		callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = client.Send(callCtx, validSystemNotificationRequest())
		callCancel()
		_ = conn.Close()
		if err != nil {
			t.Fatalf("Send(%s) error = %v", test.wantAppID, err)
		}
	}

	sender.mu.Lock()
	if len(sender.requests) != 2 || sender.requests[0].GetSourceAppId() != "com.example.a" || sender.requests[1].GetSourceAppId() != "com.example.b" {
		t.Fatalf("trusted app identities = %+v", sender.requests)
	}
	sender.mu.Unlock()

	manager.Release("com.example.a", "api")
	if _, err := os.Stat(appADir); err != nil {
		t.Fatalf("directory removed while worker still owns it: %v", err)
	}
	manager.Release("com.example.a", "worker")
	if _, err := os.Stat(appADir); !os.IsNotExist(err) {
		t.Fatalf("directory remains after final release: %v", err)
	}
}

func TestAppSystemAPISocketManagerRecreatesSocketInStableDirectory(t *testing.T) {
	oldRoot := AppSystemAPISocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-system-restart-")
	if err != nil {
		t.Fatal(err)
	}
	AppSystemAPISocketRootPath = root
	t.Cleanup(func() {
		AppSystemAPISocketRootPath = oldRoot
		_ = os.RemoveAll(root)
	})

	ctx1, cancel1 := context.WithCancel(context.Background())
	manager1 := NewAppSystemAPISocketManager(ctx1, zap.NewNop(), &recordingNotificationSender{})
	directory, err := manager1.Ensure("com.example.restart", "", []string{SystemAPICapabilityNotifications})
	if err != nil {
		t.Fatalf("first Ensure() error = %v", err)
	}
	before, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	manager1.stopAll()
	cancel1()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	manager2 := NewAppSystemAPISocketManager(ctx2, zap.NewNop(), &recordingNotificationSender{})
	restored, err := manager2.Ensure("com.example.restart", "", []string{SystemAPICapabilityNotifications})
	if err != nil {
		t.Fatalf("restored Ensure() error = %v", err)
	}
	after, err := os.Stat(restored)
	if err != nil {
		t.Fatal(err)
	}
	if restored != directory || !os.SameFile(before, after) {
		t.Fatal("Agent restart replaced the mounted System API directory inode")
	}
}
