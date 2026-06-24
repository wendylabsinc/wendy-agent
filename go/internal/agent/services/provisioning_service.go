package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// provisioningState is persisted to disk at configPath/provisioning.json.
// NOTE: KeyPEM is retained only for one-time migration of existing deployments;
// new writes never populate it — the private key lives exclusively in
// device-key.pem (mode 0o400) and is never written to provisioning.json.
type provisioningState struct {
	Enrolled  bool   `json:"enrolled"`
	CloudHost string `json:"cloudHost,omitempty"`
	OrgID     int32  `json:"orgId,omitempty"`
	AssetID   int32  `json:"assetId,omitempty"`
	KeyPEM    string `json:"keyPem,omitempty"` // read-only: migration only; never written
	CertPEM   string `json:"certPem,omitempty"`
	ChainPEM  string `json:"chainPem,omitempty"`
}

type CloudDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// DefaultCloudDialer connects to the cloud gRPC server with plaintext transport.
func DefaultCloudDialer(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	if strings.HasSuffix(addr, ":443") {
		return grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func certificateServiceAddr(cloudHost string) string {
	if _, _, err := net.SplitHostPort(cloudHost); err == nil {
		return cloudHost
	}
	return net.JoinHostPort(cloudHost, "50051")
}

// OnProvisionedFunc is called when provisioning completes successfully.
// keyData is the raw PEM bytes of the private key; callers should zero it
// when done. certPEM and chainPEM are plain strings (public material).
type OnProvisionedFunc func(certPEM, chainPEM string, keyData []byte)

// OnUnprovisionedFunc is called after the device has been unprovisioned and its
// state has been cleared. It is invoked asynchronously, shortly after the RPC
// response is sent, so implementations can revert the mDNS advertisement and
// restart the agent process. If nil, no post-unprovision action is taken.
type OnUnprovisionedFunc func()

// ProvisioningService implements agentpb.WendyProvisioningServiceServer.
type ProvisioningService struct {
	agentpb.UnimplementedWendyProvisioningServiceServer
	logger          *zap.Logger
	configPath      string
	mu              sync.Mutex
	enrolled        bool
	cloudHost       string
	orgID           int32
	assetID         int32
	keyPEM          []byte // stored as []byte so it can be zeroed on rotation/shutdown
	certPEM         string
	chainPEM        string
	CloudDialer     CloudDialer
	OnProvisioned   OnProvisionedFunc
	OnUnprovisioned OnUnprovisionedFunc
}

func NewProvisioningService(logger *zap.Logger, configPath string) *ProvisioningService {
	svc := &ProvisioningService{
		logger:      logger,
		configPath:  configPath,
		CloudDialer: DefaultCloudDialer,
	}
	svc.loadState()
	return svc
}

// ProvisioningCerts returns the stored certificate material if the agent is provisioned.
// The private key is returned as a copy so callers can zero it after use.
// Returns empty cert/chain and nil key if not provisioned.
func (s *ProvisioningService) ProvisioningCerts() (certPEM, chainPEM string, keyData []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keyPEM) == 0 {
		return s.certPEM, s.chainPEM, nil
	}
	keyData = make([]byte, len(s.keyPEM))
	copy(keyData, s.keyPEM)
	return s.certPEM, s.chainPEM, keyData
}

func (s *ProvisioningService) ProvisioningInfo() (cloudHost string, orgID, assetID int32, enrolled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cloudHost, s.orgID, s.assetID, s.enrolled
}

// IsProvisioned checks whether the agent is enrolled with a cloud organization.
func (s *ProvisioningService) IsProvisioned(_ context.Context, _ *agentpb.IsProvisionedRequest) (*agentpb.IsProvisionedResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.enrolled {
		return &agentpb.IsProvisionedResponse{
			Response: &agentpb.IsProvisionedResponse_Provisioned{
				Provisioned: &agentpb.ProvisionedResponse{
					CloudHost:      s.cloudHost,
					OrganizationId: s.orgID,
					AssetId:        s.assetID,
				},
			},
		}, nil
	}

	return &agentpb.IsProvisionedResponse{
		Response: &agentpb.IsProvisionedResponse_NotProvisioned{
			NotProvisioned: &agentpb.NotProvisionedResponse{},
		},
	}, nil
}

// StartProvisioning generates a CSR, exchanges with the cloud, and stores certificates.
func (s *ProvisioningService) StartProvisioning(ctx context.Context, req *agentpb.StartProvisioningRequest) (*agentpb.StartProvisioningResponse, error) {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()

	if s.enrolled {
		return nil, status.Error(codes.FailedPrecondition, "agent is already provisioned")
	}

	s.logger.Info("Starting provisioning",
		zap.Int32("org_id", req.GetOrganizationId()),
		zap.String("cloud_host", req.GetCloudHost()),
		zap.Int32("asset_id", req.GetAssetId()),
	)

	// Reuse the device's existing private key if present, otherwise generate a new one.
	keyPEM, err := s.loadOrGenerateKey()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load or generate key pair: %v", err)
	}

	// Generate CSR using org and asset as common name. The device identity acts
	// as both a TLS client (to the cloud) and a TLS server (agent gRPC and tunnel
	// endpoints), so request both EKUs.
	commonName := fmt.Sprintf("sh/wendy/%d/%d", req.GetOrganizationId(), req.GetAssetId())
	csrPEM, err := certs.GenerateCSR(keyPEM, commonName,
		x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate CSR: %v", err)
	}

	// Connect to the cloud gRPC server.
	cloudAddr := certificateServiceAddr(req.GetCloudHost())
	cloudConn, err := s.CloudDialer(ctx, cloudAddr)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "connecting to cloud: %v", err)
	}
	defer cloudConn.Close()

	// Send the CSR to the cloud for certificate issuance.
	certClient := cloudpb.NewCertificateServiceClient(cloudConn)
	issueResp, err := certClient.IssueCertificate(ctx, &cloudpb.IssueCertificateRequest{
		PemCsr:          csrPEM,
		EnrollmentToken: req.GetEnrollmentToken(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issuing certificate from cloud: %v", err)
	}

	// Check for error in the response.
	if issueResp.GetError() != nil {
		certErr := issueResp.GetError()
		return nil, status.Errorf(codes.Internal, "cloud certificate issuance failed: %s", certErr.GetMessage())
	}

	// Extract certificate material from the response.
	cert := issueResp.GetCertificate()
	if cert == nil {
		return nil, status.Error(codes.Internal, "cloud returned empty certificate")
	}

	certPEM := cert.GetPemCertificate()
	chainPEM := cert.GetPemCertificateChain()

	// Build the state struct from the request/cert values WITHOUT first mutating
	// s.* fields. Only apply the state to s.* after saveState succeeds so that a
	// disk-write failure does not leave the agent permanently stuck as "already
	// provisioned". The private key is never written to provisioning.json —
	// it lives only in device-key.pem (written by loadOrGenerateKey).
	state := &provisioningState{
		Enrolled:  true,
		CloudHost: req.GetCloudHost(),
		OrgID:     req.GetOrganizationId(),
		AssetID:   req.GetAssetId(),
		CertPEM:   certPEM,
		ChainPEM:  chainPEM,
	}
	if err := s.saveState(state); err != nil {
		s.logger.Error("Failed to persist provisioning state", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to save provisioning state: %v", err)
	}

	// Persist succeeded — now it is safe to update in-memory state.
	s.enrolled = true
	s.cloudHost = state.CloudHost
	s.orgID = state.OrgID
	s.assetID = state.AssetID
	s.keyPEM = keyPEM
	s.certPEM = certPEM
	s.chainPEM = chainPEM

	// Write individual PEM files so the container registry can mount and use them.
	// string(keyPEM) creates a temporary copy; filesystem write cannot be avoided.
	if err := s.writePEMFiles(string(keyPEM), certPEM, chainPEM); err != nil {
		s.logger.Error("Failed to write PEM files for registry", zap.Error(err))
		// Non-fatal: provisioning.json is the source of truth.
	}

	s.logger.Info("Provisioning completed successfully",
		zap.Int32("org_id", s.orgID),
		zap.Int32("asset_id", s.assetID),
	)

	// Capture callback data and unlock before invoking to prevent deadlock
	// (callbacks may call back into ProvisioningService) and to pass a copy
	// so callers can safely zero the slice without corrupting stored state.
	cb := s.OnProvisioned
	var cbKeyPEM []byte
	if cb != nil {
		cbKeyPEM = make([]byte, len(keyPEM))
		copy(cbKeyPEM, keyPEM)
	}
	locked = false
	s.mu.Unlock()
	if cb != nil {
		cb(certPEM, chainPEM, cbKeyPEM)
	}
	return &agentpb.StartProvisioningResponse{}, nil
}

// Unprovision resets the device to an unprovisioned state. It deletes the
// stored enrollment certificates and provisioning state from disk, clears the
// in-memory state, and (if configured) invokes OnUnprovisioned shortly after
// the response is sent so the agent can revert its mDNS advertisement and
// restart into plaintext mode.
func (s *ProvisioningService) Unprovision(_ context.Context, _ *agentpb.UnprovisionRequest) (*agentpb.UnprovisionResponse, error) {
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()

	if !s.enrolled {
		return nil, status.Error(codes.FailedPrecondition, "agent is not provisioned")
	}

	s.logger.Info("Unprovisioning device",
		zap.Int32("org_id", s.orgID),
		zap.Int32("asset_id", s.assetID),
	)

	if err := s.clearStateFiles(); err != nil {
		s.logger.Error("Failed to delete provisioning state files", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to delete provisioning state: %v", err)
	}

	// Zero the in-memory key before dropping the reference, then clear state.
	for i := range s.keyPEM {
		s.keyPEM[i] = 0
	}
	s.enrolled = false
	s.cloudHost = ""
	s.orgID = 0
	s.assetID = 0
	s.keyPEM = nil
	s.certPEM = ""
	s.chainPEM = ""

	s.logger.Info("Device unprovisioned; agent will restart into unprovisioned mode")

	cb := s.OnUnprovisioned
	locked = false
	s.mu.Unlock()

	if cb != nil {
		// Invoke asynchronously after a short delay so the RPC response is
		// flushed to the client before the agent restarts. Mirrors the agent
		// update and reboot flows.
		go func() {
			time.Sleep(500 * time.Millisecond)
			cb()
		}()
	}

	return &agentpb.UnprovisionResponse{}, nil
}

// clearStateFiles removes all on-disk provisioning artifacts: the state file,
// the device private key, the mounted PEM files, and the .provisioned marker.
// A missing file is not treated as an error.
func (s *ProvisioningService) clearStateFiles() error {
	files := []string{
		s.statePath(),
		filepath.Join(s.configPath, "device-key.pem"),
		filepath.Join(s.configPath, "device.pem"),
		filepath.Join(s.configPath, "ca.pem"),
		filepath.Join(s.configPath, ".provisioned"),
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", f, err)
		}
	}
	return nil
}

func (s *ProvisioningService) statePath() string {
	return filepath.Join(s.configPath, "provisioning.json")
}

// loadState loads provisioning state from disk.
// The private key is always read from device-key.pem (mode 0o400). If that
// file is absent but the legacy provisioning.json contains a KeyPEM entry, the
// key is migrated to device-key.pem and removed from provisioning.json so that
// subsequent reads use the dedicated file.
func (s *ProvisioningService) loadState() {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}

	var state provisioningState
	if err := json.Unmarshal(data, &state); err != nil {
		s.logger.Warn("Failed to parse provisioning state", zap.Error(err))
		return
	}

	s.enrolled = state.Enrolled
	s.cloudHost = state.CloudHost
	s.orgID = state.OrgID
	s.assetID = state.AssetID
	s.certPEM = state.CertPEM
	s.chainPEM = state.ChainPEM

	// Load the private key from device-key.pem.  Fall back to the legacy
	// KeyPEM field in provisioning.json for one-time migration of existing
	// devices, then immediately rewrite provisioning.json without the key.
	if s.enrolled {
		keyPath := filepath.Join(s.configPath, "device-key.pem")
		if keyData, readErr := os.ReadFile(keyPath); readErr == nil && len(keyData) > 0 {
			s.keyPEM = keyData
		} else if state.KeyPEM != "" {
			s.keyPEM = []byte(state.KeyPEM)
			if writeErr := os.WriteFile(keyPath, s.keyPEM, 0o400); writeErr == nil {
				s.logger.Info("Migrated device key from provisioning.json to device-key.pem")
				// Rewrite provisioning.json without the now-migrated key.
				toSave := state
				toSave.KeyPEM = ""
				if saveData, marshalErr := json.MarshalIndent(toSave, "", "  "); marshalErr == nil {
					_ = os.WriteFile(s.statePath(), saveData, 0o600)
				}
			} else {
				s.logger.Warn("Failed to migrate device key to device-key.pem", zap.Error(writeErr))
			}
		}
	}

	// Ensure PEM files exist on disk (may have been lost during OTA update).
	if s.enrolled && len(s.keyPEM) > 0 && s.certPEM != "" {
		if err := s.writePEMFiles(string(s.keyPEM), s.certPEM, s.chainPEM); err != nil {
			s.logger.Warn("Failed to restore PEM files from provisioning state", zap.Error(err))
		}
	}
}

// loadOrGenerateKey returns the PEM-encoded private key for this device as []byte.
// It reuses the key at {configPath}/device-key.pem if it exists, otherwise
// generates a new one and persists it.
func (s *ProvisioningService) loadOrGenerateKey() ([]byte, error) {
	keyPath := filepath.Join(s.configPath, "device-key.pem")
	if data, err := os.ReadFile(keyPath); err == nil && len(data) > 0 {
		s.logger.Info("Reusing existing device key", zap.String("path", keyPath))
		return data, nil
	}

	keyStr, err := certs.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	keyPEM := []byte(keyStr)

	// Persist the key so it's reused on future provisioning.
	// 0o400: private key must be read-only after creation.
	if err := os.MkdirAll(s.configPath, 0o700); err == nil {
		_ = os.WriteFile(keyPath, keyPEM, 0o400)
	}

	return keyPEM, nil
}

func (s *ProvisioningService) writePEMFiles(keyPEM, certPEM, chainPEM string) error {
	return WritePEMFiles(s.configPath, keyPEM, certPEM, chainPEM)
}

// saveState writes provisioning state to disk.
// The private key (KeyPEM) is never included in provisioning.json; it lives
// exclusively in device-key.pem so that the JSON file can be shared or
// inspected without exposing key material.
func (s *ProvisioningService) saveState(state *provisioningState) error {
	if err := os.MkdirAll(s.configPath, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Shallow copy to ensure we never accidentally serialise a key.
	toSave := *state
	toSave.KeyPEM = ""

	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	return os.WriteFile(s.statePath(), data, 0o600)
}
