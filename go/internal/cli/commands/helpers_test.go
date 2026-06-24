package commands

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// ── hostPort ────────────────────────────────────────────────────────

func TestHostPort(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		// IPv4
		{"IPv4", "192.168.1.5", 50051, "192.168.1.5:50051"},
		{"IPv4 loopback", "127.0.0.1", 50051, "127.0.0.1:50051"},
		{"IPv4 alt port", "10.0.0.1", 8080, "10.0.0.1:8080"},

		// IPv6 global — must be bracketed
		{"IPv6 global", "2001:db8::1", 50051, "[2001:db8::1]:50051"},
		{"IPv6 loopback", "::1", 50051, "[::1]:50051"},
		{"IPv6 full", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", 50051, "[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:50051"},

		// IPv6 link-local with zone ID — must be bracketed
		{"IPv6 zone en0", "fe80::3ee2:fcc9:fe8e:f69c%en0", 50051, "[fe80::3ee2:fcc9:fe8e:f69c%en0]:50051"},
		{"IPv6 zone en24 (USB)", "fe80::8c13:12bf:4df8:b976%en24", 50051, "[fe80::8c13:12bf:4df8:b976%en24]:50051"},
		{"IPv6 zone eth0 (Linux)", "fe80::1%eth0", 50051, "[fe80::1%eth0]:50051"},
		{"IPv6 zone numeric", "fe80::1%5", 50051, "[fe80::1%5]:50051"},
		{"IPv6 zone mTLS port", "fe80::1%en0", 50052, "[fe80::1%en0]:50052"},

		// Hostnames — no brackets
		{"mDNS hostname", "wendyos-otter.local", 50051, "wendyos-otter.local:50051"},
		{"plain hostname", "my-device", 50051, "my-device:50051"},
		{"FQDN", "device.example.com", 50051, "device.example.com:50051"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostPort(tt.host, tt.port)
			if got != tt.want {
				t.Fatalf("hostPort(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
			}
		})
	}
}

func TestResolveAgentPlatform(t *testing.T) {
	tests := []struct {
		name        string
		cfgPlatform string
		agentOS     string
		agentArch   string
		want        string
	}{
		{
			name:        "full platform is used as-is",
			cfgPlatform: "linux/amd64",
			agentOS:     "darwin",
			agentArch:   "arm64",
			want:        "linux/amd64",
		},
		{
			name:        "full wendyos platform is normalized to linux",
			cfgPlatform: "wendyos/arm64",
			agentOS:     "darwin",
			agentArch:   "amd64",
			want:        "linux/arm64",
		},
		{
			name:        "OS-only platform uses agent architecture",
			cfgPlatform: "darwin",
			agentOS:     "linux",
			agentArch:   "arm64",
			want:        "darwin/arm64",
		},
		{
			name:        "OS-only wendyos platform is normalized to linux",
			cfgPlatform: "wendyos",
			agentOS:     "darwin",
			agentArch:   "arm64",
			want:        "linux/arm64",
		},
		{
			name:      "empty platform defaults to linux on Linux agent",
			agentOS:   "linux",
			agentArch: "arm64",
			want:      "linux/arm64",
		},
		{
			name:      "empty platform defaults to linux on Darwin agent",
			agentOS:   "darwin",
			agentArch: "arm64",
			want:      "linux/arm64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAgentPlatform(tt.cfgPlatform, tt.agentOS, tt.agentArch)
			if got != tt.want {
				t.Fatalf("resolveAgentPlatform(%q, %q, %q) = %q, want %q", tt.cfgPlatform, tt.agentOS, tt.agentArch, got, tt.want)
			}
		})
	}
}

func TestLANAgentAddressesPrefersIPAddress(t *testing.T) {
	dev := models.LANDevice{
		IPAddress: "192.168.1.23",
		Hostname:  "wendyos-otter.local",
		Port:      defaultAgentPort,
	}

	got := lanAgentAddresses(dev)
	want := []string{
		"192.168.1.23:50051",
		"wendyos-otter.local:50051",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAgentAddresses() = %v, want %v", got, want)
	}
}

func TestExternalProviderPickerHint(t *testing.T) {
	tests := []struct {
		name        string
		providerKey string
		want        string
	}{
		{
			name:        "docker",
			providerKey: providers.ProviderKeyDocker,
			want:        "Docker",
		},
		{
			name:        "local",
			providerKey: providers.ProviderKeyLocal,
			want:        providers.LocalDisplayName(),
		},
		{
			name:        "other",
			providerKey: "wendy-lite",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := externalProviderPickerHint(tt.providerKey)
			if tt.want == "" {
				if got != "" {
					t.Fatalf("hint = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("hint = %q, want it to mention %q", got, tt.want)
			}
			for _, stale := range []string{"Docker Desktop", "Local Machine"} {
				if strings.Contains(got, stale) {
					t.Fatalf("hint = %q, want long label %q replaced", got, stale)
				}
			}
		})
	}
}

func TestProvisionedAgentAdvertisedMTLSMatchesDiscoveredMTLSDevice(t *testing.T) {
	stubDiscoverLANDevices(t, []models.LANDevice{
		{
			IPAddress: "127.0.0.1",
			Port:      defaultAgentPort + agentMTLSPortOffset,
			IsMTLS:    true,
		},
	}, nil)

	if !provisionedAgentAdvertisedMTLS(context.Background(), "127.0.0.1:50051") {
		t.Fatal("provisionedAgentAdvertisedMTLS() = false, want true")
	}
}

func TestProvisionedAgentAdvertisedMTLSIgnoresPlaintextDevices(t *testing.T) {
	stubDiscoverLANDevices(t, []models.LANDevice{
		{
			IPAddress: "127.0.0.1",
			Port:      defaultAgentPort,
			IsMTLS:    false,
		},
	}, nil)

	if provisionedAgentAdvertisedMTLS(context.Background(), "127.0.0.1:50051") {
		t.Fatal("provisionedAgentAdvertisedMTLS() = true, want false")
	}
}

func TestProvisionedAgentAdvertisedMTLSMatchesHostnameCaseInsensitively(t *testing.T) {
	stubDiscoverLANDevices(t, []models.LANDevice{
		{
			Hostname: "WENDYOS-OTTER.LOCAL.",
			Port:     defaultAgentPort + agentMTLSPortOffset,
			IsMTLS:   true,
		},
	}, nil)

	if !provisionedAgentAdvertisedMTLS(context.Background(), "wendyos-otter.local:50051") {
		t.Fatal("provisionedAgentAdvertisedMTLS() = false, want true")
	}
}

func TestLANAgentAddressesDeduplicatesIdenticalHosts(t *testing.T) {
	dev := models.LANDevice{
		IPAddress: "192.168.1.23",
		Hostname:  "192.168.1.23",
		Port:      defaultAgentPort,
	}

	got := lanAgentAddresses(dev)
	want := []string{"192.168.1.23:50051"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAgentAddresses() = %v, want %v", got, want)
	}
}

func TestLANAgentAddresses_IPv6LinkLocal(t *testing.T) {
	dev := models.LANDevice{
		IPAddress: "fe80::8c13:12bf:4df8:b976%en24",
		Hostname:  "wendyos-otter.local",
		Port:      defaultAgentPort,
	}

	got := lanAgentAddresses(dev)
	want := []string{
		"[fe80::8c13:12bf:4df8:b976%en24]:50051",
		"wendyos-otter.local:50051",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAgentAddresses() = %v, want %v", got, want)
	}
}

func TestLANAgentAddresses_IPv6OnlyNoHostname(t *testing.T) {
	dev := models.LANDevice{
		IPAddress: "fe80::1%en0",
		Port:      defaultAgentPort,
	}

	got := lanAgentAddresses(dev)
	want := []string{"[fe80::1%en0]:50051"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAgentAddresses() = %v, want %v", got, want)
	}
}

func TestLANAgentAddressesFallsBackToDefaultPort(t *testing.T) {
	dev := models.LANDevice{
		IPAddress: "192.168.1.23",
		Hostname:  "wendyos-otter.local",
	}

	got := lanAgentAddresses(dev)
	want := []string{
		"192.168.1.23:50051",
		"wendyos-otter.local:50051",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lanAgentAddresses() = %v, want %v", got, want)
	}
}

func TestIsCertRejectionErrorIgnoresPlaintextTLSProbe(t *testing.T) {
	err := errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: tls: first record does not look like a TLS handshake"`)

	if isCertRejectionError(err) {
		t.Fatal("isCertRejectionError() = true, want false for plaintext TLS probe")
	}
}

func TestIsCertRejectionErrorDetectsTLSAlert(t *testing.T) {
	err := errors.New("rpc error: code = Unavailable desc = remote error: tls: bad certificate")

	if !isCertRejectionError(err) {
		t.Fatal("isCertRejectionError() = false, want true for TLS alert")
	}
}

func TestResolveLANAgentVersionFallsBackAcrossAddresses(t *testing.T) {
	orig := getAgentVersionAtAddress
	defer func() { getAgentVersionAtAddress = orig }()

	var (
		mu    sync.Mutex
		calls []string
	)
	getAgentVersionAtAddress = func(_ context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error) {
		mu.Lock()
		calls = append(calls, address)
		mu.Unlock()

		if address == "192.168.1.23:50051" {
			return false, nil, errors.New("dial tcp 192.168.1.23:50051: i/o timeout")
		}
		return false, &agentpb.GetAgentVersionResponse{Version: "1.2.3"}, nil
	}

	dev := models.LANDevice{
		IPAddress: "192.168.1.23",
		Hostname:  "wendyos-otter.local",
		Port:      defaultAgentPort,
	}

	address, _, resp, err := resolveLANAgentVersion(context.Background(), dev)
	if err != nil {
		t.Fatalf("resolveLANAgentVersion() error = %v", err)
	}

	if address != "wendyos-otter.local:50051" {
		t.Fatalf("resolveLANAgentVersion() address = %q, want %q", address, "wendyos-otter.local:50051")
	}
	if resp.GetVersion() != "1.2.3" {
		t.Fatalf("resolveLANAgentVersion() version = %q, want %q", resp.GetVersion(), "1.2.3")
	}

	wantCalls := []string{
		"192.168.1.23:50051",
		"wendyos-otter.local:50051",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("resolveLANAgentVersion() calls = %v, want %v", calls, wantCalls)
	}
}

// setTempConfig points HOME at a temp dir and writes cfg via config.Save so
// the test uses the same serialisation path as production code. t.Setenv
// automatically restores the original HOME when the test finishes.
func setTempConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// config.Save calls ConfigDir() which creates ~/.wendy and writes config.json.
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDeviceAddress_Flag(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = "my-device.local"

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDefault {
		t.Fatal("expected isDefault=false when --device flag is set")
	}
	if addr != "my-device.local:50051" {
		t.Fatalf("addr = %q, want %q", addr, "my-device.local:50051")
	}
}

func TestResolveDeviceAddress_DefaultDevice(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = ""

	setTempConfig(t, &config.Config{DefaultDevice: "wendy-thor.local"})

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDefault {
		t.Fatal("expected isDefault=true when using default device from config")
	}
	if addr != "wendy-thor.local:50051" {
		t.Fatalf("addr = %q, want %q", addr, "wendy-thor.local:50051")
	}
}

func TestResolveDeviceAddress_ExplicitHostPortFlag(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = "my-mac.local:50051"

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDefault {
		t.Fatal("expected isDefault=false when --device flag is set")
	}
	if addr != "my-mac.local:50051" {
		t.Fatalf("addr = %q, want %q", addr, "my-mac.local:50051")
	}
}

func TestResolveDeviceAddress_ExplicitHostPortDefault(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = ""

	setTempConfig(t, &config.Config{DefaultDevice: "my-mac.local:50051"})

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDefault {
		t.Fatal("expected isDefault=true when using default device from config")
	}
	if addr != "my-mac.local:50051" {
		t.Fatalf("addr = %q, want %q", addr, "my-mac.local:50051")
	}
}

func TestResolveDeviceAddress_IPv6ZoneFlag(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = "fe80::8c13:12bf:4df8:b976%en24"

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDefault {
		t.Fatal("expected isDefault=false when --device flag is set")
	}
	if addr != "[fe80::8c13:12bf:4df8:b976%en24]:50051" {
		t.Fatalf("addr = %q, want %q", addr, "[fe80::8c13:12bf:4df8:b976%en24]:50051")
	}
}

func TestResolveDeviceAddress_IPv6DefaultDevice(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = ""

	setTempConfig(t, &config.Config{DefaultDevice: "fe80::1%en0"})

	addr, isDefault, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDefault {
		t.Fatal("expected isDefault=true when using default device from config")
	}
	if addr != "[fe80::1%en0]:50051" {
		t.Fatalf("addr = %q, want %q", addr, "[fe80::1%en0]:50051")
	}
}

func TestResolveDeviceAddress_IPv6GlobalFlag(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = "2001:db8::1"

	addr, _, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "[2001:db8::1]:50051" {
		t.Fatalf("addr = %q, want %q", addr, "[2001:db8::1]:50051")
	}
}

func TestResolveDeviceAddress_IPv4Flag(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = "192.168.1.42"

	addr, _, err := resolveDeviceAddress()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "192.168.1.42:50051" {
		t.Fatalf("addr = %q, want %q", addr, "192.168.1.42:50051")
	}
}

func TestResolveDeviceAddress_NoDevice(t *testing.T) {
	origFlag := deviceFlag
	defer func() { deviceFlag = origFlag }()
	deviceFlag = ""

	setTempConfig(t, &config.Config{})

	_, _, err := resolveDeviceAddress()
	if err == nil {
		t.Fatal("expected error when no device is specified")
	}
}

func TestResolveLANVersionsKeepsDevicesWhenMetadataLookupFails(t *testing.T) {
	orig := getAgentVersionAtAddress
	defer func() { getAgentVersionAtAddress = orig }()

	getAgentVersionAtAddress = func(_ context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error) {
		return false, nil, errors.New("unreachable: " + address)
	}

	devices := []models.LANDevice{
		{
			DisplayName: "Wendy One",
			Hostname:    "wendy-one.local",
			IPAddress:   "192.168.1.10",
			Port:        defaultAgentPort,
		},
		{
			DisplayName: "Wendy Two",
			Hostname:    "wendy-two.local",
			IPAddress:   "192.168.1.11",
			Port:        defaultAgentPort,
		},
	}

	expected := make([]models.LANDevice, len(devices))
	copy(expected, devices)

	got := resolveLANVersions(context.Background(), devices)

	if len(got) != len(expected) {
		t.Fatalf("resolveLANVersions() returned %d devices, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i].DisplayName != expected[i].DisplayName {
			t.Fatalf("resolveLANVersions()[%d].DisplayName = %q, want %q", i, got[i].DisplayName, expected[i].DisplayName)
		}
		if got[i].IPAddress != expected[i].IPAddress {
			t.Fatalf("resolveLANVersions()[%d].IPAddress = %q, want %q", i, got[i].IPAddress, expected[i].IPAddress)
		}
		if got[i].AgentVersion != "" {
			t.Fatalf("resolveLANVersions()[%d].AgentVersion = %q, want empty", i, got[i].AgentVersion)
		}
	}
}

func TestDefaultDeviceSearchLabel(t *testing.T) {
	got := defaultDeviceSearchLabel("wendyos-daring-razorbill.local")
	want := `Searching for default device "wendyos-daring-razorbill.local"...`
	if got != want {
		t.Fatalf("defaultDeviceSearchLabel() = %q, want %q", got, want)
	}
}

func TestFormatElapsedSeconds(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "fractional seconds", elapsed: 3420 * time.Millisecond, want: "3.42 seconds"},
		{name: "rounding", elapsed: 3449 * time.Millisecond, want: "3.45 seconds"},
		{name: "singular", elapsed: time.Second, want: "1.00 second"},
		{name: "rounds to singular", elapsed: 1004 * time.Millisecond, want: "1.00 second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatElapsedSeconds(tt.elapsed); got != tt.want {
				t.Fatalf("formatElapsedSeconds(%v) = %q, want %q", tt.elapsed, got, tt.want)
			}
		})
	}
}

func TestConnectResolvedAgent_UsesSpinnerForInteractiveDefaultDevice(t *testing.T) {
	origInteractive := isInteractiveTerminalFn
	origSpinner := runAgentConnectionSpinner
	origJSON := jsonOutput
	defer func() {
		isInteractiveTerminalFn = origInteractive
		runAgentConnectionSpinner = origSpinner
		jsonOutput = origJSON
	}()

	isInteractiveTerminalFn = func() bool { return true }
	jsonOutput = false

	wantConn := &grpcclient.AgentConnection{Host: "wendyos-daring-razorbill.local"}
	var (
		gotLabel       string
		spinnerInvoked bool
	)
	runAgentConnectionSpinner = func(_ context.Context, label string, _ func(context.Context) (*grpcclient.AgentConnection, error)) (*grpcclient.AgentConnection, error) {
		spinnerInvoked = true
		gotLabel = label
		return wantConn, nil
	}

	gotConn, err := connectResolvedAgent(
		context.Background(),
		"wendyos-daring-razorbill.local",
		hostPort("wendyos-daring-razorbill.local", defaultAgentPort),
		true,
	)
	if err != nil {
		t.Fatalf("connectResolvedAgent() error = %v", err)
	}
	if !spinnerInvoked {
		t.Fatal("expected interactive default-device connection to use spinner")
	}
	if gotLabel != `Searching for default device "wendyos-daring-razorbill.local"...` {
		t.Fatalf("spinner label = %q, want %q", gotLabel, `Searching for default device "wendyos-daring-razorbill.local"...`)
	}
	if gotConn != wantConn {
		t.Fatal("connectResolvedAgent() did not return spinner result")
	}
}

func TestConnectResolvedAgent_NoAuthProvisionedAgentRequiresLogin(t *testing.T) {
	origInteractive := isInteractiveTerminalFn
	origJSON := jsonOutput
	defer func() {
		isInteractiveTerminalFn = origInteractive
		jsonOutput = origJSON
	}()

	isInteractiveTerminalFn = func() bool { return false }
	jsonOutput = false
	setTempConfig(t, &config.Config{})

	plaintextAddr := startFailingPlaintextAgent(t)
	knownProvisionedMTLS := stubProvisionedMTLSDiscovery(t, plaintextAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := connectResolvedAgentWithProvisionedHint(ctx, "127.0.0.1", plaintextAddr, false, func() bool { return knownProvisionedMTLS })
	if conn != nil {
		conn.Close()
		t.Fatal("connectResolvedAgent() returned a connection for an auth-only agent")
	}
	if !errors.Is(err, errProvisionedAgentUnauthorized) {
		t.Fatalf("connectResolvedAgent() error = %v, want %v", err, errProvisionedAgentUnauthorized)
	}
	if err.Error() != provisionedAgentUnauthorizedMessage {
		t.Fatalf("connectResolvedAgent() message = %q, want %q", err.Error(), provisionedAgentUnauthorizedMessage)
	}
}

func TestConnectResolvedAgent_ProvisionedAgentPreservesMTLSError(t *testing.T) {
	origInteractive := isInteractiveTerminalFn
	origJSON := jsonOutput
	defer func() {
		isInteractiveTerminalFn = origInteractive
		jsonOutput = origJSON
	}()

	isInteractiveTerminalFn = func() bool { return false }
	jsonOutput = false
	setTempConfig(t, &config.Config{
		Auth: []config.AuthConfig{
			{
				Certificates: []config.CertificateInfo{
					{
						PemCertificate: "not a certificate",
						PemPrivateKey:  "not a private key",
					},
				},
			},
		},
	})

	plaintextAddr := startFailingPlaintextAgent(t)
	knownProvisionedMTLS := stubProvisionedMTLSDiscovery(t, plaintextAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := connectResolvedAgentWithProvisionedHint(ctx, "127.0.0.1", plaintextAddr, false, func() bool { return knownProvisionedMTLS })
	if conn != nil {
		conn.Close()
		t.Fatal("connectResolvedAgent() returned a connection for an auth-only agent")
	}
	if !errors.Is(err, errProvisionedAgentUnauthorized) {
		t.Fatalf("connectResolvedAgent() error = %v, want %v", err, errProvisionedAgentUnauthorized)
	}
	if errors.Unwrap(err) == nil {
		t.Fatalf("connectResolvedAgent() error = %v, want wrapped mTLS cause", err)
	}
	if !strings.Contains(err.Error(), "Last mTLS error:") || !strings.Contains(err.Error(), "loading TLS cert") {
		t.Fatalf("connectResolvedAgent() message = %q, want mTLS diagnostic", err.Error())
	}
}

func stubProvisionedMTLSDiscovery(t *testing.T, plaintextAddr string) bool {
	t.Helper()
	host, portStr, err := net.SplitHostPort(plaintextAddr)
	if err != nil {
		t.Fatalf("split plaintext address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse plaintext port: %v", err)
	}
	stubDiscoverLANDevices(t, []models.LANDevice{
		{
			IPAddress: host,
			Port:      port + agentMTLSPortOffset,
			IsMTLS:    true,
		},
	}, nil)
	return provisionedAgentAdvertisedMTLS(context.Background(), plaintextAddr)
}

func startFailingPlaintextAgent(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen plaintext candidate: %v", err)
	}
	go closeAcceptedConnections(listener)
	t.Cleanup(func() {
		listener.Close()
	})
	return listener.Addr().String()
}

func closeAcceptedConnections(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}
}

func stubDiscoverLANDevices(t *testing.T, devices []models.LANDevice, err error) {
	t.Helper()

	orig := discoverLANDevices
	discoverLANDevices = func(context.Context, time.Duration) ([]models.LANDevice, error) {
		return devices, err
	}
	t.Cleanup(func() {
		discoverLANDevices = orig
	})
}

func TestProvisionedAgentUnauthorizedMentionsCLIUpgrade(t *testing.T) {
	// A reachability timeout against an mTLS-advertised device should hint at
	// both stale certs and a too-old CLI.
	err := newProvisionedAgentUnauthorizedError(errors.New("dial tcp 192.168.1.50:50051: i/o timeout"))
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "upgrade") || !strings.Contains(msg, "wendy auth refresh-certs") {
		t.Fatalf("message should mention upgrading the CLI and refresh-certs, got: %q", msg)
	}
}

func TestLanAgentAddressesPrefersUSBLinkLocal(t *testing.T) {
	tests := []struct {
		name string
		dev  models.LANDevice
		want []string
	}{
		{
			name: "usb present orders link-local before routed wifi ip",
			dev:  models.LANDevice{Hostname: "playful-reed.local", IPAddress: "192.168.1.50", USB: "en5 (USB Ethernet) 480 Mbps", Port: 50051},
			want: []string{"playful-reed.local:50051", "192.168.1.50:50051"},
		},
		{
			name: "no usb keeps ip-first ordering",
			dev:  models.LANDevice{Hostname: "playful-reed.local", IPAddress: "192.168.1.50", Port: 50051},
			want: []string{"192.168.1.50:50051", "playful-reed.local:50051"},
		},
		{
			name: "usb present but no ip falls back to hostname only",
			dev:  models.LANDevice{Hostname: "playful-reed.local", USB: "en5 (USB Ethernet) 480 Mbps", Port: 50051},
			want: []string{"playful-reed.local:50051"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lanAgentAddresses(tt.dev)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("lanAgentAddresses() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsCertRejectionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// A plaintext (unprovisioned) agent probed with TLS: gRPC wraps the
			// "first record does not look like a TLS handshake" detail inside an
			// "authentication handshake failed" envelope. This is NOT a cert
			// rejection — the server simply isn't a TLS endpoint, so the CLI must
			// fall back to plaintext rather than report a bogus clock/cert error.
			"plaintext server probed with TLS",
			errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: tls: first record does not look like a TLS handshake"`),
			false,
		},
		{
			"server sent TLS alert (bad cert)",
			errors.New("rpc error: code = Unavailable desc = connection error: desc = \"remote error: tls: bad certificate\""),
			true,
		},
		{
			"client certificate required",
			errors.New("rpc error: code = Unavailable desc = connection error: desc = \"remote error: tls: certificate required\""),
			true,
		},
		{
			"plain transport error (connection refused)",
			errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial tcp 127.0.0.1:50052: connect: connection refused"`),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCertRejectionError(tc.err); got != tc.want {
				t.Errorf("isCertRejectionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUpdateCheckTTLCache(t *testing.T) {
	tmp := t.TempDir()
	// Redirect os.UserCacheDir() on both darwin ($HOME/Library/Caches) and
	// linux ($XDG_CACHE_HOME or $HOME/.cache) into the temp dir.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))

	const host = "device.local"

	if updateCheckRecentlyPassed(host) {
		t.Fatal("cold: expected no recent pass before any check")
	}

	markUpdateCheckPassed(host)
	if !updateCheckRecentlyPassed(host) {
		t.Fatal("warm: expected recent pass after marking")
	}

	if updateCheckRecentlyPassed("other.local") {
		t.Fatal("marker must be per-host")
	}

	// Backdate the marker beyond the TTL: it must no longer count as recent.
	path := updateCheckMarkerPath(host)
	if path == "" {
		t.Fatal("expected a non-empty marker path")
	}
	old := time.Now().Add(-updateCheckTTL - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if updateCheckRecentlyPassed(host) {
		t.Fatal("stale: expected marker older than TTL to fail the check")
	}
}
