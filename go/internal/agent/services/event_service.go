package services

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// EventPublisher forwards an attributed event to Wendy Cloud.
type EventPublisher interface {
	PublishEvent(context.Context, *cloudpb.PublishEventRequest) (*cloudpb.PublishEventResponse, error)
}

// WendyEventService serves one app-specific socket. sourceAppID comes from the
// entitlement mount and is never accepted from workload-controlled input.
type WendyEventService struct {
	agentpb.UnimplementedWendyEventServiceServer
	sourceAppID string
	publisher   EventPublisher
	limiter     eventRateLimiter
}

func NewWendyEventService(sourceAppID string, publisher EventPublisher) *WendyEventService {
	return &WendyEventService{
		sourceAppID: sourceAppID,
		publisher:   publisher,
		limiter:     newEventRateLimiter(10, time.Second),
	}
}

func (s *WendyEventService) PublishEvent(
	ctx context.Context,
	req *agentpb.PublishAppEventRequest,
) (*cloudpb.PublishEventResponse, error) {
	if s.publisher == nil {
		return nil, status.Error(codes.Unavailable, "event delivery is unavailable")
	}
	if err := validateAppEvent(req); err != nil {
		return nil, err
	}
	if !s.limiter.allow(time.Now()) {
		return nil, status.Error(codes.ResourceExhausted, "event publishing rate exceeded; retry later with the same source_event_id")
	}
	cloudRequest := &cloudpb.PublishEventRequest{
		SourceEventId: req.GetSourceEventId(),
		AppId:         s.sourceAppID,
		Title:         req.GetTitle(),
		Body:          req.GetBody(),
		Severity:      req.GetSeverity(),
		Target:        req.GetTarget(),
	}
	response, err := s.publisher.PublishEvent(ctx, cloudRequest)
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Errorf(codes.Unavailable, "publish event: %v", err)
	}
	return response, nil
}

// CloudEventPublisher authenticates with the device certificate held by the
// ProvisioningService. Cloud derives organization and asset from that identity.
type eventRateLimiter struct {
	mu             sync.Mutex
	tokens         float64
	capacity       float64
	refillInterval time.Duration
	lastRefill     time.Time
}

func newEventRateLimiter(capacity int, refillInterval time.Duration) eventRateLimiter {
	return eventRateLimiter{
		tokens:         float64(capacity),
		capacity:       float64(capacity),
		refillInterval: refillInterval,
		lastRefill:     time.Now(),
	}
}

func (l *eventRateLimiter) allow(now time.Time) bool {
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

func validateAppEvent(request *agentpb.PublishAppEventRequest) error {
	if !validEventIdentifier(request.GetSourceEventId(), 128, true) {
		return status.Error(codes.InvalidArgument, "source_event_id must contain 1...128 safe ASCII bytes")
	}
	if !validEventText(request.GetTitle(), 120) {
		return status.Error(codes.InvalidArgument, "title must contain 1...120 printable UTF-8 bytes")
	}
	if !validEventText(request.GetBody(), 2000) {
		return status.Error(codes.InvalidArgument, "body must contain 1...2000 printable UTF-8 bytes")
	}
	switch request.GetSeverity() {
	case cloudpb.EventSeverity_EVENT_SEVERITY_INFO,
		cloudpb.EventSeverity_EVENT_SEVERITY_WARNING,
		cloudpb.EventSeverity_EVENT_SEVERITY_ERROR,
		cloudpb.EventSeverity_EVENT_SEVERITY_CRITICAL:
	default:
		return status.Error(codes.InvalidArgument, "severity is required")
	}
	live := request.GetTarget().GetLive()
	if live == nil || !validEventText(live.GetCameraId(), 256) {
		return status.Error(codes.InvalidArgument, "a Live target with a 1...256 byte camera_id is required")
	}
	return nil
}

func validEventIdentifier(value string, maximumBytes int, allowColon bool) bool {
	if value == "" || len(value) > maximumBytes {
		return false
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '.' || char == '_' ||
			(allowColon && char == ':') {
			continue
		}
		return false
	}
	return true
}

func validEventText(value string, maximumBytes int) bool {
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

type CloudEventPublisher struct {
	logger          *zap.Logger
	provisioningSvc *ProvisioningService
	mu              sync.Mutex
	connectionKey   [32]byte
	connection      *grpc.ClientConn
}

func NewCloudEventPublisher(logger *zap.Logger, provisioningSvc *ProvisioningService) *CloudEventPublisher {
	return &CloudEventPublisher{logger: logger, provisioningSvc: provisioningSvc}
}

func (p *CloudEventPublisher) PublishEvent(
	ctx context.Context,
	request *cloudpb.PublishEventRequest,
) (*cloudpb.PublishEventResponse, error) {
	cloudHost, _, _, enrolled := p.provisioningSvc.ProvisioningInfo()
	if !enrolled {
		return nil, status.Error(codes.FailedPrecondition, "device must be enrolled before publishing events")
	}
	certPEM, chainPEM, keyData := p.provisioningSvc.ProvisioningCerts()
	defer func() {
		for i := range keyData {
			keyData[i] = 0
		}
	}()

	connection, err := p.connectionFor(cloudHost, certPEM, chainPEM, keyData)
	if err != nil {
		return nil, err
	}
	response, err := cloudpb.NewEventServiceClient(connection).PublishEvent(ctx, request)
	if err != nil {
		p.logger.Warn("app-originated event delivery failed",
			zap.String("app_id", request.GetAppId()),
			zap.String("source_event_id", request.GetSourceEventId()),
			zap.Error(err))
		return nil, err
	}
	return response, nil
}

func (p *CloudEventPublisher) connectionFor(
	cloudHost, certPEM, chainPEM string,
	keyData []byte,
) (*grpc.ClientConn, error) {
	key := sha256.Sum256([]byte(cloudHost + "\x00" + certPEM))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connection != nil && p.connectionKey == key {
		return p.connection, nil
	}

	certBundle := append([]byte(certPEM), '\n')
	certBundle = append(certBundle, []byte(chainPEM)...)
	certificate, err := tls.X509KeyPair(certBundle, keyData)
	if err != nil {
		return nil, fmt.Errorf("parse device event client certificate: %w", err)
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
	if p.connection != nil {
		_ = p.connection.Close()
	}
	p.connection = connection
	p.connectionKey = key
	return connection, nil
}
