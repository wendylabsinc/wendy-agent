package services

import (
	"context"
	"errors"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TrustedKeysService is the owner-only management of the device's local
// operator keys. The Authorizer admits only an owner authenticated by a
// trusted key; every change is written to the audit log with the acting
// key's fingerprint. While the device is cloud-enrolled the Authorizer refuses
// every call before it gets here (FailedPrecondition, audited): cloud manages
// access then.
type TrustedKeysService struct {
	agentpbv2.UnimplementedWendyTrustedKeysServiceServer
	logger *zap.Logger
	keys   *trustedkeys.Store
	now    func() time.Time
}

func NewTrustedKeysService(logger *zap.Logger, keys *trustedkeys.Store) *TrustedKeysService {
	return &TrustedKeysService{logger: logger, keys: keys, now: time.Now}
}

var roleToProto = map[trustedkeys.Role]agentpbv2.TrustedKeyRole{
	trustedkeys.RoleOwner:    agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OWNER,
	trustedkeys.RoleOperator: agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_OPERATOR,
	trustedkeys.RoleViewer:   agentpbv2.TrustedKeyRole_TRUSTED_KEY_ROLE_VIEWER,
}

func roleFromProto(r agentpbv2.TrustedKeyRole) trustedkeys.Role {
	for role, pr := range roleToProto {
		if pr == r {
			return role
		}
	}
	return trustedkeys.RoleNone
}

func trustedKeyToProto(e trustedkeys.Entry) *agentpbv2.TrustedKey {
	return &agentpbv2.TrustedKey{
		Fingerprint: e.Fingerprint(),
		PublicKey:   e.SPKI(),
		Role:        roleToProto[e.EntryRole()],
		Label:       e.Label,
		AddedAt:     e.AddedAt.Unix(),
		AddedBy:     e.AddedBy,
	}
}

// actor is the acting owner key. The Authorizer guarantees it; a missing
// principal means the service was registered without it, so refuse.
func actor(ctx context.Context) (interceptor.Principal, error) {
	p, ok := interceptor.PrincipalFromContext(ctx)
	if !ok || p.Kind != interceptor.KindLocalKey || p.Role != trustedkeys.RoleOwner {
		return interceptor.Principal{}, status.Error(codes.PermissionDenied, "trusted key management requires an owner key")
	}
	return p, nil
}

func (s *TrustedKeysService) AddTrustedKey(ctx context.Context, req *agentpbv2.AddTrustedKeyRequest) (*agentpbv2.AddTrustedKeyResponse, error) {
	p, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	role := roleFromProto(req.GetRole())
	if role == trustedkeys.RoleNone {
		return nil, status.Error(codes.InvalidArgument, "role must be owner, operator or viewer")
	}
	spki, err := trustedkeys.ParsePublicKey(req.GetPublicKey())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry, err := s.keys.Add(spki, role, req.GetLabel(), p.Fingerprint, s.now())
	switch {
	case errors.Is(err, trustedkeys.ErrAlreadyExists):
		return nil, status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, trustedkeys.ErrDormant):
		s.auditDormant(p, "add_refused", trustedkeys.Fingerprint(spki))
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, trustedkeys.ErrActorNotOwner):
		s.logger.Warn("audit: trusted key add refused",
			zap.String("audit", "trusted_keys"),
			zap.String("action", "add_refused"),
			zap.String("actor_fingerprint", p.Fingerprint),
			zap.String("fingerprint", trustedkeys.Fingerprint(spki)),
			zap.String("reason", "actor not owner"))
		return nil, status.Error(codes.PermissionDenied, err.Error())
	case err != nil:
		return nil, status.Errorf(codes.Internal, "adding trusted key: %v", err)
	}
	s.logger.Info("audit: trusted key added",
		zap.String("audit", "trusted_keys"),
		zap.String("action", "add"),
		zap.String("actor_fingerprint", p.Fingerprint),
		zap.String("fingerprint", entry.Fingerprint()),
		zap.String("role", role.String()),
		zap.String("label", entry.Label))
	return &agentpbv2.AddTrustedKeyResponse{Key: trustedKeyToProto(entry)}, nil
}

func (s *TrustedKeysService) ListTrustedKeys(ctx context.Context, _ *agentpbv2.ListTrustedKeysRequest) (*agentpbv2.ListTrustedKeysResponse, error) {
	if _, err := actor(ctx); err != nil {
		return nil, err
	}
	resp := &agentpbv2.ListTrustedKeysResponse{}
	for _, e := range s.keys.List() {
		resp.Keys = append(resp.Keys, trustedKeyToProto(e))
	}
	return resp, nil
}

func (s *TrustedKeysService) RemoveTrustedKey(ctx context.Context, req *agentpbv2.RemoveTrustedKeyRequest) (*agentpbv2.RemoveTrustedKeyResponse, error) {
	p, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	removed, err := s.keys.Remove(req.GetFingerprint(), p.Fingerprint)
	switch {
	case errors.Is(err, trustedkeys.ErrActorNotOwner):
		s.logger.Warn("audit: trusted key removal refused",
			zap.String("audit", "trusted_keys"),
			zap.String("action", "remove_refused"),
			zap.String("actor_fingerprint", p.Fingerprint),
			zap.String("fingerprint", req.GetFingerprint()),
			zap.String("reason", "actor not owner"))
		return nil, status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, trustedkeys.ErrDormant):
		s.auditDormant(p, "remove_refused", req.GetFingerprint())
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, trustedkeys.ErrNotFound):
		return nil, status.Error(codes.NotFound, err.Error())
	case errors.Is(err, trustedkeys.ErrLastOwner):
		s.logger.Warn("audit: trusted key removal refused",
			zap.String("audit", "trusted_keys"),
			zap.String("action", "remove_refused"),
			zap.String("actor_fingerprint", p.Fingerprint),
			zap.String("fingerprint", req.GetFingerprint()),
			zap.String("reason", "last owner"))
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case err != nil:
		return nil, status.Errorf(codes.Internal, "removing trusted key: %v", err)
	}
	s.logger.Info("audit: trusted key removed",
		zap.String("audit", "trusted_keys"),
		zap.String("action", "remove"),
		zap.String("actor_fingerprint", p.Fingerprint),
		zap.String("fingerprint", removed.Fingerprint()),
		zap.String("role", removed.EntryRole().String()),
		zap.String("label", removed.Label))
	return &agentpbv2.RemoveTrustedKeyResponse{}, nil
}

// auditDormant records a change refused inside the store because the device
// was enrolled after the call passed the Authorizer.
func (s *TrustedKeysService) auditDormant(p interceptor.Principal, action, fingerprint string) {
	s.logger.Warn("audit: trusted key change refused",
		zap.String("audit", "trusted_keys"),
		zap.String("action", action),
		zap.String("actor_fingerprint", p.Fingerprint),
		zap.String("fingerprint", fingerprint),
		zap.String("reason", "cloud-enrolled"))
}
