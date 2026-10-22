package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/netmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
)

const meshWorkerEnv = "WENDY_INTERNAL_MESH_SESSION"
const meshEndpointEnv = "WENDY_INTERNAL_MESH_ENDPOINT"

var meshStartMu sync.Mutex

// selectedMeshCertificate is deliberately non-interactive and never enrolls or
// signs in. Only the selected organization's existing operator session can join.
func selectedMeshCertificate(cfg *config.Config, endpoint string) (config.CertificateInfo, bool) {
	auth, err := config.ResolveAuth(cfg, endpoint, nil)
	if err != nil {
		return config.CertificateInfo{}, false
	}
	for _, certificate := range auth.Certificates {
		peer, err := meshid.PeerFromPrincipal(certificate.PrincipalURI)
		if err == nil && peer.IsOperator() && certificate.PemCertificate != "" && certificate.PemPrivateKey != "" && certificate.PemCertificateChain != "" {
			return certificate, true
		}
	}
	return config.CertificateInfo{}, false
}
func meshFingerprint(c config.CertificateInfo) string {
	sum := sha256.Sum256([]byte(c.PrincipalURI + "\x00" + c.PemCertificate + "\x00" + c.PemCertificateChain))
	return hex.EncodeToString(sum[:])
}
func meshDirectory(c config.CertificateInfo) (string, error) {
	base, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	principal := sha256.Sum256([]byte(c.PrincipalURI))
	return filepath.Join(base, "mesh", hex.EncodeToString(principal[:12])), nil
}
func meshSelection(endpoint string) (config.CertificateInfo, bool) {
	cfg, err := config.Load()
	if err != nil {
		return config.CertificateInfo{}, false
	}
	return selectedMeshCertificate(cfg, endpoint)
}

// Refresh only the selected operator session before starting discovery. The
// worker's repeated selection checks remain read-only; the caller owns renewal.
func prepareMeshCertificate(ctx context.Context, endpoint string) (config.CertificateInfo, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.CertificateInfo{}, false, err
	}
	if _, ok := selectedMeshCertificate(cfg, endpoint); !ok {
		return config.CertificateInfo{}, false, nil
	}
	auth, err := config.ResolveAuth(cfg, endpoint, nil)
	if err != nil {
		return config.CertificateInfo{}, false, err
	}
	// This serializes renewal with other auth writers, reloads the session,
	// and persists rotated OAuth tokens even when certificate issuance fails.
	if err := prepareDefaultCloudCertificate(ctx, auth); err != nil {
		return config.CertificateInfo{}, false, fmt.Errorf("refreshing Wendy Net operator certificate: %w; run 'wendy auth refresh-certs' or sign in again with 'wendy auth login'", err)
	}
	certificate, ok := selectedMeshCertificate(cfg, endpoint)
	return certificate, ok, nil
}

// RunMeshWorker handles an internal re-exec, before command construction. There
// is no user command or separately enrolled desktop identity. The worker reads
// credentials from the same protected configuration as the calling CLI.
func RunMeshWorker() bool {
	fingerprint := os.Getenv(meshWorkerEnv)
	if fingerprint == "" || len(os.Args) != 1 {
		return false
	}
	endpoint := os.Getenv(meshEndpointEnv)
	certificate, ok := meshSelection(endpoint)
	if !ok || meshFingerprint(certificate) != fingerprint {
		return true
	}
	credentials, err := localmesh.NewOperatorCredentials(certificate.PemCertificate, certificate.PemCertificateChain, certificate.PemPrivateKey)
	if err != nil {
		return true
	}
	if credentials.Device.Principal() != certificate.PrincipalURI {
		return true
	}
	dir, err := meshDirectory(certificate)
	if err != nil {
		return true
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Bound the session to the earliest expiry in the verified chain.
	identity, err := credentials.Verify(credentials.Certificate.Certificate, time.Now())
	if err != nil {
		return true
	}
	valid := func() bool {
		current, ok := meshSelection(endpoint)
		return ok && meshFingerprint(current) == fingerprint && time.Now().Before(identity.NotAfter)
	}
	_ = netmesh.Serve(ctx, dir, func(ctx context.Context) (*netmesh.Client, error) { return netmesh.New(ctx, dir, credentials) }, valid)
	return true
}
func ensureMeshSession(ctx context.Context) (string, config.CertificateInfo, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", config.CertificateInfo{}, nil
	}
	certificate, ok, err := prepareMeshCertificate(ctx, "")
	if err != nil {
		return "", certificate, err
	}
	if !ok {
		return "", certificate, nil
	}
	// Validate before any radio or worker starts, including certificate principal,
	// issuer lifetime, key possession, and organization scope.
	credentials, err := localmesh.NewOperatorCredentials(certificate.PemCertificate, certificate.PemCertificateChain, certificate.PemPrivateKey)
	if err != nil {
		return "", certificate, err
	}
	if credentials.Device.Principal() != certificate.PrincipalURI {
		return "", certificate, errors.New("operator certificate does not match selected organization identity")
	}
	dir, err := meshDirectory(certificate)
	if err != nil {
		return "", certificate, err
	}
	meshStartMu.Lock()
	defer meshStartMu.Unlock()
	sum := sha256.Sum256(credentials.Certificate.Certificate[0])
	identity := hex.EncodeToString(sum[:])
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	status, err := netmesh.ReadStatus(probe, dir)
	cancel()
	if err == nil && status.Identity == identity {
		return dir, certificate, nil
	}
	if err = netmesh.PrivateDirectory(dir); err != nil {
		return "", certificate, err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", certificate, err
	}
	worker := exec.Command(exe)
	worker.Env = append(os.Environ(), meshWorkerEnv+"="+meshFingerprint(certificate), meshEndpointEnv+"=")
	detachMeshWorker(worker)
	if err = worker.Start(); err != nil {
		return "", certificate, err
	}
	go func() { _ = worker.Wait() }()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return "", certificate, fmt.Errorf("starting organization mesh: %w", waitCtx.Err())
		case <-ticker.C:
			probe, cancel := context.WithTimeout(waitCtx, 300*time.Millisecond)
			status, err := netmesh.ReadStatus(probe, dir)
			cancel()
			if err == nil && status.Identity == identity {
				return dir, certificate, nil
			}
		}
	}
}
func meshDeviceRow(d netmesh.Device) models.LANDevice {
	return models.LANDevice{ID: d.ID.String(), DisplayName: discovery.SanitiseDisplayName(d.Name), Hostname: "mesh:" + d.ID.String(), IPAddress: d.Address.String(), Port: int(d.Port), IsMTLS: true, IsWendyDevice: true, Mesh: true}
}

func meshDiscoveryStream(ctx context.Context) <-chan discovery.LANEvent {
	out := make(chan discovery.LANEvent)
	go func() {
		defer close(out)
		dir, _, err := ensureMeshSession(ctx)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Wendy Net: %v\n", err)
			}
			return
		}
		if dir == "" {
			return
		}
		seen := map[string]models.LANDevice{}
		send := func(event discovery.LANEvent) bool {
			select {
			case <-ctx.Done():
				return false
			case out <- event:
				return true
			}
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			status, err := netmesh.ReadStatus(ctx, dir)
			if err != nil {
				for _, row := range seen {
					if !send(discovery.LANEvent{Kind: discovery.LANRetracted, Device: row}) {
						return
					}
				}
				return
			}
			next := map[string]models.LANDevice{}
			for _, d := range status.Devices {
				row := meshDeviceRow(d)
				next[row.ID] = row
				if old, ok := seen[row.ID]; !ok || old.DisplayName != row.DisplayName || old.Port != row.Port {
					if !send(discovery.LANEvent{Kind: discovery.LANFound, Device: row}) {
						return
					}
				}
			}
			for id, row := range seen {
				if _, ok := next[id]; !ok {
					if !send(discovery.LANEvent{Kind: discovery.LANRetracted, Device: row}) {
						return
					}
				}
			}
			seen = next
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}
func connectMeshSelector(ctx context.Context, selector string) (*SelectedDevice, error) {
	name := strings.TrimPrefix(selector, "mesh:")
	if name == "" || strings.ContainsAny(name, "/\\\r\n\t") {
		return nil, errors.New("invalid mesh device selector")
	}
	dir, certificate, err := ensureMeshSession(ctx)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errors.New("mesh access requires a selected organization and operator certificate")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var device netmesh.Device
	for {
		status, err := netmesh.ReadStatus(waitCtx, dir)
		if err != nil {
			return nil, err
		}
		device, err = netmesh.Resolve(status.Devices, name)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "ambiguous") {
			return nil, err
		}
		select {
		case <-waitCtx.Done():
			return nil, err
		case <-time.After(300 * time.Millisecond):
		}
	}
	expected, err := certs.ParseDevicePrincipal(certs.DeviceSPIFFEURI(certificate.TenantUUID(), device.ID.String()))
	if err != nil {
		return nil, err
	}
	address := netip.AddrPortFrom(device.Address, device.Port).String()
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		return netmesh.Dial(ctx, dir, device.ID.String(), device.Port)
	}
	connection, err := grpcclient.ConnectWithTLSExpecting(waitCtx, address, &certificate, openPinStore(), &expected, grpc.WithContextDialer(dial))
	if err != nil {
		return nil, err
	}
	version, err := connection.AgentService.GetAgentVersion(waitCtx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		connection.Close()
		return nil, err
	}
	connection.CacheAgentVersion(version)
	connection.RegistryDialer = func(ctx context.Context, port int) (net.Conn, error) {
		if port <= 0 || port > 65535 {
			return nil, errors.New("invalid registry port")
		}
		return netmesh.Dial(ctx, dir, device.ID.String(), uint16(port))
	}
	connection.Reconnect = func(ctx context.Context) (*grpcclient.AgentConnection, error) {
		picked, err := connectMeshSelector(ctx, "mesh:"+device.ID.String())
		if err != nil {
			return nil, err
		}
		return picked.Agent, nil
	}
	picked := &SelectedDevice{Agent: connection, DefaultSelector: "mesh:" + device.ID.String(), PinKey: "mesh:" + certificate.TenantUUID() + ":" + name}
	if err := enforceSelectedDevicePin(picked); err != nil {
		return nil, err
	}
	return picked, nil
}

// A bare name selected from discovery can reuse its authenticated mesh roster.
// Unknown names retain the existing LAN/cloud resolution behavior.
func connectDiscoveredMeshName(ctx context.Context, name string) (*SelectedDevice, bool, error) {
	if name == "" || strings.ContainsAny(name, ".:/\\") {
		return nil, false, nil
	}
	certificate, ok := meshSelection("")
	if !ok {
		return nil, false, nil
	}
	dir, err := meshDirectory(certificate)
	if err != nil {
		return nil, false, nil
	}
	probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	status, err := netmesh.ReadStatus(probe, dir)
	if err != nil {
		return nil, false, nil
	}
	matches := 0
	for _, device := range status.Devices {
		if device.Name == name || device.ID.String() == name {
			matches++
		}
	}
	if matches == 0 {
		return nil, false, nil
	}
	if matches > 1 {
		return nil, true, fmt.Errorf("ambiguous mesh device %q; use its UUID", name)
	}
	picked, err := connectMeshSelector(ctx, "mesh:"+name)
	return picked, true, err
}
