package interceptor

import (
	"context"
	"crypto/x509"

	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Listener names the transport a call arrived on; it decides how the caller
// is identified.
type Listener int

const (
	// ListenerMTLS is the TCP mTLS listener (and the BLE L2CAP channel, which
	// runs the same mutual TLS).
	ListenerMTLS Listener = iota
	// ListenerAdminSocket is the unix control socket bind-mounted into
	// containers with the admin entitlement.
	ListenerAdminSocket
	// ListenerPreEnroll is today's plaintext pre-enrollment listener. It has
	// no caller identity; it exists only on devices without a trusted-keys
	// file and goes away in WDY-3882 PR 2.
	ListenerPreEnroll
)

// PrincipalKind is how the caller was identified.
type PrincipalKind int

const (
	KindNone PrincipalKind = iota
	// KindLocalKey: client certificate whose public key is in the device's
	// trusted-keys file. Role comes from the file.
	KindLocalKey
	// KindTenantUser: a CA-issued (cloud / pki-core) operator certificate of
	// the device's tenant. Always RoleOperator: tenant admin is not
	// expressible in the certificate yet (spec 2 is a separate item).
	KindTenantUser
	// KindPeerDevice: another device's certificate in the same tenant. No
	// role; only the methods in peerMethods.
	KindPeerDevice
	// KindAdminSocket: a container with the admin entitlement. RoleOperator,
	// so an app cannot manage keys or unprovision the device.
	KindAdminSocket
	// KindPreEnroll: anonymous caller on the plaintext pre-enroll listener.
	KindPreEnroll
)

func (k PrincipalKind) String() string {
	switch k {
	case KindLocalKey:
		return "local-key"
	case KindTenantUser:
		return "tenant-user"
	case KindPeerDevice:
		return "peer-device"
	case KindAdminSocket:
		return "admin-socket"
	case KindPreEnroll:
		return "pre-enroll"
	default:
		return "none"
	}
}

// Principal is the authenticated caller of one RPC.
type Principal struct {
	Kind PrincipalKind
	Role trustedkeys.Role
	// Fingerprint of the client certificate's public key, when there is one.
	Fingerprint string
}

type principalKey struct{}

// PrincipalFromContext returns the caller the Authorizer admitted.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Authorizer is the one method→role check every agent listener runs.
type Authorizer struct {
	Logger   *zap.Logger
	Listener Listener
	// Keys is the trusted-keys store; nil or empty means no local operators.
	Keys *trustedkeys.Store
	// Expected is the device's own tenant scope (zero when not enrolled) and
	// OrgMode the tenant-equality mode, both for CA-issued certificates.
	Expected certs.Scope
	OrgMode  OrgMode
}

// Authorize identifies the caller and checks it may call fullMethod. Methods
// absent from the table are denied to everyone.
func (a *Authorizer) Authorize(ctx context.Context, fullMethod string) (Principal, error) {
	p, err := a.identify(ctx)
	if err != nil {
		return Principal{}, err
	}
	if !permitted(p, fullMethod) {
		a.Logger.Warn("rejected RPC: caller lacks the required role",
			zap.String("method", fullMethod),
			zap.String("principal", p.Kind.String()),
			zap.String("role", p.Role.String()),
			zap.String("fingerprint", p.Fingerprint),
			zap.String("remote", peerAddr(ctx)))
		return Principal{}, status.Errorf(codes.PermissionDenied, "%s is not permitted for this caller", fullMethod)
	}
	return p, nil
}

func permitted(p Principal, fullMethod string) bool {
	min, mapped := methodRoles[fullMethod]
	if !mapped {
		return false
	}
	if ownerKeyMethods[fullMethod] {
		return p.Kind == KindLocalKey && p.Role == trustedkeys.RoleOwner
	}
	switch p.Kind {
	case KindPeerDevice:
		return peerMethods[fullMethod]
	case KindTenantUser:
		// Unprovision on an enrolled device is the tenant's decision; the
		// handler enforces the Cloud-signed reset authorization on direct-PKI
		// devices. Keeps today's `wendy device unenroll` working until tenant
		// admin is expressible in the certificate.
		if tenantUnprovisionMethods[fullMethod] {
			return true
		}
	}
	return p.Role >= min
}

func (a *Authorizer) identify(ctx context.Context) (Principal, error) {
	switch a.Listener {
	case ListenerPreEnroll:
		return Principal{Kind: KindPreEnroll, Role: trustedkeys.RoleOwner}, nil
	case ListenerAdminSocket:
		return Principal{Kind: KindAdminSocket, Role: trustedkeys.RoleOperator}, nil
	}

	leaf, err := peerLeaf(ctx)
	if err != nil {
		a.Logger.Warn("rejected unauthenticated gRPC caller", zap.String("remote", peerAddr(ctx)), zap.Error(err))
		return Principal{}, err
	}
	ident, hasIdent, identErr := certs.IdentityFromCert(leaf)
	if identErr == nil && !hasIdent {
		// A certificate naming no tenant is a local operator when its key
		// is trusted. A key never lifts a certificate that names a tenant
		// into a local role, and a local key never carries a tenant identity
		// (the handshake refuses self-issued certificates that name one).
		if role, fp := a.Keys.Lookup(leaf.RawSubjectPublicKeyInfo); role != trustedkeys.RoleNone {
			if !hasEKU(leaf, x509.ExtKeyUsageClientAuth) {
				return Principal{}, status.Error(codes.Unauthenticated, "certificate is not valid for client authentication")
			}
			return Principal{Kind: KindLocalKey, Role: role, Fingerprint: fp}, nil
		}
	}
	if err := CheckMTLS(ctx, a.Logger, a.Expected, a.OrgMode); err != nil {
		return Principal{}, err
	}
	fp := trustedkeys.Fingerprint(leaf.RawSubjectPublicKeyInfo)
	if hasIdent && ident.EntityType == certs.EntityAsset {
		return Principal{Kind: KindPeerDevice, Fingerprint: fp}, nil
	}
	return Principal{Kind: KindTenantUser, Role: trustedkeys.RoleOperator, Fingerprint: fp}, nil
}

func peerLeaf(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return nil, status.Error(codes.Unauthenticated, "missing peer credentials")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "mTLS authentication required")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, status.Error(codes.Unauthenticated, "client certificate not verified")
	}
	return tlsInfo.State.PeerCertificates[0], nil
}

func hasEKU(leaf *x509.Certificate, want x509.ExtKeyUsage) bool {
	for _, eku := range leaf.ExtKeyUsage {
		if eku == want {
			return true
		}
	}
	return false
}

// Unary is the unary form of Authorize.
func (a *Authorizer) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		p, err := a.Authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(context.WithValue(ctx, principalKey{}, p), req)
	}
}

// Stream is the streaming form of Authorize.
func (a *Authorizer) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		p, err := a.Authorize(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &principalStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), principalKey{}, p)})
	}
}

type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalStream) Context() context.Context { return s.ctx }

// ServerOptions installs the Authorizer as the first interceptor.
func (a *Authorizer) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(a.Unary()),
		grpc.ChainStreamInterceptor(a.Stream()),
	}
}

// Unmapped lists the methods served by srv that have no entry in the role
// table. They are denied at runtime; the agent refuses to start with any, so
// a new RPC cannot ship without a role decision.
func Unmapped(srv interface {
	GetServiceInfo() map[string]grpc.ServiceInfo
}) []string {
	var out []string
	for name, info := range srv.GetServiceInfo() {
		for _, m := range info.Methods {
			full := "/" + name + "/" + m.Name
			if _, ok := methodRoles[full]; !ok {
				out = append(out, full)
			}
		}
	}
	return out
}
