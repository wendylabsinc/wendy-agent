package netmesh

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
)

func fixtureCredentials(t *testing.T) []*localmesh.Credentials {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(der)
	chain := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	principals := []string{"operator/alice", "device/22222222-2222-4222-8222-222222222222", "device/33333333-3333-4333-8333-333333333333"}
	var credentials []*localmesh.Credentials
	for i, p := range principals {
		var key crypto.Signer
		if i == 0 {
			key, err = mldsa.GenerateKey(mldsa.MLDSA65())
		} else {
			key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		}
		if err != nil {
			t.Fatal(err)
		}
		uri, _ := url.Parse("spiffe://wendy.sh/tenant/11111111-1111-4111-8111-111111111111/" + p)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		constructor := localmesh.NewCredentials
		if i == 0 {
			constructor = localmesh.NewOperatorCredentials
		}
		credential, err := constructor(certPEM, chain, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		credentials = append(credentials, credential)
	}
	return credentials
}
func TestOperatorMultihopTCPFailover(t *testing.T) {
	credentials := fixtureCredentials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	nodes := make([]*localmesh.Node, 3)
	networks := make([]*Network, 3)
	for i, c := range credentials {
		dir := t.TempDir()
		store, state, err := localmesh.OpenDeviceStateStore(filepath.Join(dir, "routing-state.json"), c.Device, babel.RouterID(0x1000000000001+i))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Save(state); err != nil {
			t.Fatal(err)
		}
		networks[i], err = NewNetwork(c.Device)
		if err != nil {
			t.Fatal(err)
		}
		port := uint16(50052)
		if i == 0 {
			port = 0
		}
		nodes[i], err = localmesh.NewNodeWithNetwork(ctx, dir, c, "robot", port, networks[i])
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := nodes[i].Run(); err != nil && ctx.Err() == nil {
				t.Errorf("node: %v", err)
			}
		}(i)
	}
	link := func(a, b int, cost uint16) context.CancelFunc {
		linkCtx, stop := context.WithCancel(ctx)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		serverConfig, err := credentials[b].PeerTLS(credentials[a].Device)
		if err != nil {
			t.Fatal(err)
		}
		serverConfig.NextProtos = []string{localmesh.StreamALPN}
		clientConfig, err := credentials[a].PeerTLS(credentials[b].Device)
		if err != nil {
			t.Fatal(err)
		}
		clientConfig.NextProtos = []string{localmesh.StreamALPN}
		ready := make(chan error, 2)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer listener.Close()
			raw, err := listener.Accept()
			if err != nil {
				ready <- err
				return
			}
			secure := tls.Server(raw, serverConfig)
			defer secure.Close()
			closeOnCancel := context.AfterFunc(linkCtx, func() { secure.Close() })
			defer closeOnCancel()
			err = secure.HandshakeContext(linkCtx)
			ready <- err
			if err == nil {
				_ = nodes[b].AttachStream(linkCtx, credentials[a].Device, secure, cost)
			}
		}()
		raw, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		secure := tls.Client(raw, clientConfig)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer secure.Close()
			closeOnCancel := context.AfterFunc(linkCtx, func() { secure.Close() })
			defer closeOnCancel()
			err := secure.HandshakeContext(linkCtx)
			ready <- err
			if err == nil {
				_ = nodes[a].AttachStream(linkCtx, credentials[b].Device, secure, cost)
			}
		}()
		for range 2 {
			if err := <-ready; err != nil {
				t.Fatal(err)
			}
		}
		return stop
	}
	stopFast := link(0, 1, 64)
	link(0, 1, 4096)
	link(1, 2, 512)
	waitFor := func(check func() bool) {
		t.Helper()
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("mesh did not converge: A=%+v B=%+v C=%+v", nodes[0].Snapshot(), nodes[1].Snapshot(), nodes[2].Snapshot())
			case <-ticker.C:
			}
		}
	}
	waitFor(func() bool {
		devices := devicesFromSnapshot(nodes[0].Snapshot(), time.Now())
		for _, d := range devices {
			if d.ID == credentials[2].Device.UUID() {
				return true
			}
		}
		return false
	})
	listener, err := gonet.ListenTCP(networks[2].stack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom16(credentials[2].Device.MeshAddress().As16()), Port: 9000}, ipv6.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	wg.Add(1)
	go func() {
		defer wg.Done()
		stop := context.AfterFunc(ctx, func() { listener.Close() })
		defer stop()
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		closeOnCancel := context.AfterFunc(ctx, func() { connection.Close() })
		defer closeOnCancel()
		io.Copy(connection, connection)
	}()
	connection, err := networks[0].DialContext(ctx, net.JoinHostPort(credentials[2].Device.MeshAddress().String(), "9000"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	roundtrip := func() {
		t.Helper()
		connection.SetDeadline(time.Now().Add(10 * time.Second))
		payload := bytes.Repeat([]byte("operator mesh failover"), 100)
		if _, err := connection.Write(payload); err != nil {
			t.Fatal(err)
		}
		answer := make([]byte, len(payload))
		if _, err := io.ReadFull(connection, answer); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, answer) {
			t.Fatal("corrupted data")
		}
	}
	roundtrip()
	stopFast()
	waitFor(func() bool {
		snapshot := nodes[0].Snapshot()
		for _, r := range snapshot.Routes {
			if r.Prefix.Addr() != credentials[2].Device.MeshAddress() || r.Unreachable {
				continue
			}
			for _, l := range snapshot.Links {
				if l.ID == r.Link && l.Cost == 4096 {
					return true
				}
			}
		}
		return false
	})
	roundtrip()
	// Operators carry return routes, never appear in the discoverable device roster.
	for _, d := range devicesFromSnapshot(nodes[2].Snapshot(), time.Now()) {
		if d.ID == credentials[0].Device.UUID() {
			t.Fatal("operator was listed as a device")
		}
	}
}
