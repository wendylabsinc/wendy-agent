package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	systempb "github.com/wendylabsinc/wendy/go/proto/gen/systempb"
)

const (
	maxNotificationMetadataBytes = 8 * 1024
	notificationForwardTimeout   = 15 * time.Second

	deviceProofURIHeader       = "x-wendy-device-uri"
	deviceProofTimestampHeader = "x-wendy-device-timestamp"
	deviceProofSignatureHeader = "x-wendy-device-signature"
	deviceProofDomain          = "wendy-device-request-proof/v1"
	deviceProofFullMethod      = "wendycloud.v1.NotificationService/CreateNotificationV2"
)

// NotificationSender forwards a trusted, app-attributed Notification to Wendy Cloud.
type NotificationSender interface {
	CreateNotificationV2(context.Context, *cloudpb.CreateNotificationV2Request) (*cloudpb.CreateNotificationV2Response, error)
}

// SystemNotificationService serves one app-specific System API socket. The
// source app ID comes from the Agent's container metadata and socket mount; it
// is never accepted from workload-controlled input.
type SystemNotificationService struct {
	systempb.UnimplementedNotificationServiceServer
	sourceAppID string
	sender      NotificationSender
	limiter     notificationRateLimiter
}

func NewSystemNotificationService(sourceAppID string, sender NotificationSender) *SystemNotificationService {
	return &SystemNotificationService{
		sourceAppID: sourceAppID,
		sender:      sender,
		limiter:     newNotificationRateLimiter(10, time.Second),
	}
}

func (s *SystemNotificationService) Send(
	ctx context.Context,
	req *systempb.SendRequest,
) (*systempb.SendResponse, error) {
	if s.sender == nil {
		return nil, status.Error(codes.Unavailable, "notification delivery is unavailable")
	}
	if err := validateNotificationSendRequest(req); err != nil {
		return nil, err
	}
	if !s.limiter.allow(time.Now()) {
		return nil, status.Error(codes.ResourceExhausted, "notification rate exceeded; retry later with the same source_id")
	}

	sourceAppID := s.sourceAppID
	cloudRequest := &cloudpb.CreateNotificationV2Request{
		Audience:    cloudNotificationAudience(req.GetAudience()),
		Title:       req.GetTitle(),
		Body:        req.GetBody(),
		Severity:    cloudNotificationSeverity(req.GetSeverity()),
		DeepLink:    req.GetDeepLink(),
		SourceId:    req.GetSourceId(),
		Metadata:    req.GetMetadata(),
		SourceAppId: &sourceAppID,
	}
	forwardCtx, cancel := notificationForwardContext(ctx)
	defer cancel()
	response, err := s.sender.CreateNotificationV2(forwardCtx, cloudRequest)
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Errorf(codes.Unavailable, "send notification: %v", err)
	}
	return &systempb.SendResponse{
		Duplicate:      response.GetDuplicate(),
		RecipientCount: response.GetRecipientCount(),
	}, nil
}

func notificationForwardContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= notificationForwardTimeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, notificationForwardTimeout)
}

type notificationRateLimiter struct {
	mu             sync.Mutex
	tokens         float64
	capacity       float64
	refillInterval time.Duration
	lastRefill     time.Time
}

func newNotificationRateLimiter(capacity int, refillInterval time.Duration) notificationRateLimiter {
	return notificationRateLimiter{
		tokens:         float64(capacity),
		capacity:       float64(capacity),
		refillInterval: refillInterval,
		lastRefill:     time.Now(),
	}
}

func (l *notificationRateLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens += now.Sub(l.lastRefill).Seconds() / l.refillInterval.Seconds()
	if l.tokens > l.capacity {
		l.tokens = l.capacity
	}
	l.lastRefill = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

func validateNotificationSendRequest(request *systempb.SendRequest) error {
	if request == nil {
		return status.Error(codes.InvalidArgument, "request is required")
	}
	if !validNotificationIdentifier(request.GetSourceId(), 128) {
		return status.Error(codes.InvalidArgument, "source_id must contain 1...128 safe ASCII bytes")
	}
	if !validNotificationText(request.GetTitle(), 120) {
		return status.Error(codes.InvalidArgument, "title must contain 1...120 printable UTF-8 bytes")
	}
	if !validNotificationText(request.GetBody(), 2000) {
		return status.Error(codes.InvalidArgument, "body must contain 1...2000 printable UTF-8 bytes")
	}
	switch request.GetSeverity() {
	case systempb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO,
		systempb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING,
		systempb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR,
		systempb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL:
	default:
		return status.Error(codes.InvalidArgument, "severity is required")
	}
	if err := validateNotificationAudience(request.GetAudience()); err != nil {
		return err
	}
	if !validNotificationDeepLink(request.GetDeepLink()) {
		return status.Error(codes.InvalidArgument, "deep_link must be an absolute wendy:// URI with a host, no userinfo, and at most 2048 bytes")
	}
	if metadata := request.GetMetadata(); metadata != nil && proto.Size(metadata) > maxNotificationMetadataBytes {
		return status.Errorf(codes.InvalidArgument, "metadata must be at most %d encoded bytes", maxNotificationMetadataBytes)
	}
	return nil
}

func validateNotificationAudience(audience *systempb.NotificationAudience) error {
	if audience == nil {
		return status.Error(codes.InvalidArgument, "audience is required")
	}
	switch value := audience.GetAudience().(type) {
	case *systempb.NotificationAudience_UserId:
		if !validNotificationIdentifier(value.UserId, 128) {
			return status.Error(codes.InvalidArgument, "audience user_id must contain 1...128 safe ASCII bytes")
		}
	case *systempb.NotificationAudience_OrgTeamId:
		if value.OrgTeamId <= 0 {
			return status.Error(codes.InvalidArgument, "audience org_team_id must be positive")
		}
	case *systempb.NotificationAudience_OrganizationRole:
		switch value.OrganizationRole {
		case systempb.OrganizationRole_ORGANIZATION_ROLE_OWNER,
			systempb.OrganizationRole_ORGANIZATION_ROLE_ADMIN,
			systempb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER,
			systempb.OrganizationRole_ORGANIZATION_ROLE_MEMBER,
			systempb.OrganizationRole_ORGANIZATION_ROLE_VIEWER:
		default:
			return status.Error(codes.InvalidArgument, "audience organization_role is required")
		}
	default:
		return status.Error(codes.InvalidArgument, "audience is required")
	}
	return nil
}

func cloudNotificationAudience(audience *systempb.NotificationAudience) *cloudpb.NotificationAudience {
	if audience == nil {
		return nil
	}
	mapped := &cloudpb.NotificationAudience{}
	switch value := audience.GetAudience().(type) {
	case *systempb.NotificationAudience_UserId:
		mapped.Audience = &cloudpb.NotificationAudience_UserId{UserId: value.UserId}
	case *systempb.NotificationAudience_OrgTeamId:
		mapped.Audience = &cloudpb.NotificationAudience_OrgTeamId{OrgTeamId: value.OrgTeamId}
	case *systempb.NotificationAudience_OrganizationRole:
		mapped.Audience = &cloudpb.NotificationAudience_OrganizationRole{
			OrganizationRole: cloudOrganizationRole(value.OrganizationRole),
		}
	}
	return mapped
}

func cloudOrganizationRole(role systempb.OrganizationRole) cloudpb.OrganizationRole {
	switch role {
	case systempb.OrganizationRole_ORGANIZATION_ROLE_OWNER:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_OWNER
	case systempb.OrganizationRole_ORGANIZATION_ROLE_ADMIN:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_ADMIN
	case systempb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER
	case systempb.OrganizationRole_ORGANIZATION_ROLE_MEMBER:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_MEMBER
	case systempb.OrganizationRole_ORGANIZATION_ROLE_VIEWER:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_VIEWER
	default:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_UNSPECIFIED
	}
}

func cloudNotificationSeverity(severity systempb.NotificationSeverity) cloudpb.NotificationSeverity {
	switch severity {
	case systempb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO
	case systempb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING
	case systempb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR
	case systempb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL
	default:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_UNSPECIFIED
	}
}

func validNotificationIdentifier(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes {
		return false
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '.' || char == '_' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validNotificationText(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validNotificationDeepLink(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.IsAbs() && strings.EqualFold(parsed.Scheme, "wendy") && parsed.User == nil && parsed.Host != ""
}

type notificationDeviceProof struct {
	uri       string
	timestamp string
	signature string
}

func canonicalNotificationDeviceProof(
	request *cloudpb.CreateNotificationV2Request,
	orgID, assetID int32,
	timestamp int64,
) ([]byte, string, string, error) {
	if request == nil {
		return nil, "", "", fmt.Errorf("notification request is required")
	}
	if orgID <= 0 || assetID <= 0 {
		return nil, "", "", fmt.Errorf("device proof requires positive organization and asset IDs")
	}
	if timestamp < 0 {
		return nil, "", "", fmt.Errorf("device proof timestamp must be non-negative")
	}
	requestBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return nil, "", "", fmt.Errorf("deterministically serialize notification request: %w", err)
	}
	uri := fmt.Sprintf("urn:wendy:org:%d:asset:%d", orgID, assetID)
	timestampText := strconv.FormatInt(timestamp, 10)
	canonical := make([]byte, 0, len(deviceProofDomain)+len(deviceProofFullMethod)+len(uri)+len(timestampText)+len(requestBytes)+4)
	canonical = append(canonical, deviceProofDomain...)
	canonical = append(canonical, 0)
	canonical = append(canonical, deviceProofFullMethod...)
	canonical = append(canonical, 0)
	canonical = append(canonical, uri...)
	canonical = append(canonical, 0)
	canonical = append(canonical, timestampText...)
	canonical = append(canonical, 0)
	canonical = append(canonical, requestBytes...)
	return canonical, uri, timestampText, nil
}

func parseDeviceProofPrivateKey(keyData []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("decode provisioned device private key PEM")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "EC PRIVATE KEY":
		parsed, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse provisioned EC private key: %w", err)
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse provisioned PKCS#8 private key: %w", err)
		}
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("provisioned private key is not ECDSA")
		}
	default:
		return nil, fmt.Errorf("unsupported provisioned private key PEM type %q", block.Type)
	}
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("provisioned device proof key must use ECDSA P-256")
	}
	return key, nil
}

func signNotificationDeviceProof(
	request *cloudpb.CreateNotificationV2Request,
	orgID, assetID int32,
	timestamp int64,
	keyData []byte,
	random io.Reader,
) (notificationDeviceProof, error) {
	canonical, uri, timestampText, err := canonicalNotificationDeviceProof(request, orgID, assetID, timestamp)
	if err != nil {
		return notificationDeviceProof{}, err
	}
	key, err := parseDeviceProofPrivateKey(keyData)
	if err != nil {
		return notificationDeviceProof{}, err
	}
	digest := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(random, key, digest[:])
	if err != nil {
		return notificationDeviceProof{}, fmt.Errorf("sign device request proof: %w", err)
	}
	return notificationDeviceProof{
		uri:       uri,
		timestamp: timestampText,
		signature: base64.RawURLEncoding.EncodeToString(signature),
	}, nil
}

func notificationDeviceProofContext(
	ctx context.Context,
	request *cloudpb.CreateNotificationV2Request,
	orgID, assetID int32,
	timestamp int64,
	keyData []byte,
	random io.Reader,
) (context.Context, error) {
	proof, err := signNotificationDeviceProof(request, orgID, assetID, timestamp, keyData, random)
	if err != nil {
		return nil, err
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set(deviceProofURIHeader, proof.uri)
	md.Set(deviceProofTimestampHeader, proof.timestamp)
	md.Set(deviceProofSignatureHeader, proof.signature)
	return metadata.NewOutgoingContext(ctx, md), nil
}

// CloudNotificationSender authenticates its request with a short-lived proof
// signed by the provisioned device key. This preserves device attribution when
// the production Cloud ingress cannot forward the TLS client certificate.
type CloudNotificationSender struct {
	logger          *zap.Logger
	provisioningSvc *ProvisioningService
	mu              sync.Mutex
	connectionKey   [32]byte
	connection      *grpc.ClientConn
}

func NewCloudNotificationSender(logger *zap.Logger, provisioningSvc *ProvisioningService) *CloudNotificationSender {
	return &CloudNotificationSender{logger: logger, provisioningSvc: provisioningSvc}
}

func (s *CloudNotificationSender) CreateNotificationV2(
	ctx context.Context,
	request *cloudpb.CreateNotificationV2Request,
) (*cloudpb.CreateNotificationV2Response, error) {
	cloudHost, orgID, assetID, enrolled := s.provisioningSvc.ProvisioningInfo()
	if !enrolled {
		return nil, status.Error(codes.FailedPrecondition, "device must be enrolled before sending notifications")
	}
	certPEM, chainPEM, keyData := s.provisioningSvc.ProvisioningCerts()
	defer func() {
		for i := range keyData {
			keyData[i] = 0
		}
	}()

	proofCtx, err := notificationDeviceProofContext(
		ctx,
		request,
		orgID,
		assetID,
		time.Now().Unix(),
		keyData,
		rand.Reader,
	)
	if err != nil {
		return nil, fmt.Errorf("create notification device request proof: %w", err)
	}
	connection, err := s.connectionFor(cloudHost, certPEM, chainPEM, keyData)
	if err != nil {
		return nil, err
	}
	response, err := cloudpb.NewNotificationServiceClient(connection).CreateNotificationV2(proofCtx, request)
	if err != nil {
		s.logger.Warn("app-originated notification delivery failed",
			zap.String("app_id", request.GetSourceAppId()),
			zap.String("source_id", request.GetSourceId()),
			zap.Error(err))
		return nil, err
	}
	return response, nil
}

func (s *CloudNotificationSender) connectionFor(
	cloudHost, certPEM, chainPEM string,
	keyData []byte,
) (*grpc.ClientConn, error) {
	key := sha256.Sum256([]byte(cloudHost + "\x00" + certPEM))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connection != nil && s.connectionKey == key {
		return s.connection, nil
	}

	certBundle := append([]byte(certPEM), '\n')
	certBundle = append(certBundle, []byte(chainPEM)...)
	certificate, err := tls.X509KeyPair(certBundle, keyData)
	if err != nil {
		return nil, fmt.Errorf("parse device notification client certificate: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system roots for Wendy Cloud: %w", err)
	}
	// SECURITY: The enrollment chain is controlled by Wendy Cloud and is also
	// used by the existing telemetry flusher. Adding it supports direct local
	// broker deployments whose server certificate uses the Wendy development CA.
	if chainPEM != "" && !roots.AppendCertsFromPEM([]byte(chainPEM)) {
		return nil, fmt.Errorf("parse Wendy enrollment CA chain")
	}
	connection, err := grpc.NewClient(
		normalizeCloudHost(cloudHost),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
			RootCAs:      roots,
		})),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to Wendy Cloud: %w", err)
	}
	if s.connection != nil {
		_ = s.connection.Close()
	}
	s.connection = connection
	s.connectionKey = key
	return connection, nil
}
