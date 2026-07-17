package services

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// RotateCertificateIfMissingSAN transparently reissues the device certificate when it
// resolves its Wendy identity only from the legacy CommonName and carries no
// "urn:wendy:org:..." URI SAN. Devices enrolled before SAN issuance hold such certs;
// downstream mTLS identity now prefers the SAN, so this migrates them without operator
// action. It is best-effort and intended to be called once at agent startup: any error
// is returned for logging but must never stop the agent from serving with its existing
// (still-valid) certificate.
//
// Unlike the CLI (which prompts), the agent rotates automatically — it is unattended.
func (s *ProvisioningService) RotateCertificateIfMissingSAN(ctx context.Context) error {
	s.mu.Lock()
	if !s.enrolled {
		s.mu.Unlock()
		return nil
	}
	certPEM := s.certPEM
	chainPEM := s.chainPEM
	cloudHost := s.cloudHost
	orgID := s.orgID
	assetID := s.assetID
	keyPEM := make([]byte, len(s.keyPEM))
	copy(keyPEM, s.keyPEM)
	s.mu.Unlock()

	leaf, err := parseLeafCertificate(certPEM)
	if err != nil {
		return fmt.Errorf("parsing device certificate: %w", err)
	}
	if certs.HasWendyIdentitySAN(leaf) {
		return nil // already carries the identity SAN; nothing to do
	}
	if len(keyPEM) == 0 {
		return fmt.Errorf("device private key unavailable; cannot rotate certificate")
	}

	s.logger.Info("device certificate lacks an identity SAN; rotating via cloud refresh",
		zap.Int32("org_id", orgID), zap.Int32("asset_id", assetID))

	// The refreshed CSR now requests the authoritative asset identity SAN (and both EKUs,
	// matching the original enrollment CSR). The device key is reused so the reissued cert
	// simply gains the SAN.
	commonName := fmt.Sprintf("sh/wendy/%d/%d", orgID, assetID)
	csrPEM, err := certs.GenerateCSR(keyPEM, commonName, certs.AssetURN(orgID, assetID),
		x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return fmt.Errorf("generating rotation CSR: %w", err)
	}

	conn, err := refreshDial(certificateServiceAddr(cloudHost), certPEM, chainPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("connecting to cloud: %w", err)
	}
	defer conn.Close()

	// The cloud RefreshCertificate handler derives the caller identity from the client-cert
	// metadata header (as the tunnel/mesh paths do) and pins the reissued SAN to it.
	certHeader := fmt.Sprintf("URI=urn:wendy:org:%d:asset:%d", orgID, assetID)
	rctx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-wendy-client-cert", certHeader,
		"x-forwarded-client-cert", certHeader,
	))

	resp, err := cloudpb.NewCertificateServiceClient(conn).RefreshCertificate(rctx,
		&cloudpb.RefreshCertificateRequest{PemCsr: csrPEM})
	if err != nil {
		return fmt.Errorf("refreshing certificate: %w", err)
	}
	if resp.GetError() != nil {
		return fmt.Errorf("cloud refused certificate rotation: %s", resp.GetError().GetMessage())
	}
	newCert := resp.GetCertificate()
	if newCert == nil {
		return fmt.Errorf("cloud returned an empty certificate")
	}
	newCertPEM := newCert.GetPemCertificate()
	newChainPEM := newCert.GetPemCertificateChain()

	// Guard against a no-op rotation (a cloud that predates SAN issuance): apply the fresh
	// cert regardless (it is valid), but warn so a persistent gap is visible rather than a
	// silent rotate-every-boot loop.
	if newLeaf, perr := parseLeafCertificate(newCertPEM); perr == nil && !certs.HasWendyIdentitySAN(newLeaf) {
		s.logger.Warn("rotated certificate still lacks an identity SAN; cloud may predate SAN issuance",
			zap.Int32("org_id", orgID), zap.Int32("asset_id", assetID))
	}

	state := &provisioningState{
		Enrolled:  true,
		CloudHost: cloudHost,
		OrgID:     orgID,
		AssetID:   assetID,
		CertPEM:   newCertPEM,
		ChainPEM:  newChainPEM,
	}
	s.mu.Lock()
	if err := s.saveState(state); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("persisting rotated certificate: %w", err)
	}
	s.certPEM = newCertPEM
	s.chainPEM = newChainPEM
	cb := s.OnProvisioned
	var cbKey []byte
	if cb != nil {
		cbKey = make([]byte, len(s.keyPEM))
		copy(cbKey, s.keyPEM)
	}
	s.mu.Unlock()

	if err := s.writePEMFiles(string(keyPEM), newCertPEM, newChainPEM); err != nil {
		s.logger.Error("failed to write rotated PEM files", zap.Error(err))
		// Non-fatal: provisioning.json is the source of truth.
	}
	if cb != nil {
		cb(newCertPEM, newChainPEM, cbKey)
	}
	s.logger.Info("device certificate rotated to include identity SAN",
		zap.Int32("org_id", orgID), zap.Int32("asset_id", assetID))
	return nil
}

// parseLeafCertificate normalizes a possibly-multi-block / trailing-bytes PEM to its leaf
// and parses it.
func parseLeafCertificate(certPEM string) (*x509.Certificate, error) {
	leafPEM, err := certs.LeafCertificatePEM(certPEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(leafPEM))
	if block == nil {
		return nil, fmt.Errorf("decoding certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// refreshDial connects to the cloud certificate service for a refresh. On a :443 endpoint it
// presents the device's mTLS client certificate and verifies the cloud's server certificate
// against the system roots; otherwise (local dev) it uses plaintext. Mirrors the CLI's refresh
// transport selection.
func refreshDial(addr, certPEM, chainPEM string, keyPEM []byte) (*grpc.ClientConn, error) {
	if strings.HasSuffix(addr, ":443") {
		tlsCfg, err := certs.LoadTLSConfig(certPEM, chainPEM, string(keyPEM), "")
		if err != nil {
			return nil, fmt.Errorf("building mTLS config: %w", err)
		}
		return grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
