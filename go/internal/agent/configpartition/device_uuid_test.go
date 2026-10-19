package configpartition

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func certWithURI(t *testing.T, uri string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), URIs: []*url.URL{u}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestPersistDeviceUUIDWritesTheCertUUIDAndMDNSID(t *testing.T) {
	const device = "019535d9-3df7-79fb-b466-fa907fa17f9e"
	dir := t.TempDir()
	uuidPath := filepath.Join(dir, "etc", "wendyos", "device-uuid")
	serviceDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(serviceDir, "wendy-agent.service")
	if err := os.WriteFile(service, []byte("<service><type>_wendyos._udp</type><txt-record>id=first-boot-uuid</txt-record><txt-record>name=box</txt-record></service>"), 0o644); err != nil {
		t.Fatal(err)
	}

	cert := certWithURI(t, "spiffe://wendy.sh/tenant/2558fd76-afc7-466e-9613-6b715296a526/device/"+device)
	if err := persistDeviceUUID(dir, serviceDir, cert); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(uuidPath); strings.TrimSpace(string(got)) != device {
		t.Fatalf("device-uuid = %q, want %s", got, device)
	}
	got, _ := os.ReadFile(service)
	if !strings.Contains(string(got), "<txt-record>id="+device+"</txt-record>") || !strings.Contains(string(got), "name=box") {
		t.Fatalf("mDNS id= not the cert UUID: %s", got)
	}

	// An unnamed device kept the hostname it answered by before the UUID changed.
	hostname, _ := os.Hostname()
	if got, _ := os.ReadFile(filepath.Join(dir, "etc", "wendy-agent", "hostname")); strings.TrimSpace(string(got)) != hostname {
		t.Fatalf("hostname not pinned before the UUID changed: %q", got)
	}

	// A legacy asset certificate has no device UUID: nothing is rewritten.
	legacyRoot := t.TempDir()
	if err := persistDeviceUUID(legacyRoot, serviceDir, certWithURI(t, "urn:wendy:org:7:asset:42")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, "etc")); !os.IsNotExist(err) {
		t.Fatalf("legacy certificate wrote identity files: %v", err)
	}
}

func TestPersistDeviceUUIDLeavesANamedDevicesHostnameAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etc", "wendyos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "wendyos", "device-name"), []byte("box-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cert := certWithURI(t, "spiffe://wendy.sh/tenant/2558fd76-afc7-466e-9613-6b715296a526/device/019535d9-3df7-79fb-b466-fa907fa17f9e")
	if err := persistDeviceUUID(dir, t.TempDir(), cert); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "wendy-agent", "hostname")); !os.IsNotExist(err) {
		t.Fatalf("named device got an explicit hostname: %v", err)
	}
}
