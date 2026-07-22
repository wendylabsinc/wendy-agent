package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

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
}

func NewWendyEventService(sourceAppID string, publisher EventPublisher) *WendyEventService {
	return &WendyEventService{sourceAppID: sourceAppID, publisher: publisher}
}

func (s *WendyEventService) PublishEvent(
	ctx context.Context,
	req *agentpb.PublishAppEventRequest,
) (*cloudpb.PublishEventResponse, error) {
	if s.publisher == nil {
		return nil, status.Error(codes.Unavailable, "event delivery is unavailable")
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
type CloudEventPublisher struct {
	logger          *zap.Logger
	provisioningSvc *ProvisioningService
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

	certBundle := append([]byte(certPEM), '\n')
	certBundle = append(certBundle, []byte(chainPEM)...)
	certificate, err := tls.X509KeyPair(certBundle, keyData)
	if err != nil {
		return nil, fmt.Errorf("parse device event client certificate: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if chainPEM != "" {
		roots.AppendCertsFromPEM([]byte(chainPEM))
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
	defer connection.Close()

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
