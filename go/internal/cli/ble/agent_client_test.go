package ble

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/protobuf/proto"
)

func agentTestTLS(t *testing.T, serverIdentity string) (*tls.Config, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	issue := func(identity string, serial int64, usage x509.ExtKeyUsage) (string, string, tls.Certificate) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(identity)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, URIs: []*url.URL{u}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
		pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			t.Fatal(err)
		}
		return certPEM, keyPEM, pair
	}
	clientPEM, clientKey, _ := issue("urn:wendy:org:7:user:1", 2, x509.ExtKeyUsageClientAuth)
	_, _, serverPair := issue(serverIdentity, 3, x509.ExtKeyUsageServerAuth)
	expected := certs.WendyIdentity{OrgID: 7, EntityType: certs.EntityAsset, EntityID: "42"}
	clientConfig, err := NewClientTLSConfig(clientPEM, clientKey, certs.ServerVerifyOpts{ChainPEM: caPEM, ExpectedOrgID: 7, ExpectedIdentity: &expected})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(caPEM))
	return clientConfig, &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
}

func agentTestClient(t *testing.T, serve func(*tls.Conn)) *AgentClient {
	t.Helper()
	clientConfig, serverConfig := agentTestTLS(t, "urn:wendy:org:7:asset:42")
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer b.Close()
		server := tls.Server(b, serverConfig)
		if server.Handshake() == nil {
			serve(server)
		}
	}()
	t.Cleanup(func() {
		a.Close()
		b.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	client, err := connectAgentStream(context.Background(), a, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func readAgentCommand(conn io.Reader) (*agentpb.BluetoothCommand, error) {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint16(header[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	cmd := new(agentpb.BluetoothCommand)
	return cmd, proto.Unmarshal(body, cmd)
}
func writeAgentResponse(conn io.Writer, response *agentpb.BluetoothResponse) error {
	body, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	frame := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(frame, uint16(len(body)))
	copy(frame[2:], body)
	_, err = conn.Write(frame)
	return err
}
func versionCommand() *agentpb.BluetoothCommand {
	return &agentpb.BluetoothCommand{Command: &agentpb.BluetoothCommand_AgentVersion{AgentVersion: &agentpb.AgentVersionCommand{}}}
}

func TestAgentClientRejectsWrongDeviceBeforeCommands(t *testing.T) {
	clientConfig, serverConfig := agentTestTLS(t, "urn:wendy:org:7:asset:99")
	// TCP buffering lets TLS send a fatal alert while the server finishes its
	// first flight, unlike net.Pipe's two simultaneous synchronous writes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		done <- tls.Server(conn, serverConfig).Handshake()
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = connectAgentStream(context.Background(), conn, clientConfig)
	var mismatch *certs.IdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	<-done
}

func TestAgentClientHandshakeCancellation(t *testing.T) {
	clientConfig, _ := agentTestTLS(t, "urn:wendy:org:7:asset:42")
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := connectAgentStream(ctx, a, clientConfig)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("handshake not bounded: %v", err)
	}
}

func TestAgentClientSerializesCommandsAndRetainsVerifiedIdentity(t *testing.T) {
	client := agentTestClient(t, func(server *tls.Conn) {
		for i := 0; i < 2; i++ {
			cmd, err := readAgentCommand(server)
			if err != nil {
				return
			}
			var response *agentpb.BluetoothResponse
			if cmd.GetAgentVersion() != nil {
				response = &agentpb.BluetoothResponse{Response: &agentpb.BluetoothResponse_AgentVersion{AgentVersion: &agentpb.AgentVersionResponse{Version: "test"}}}
			} else {
				response = &agentpb.BluetoothResponse{Response: &agentpb.BluetoothResponse_WifiStatus{WifiStatus: &agentpb.WifiStatusResponse{Connected: true}}}
			}
			time.Sleep(10 * time.Millisecond)
			if writeAgentResponse(server, response) != nil {
				return
			}
		}
	})
	id, ok := client.ObservedServerIdentity()
	if !ok || id.EntityID != "42" {
		t.Fatalf("identity = %+v, %v", id, ok)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r, err := client.AgentVersion()
		if err != nil || r.GetVersion() != "test" {
			t.Errorf("version: %v, %v", r, err)
		}
	}()
	go func() {
		defer wg.Done()
		r, err := client.WifiStatus()
		if err != nil || !r.GetConnected() {
			t.Errorf("status: %v, %v", r, err)
		}
	}()
	wg.Wait()
}

func TestAgentClientRejectsOversizedCommandWithoutWriting(t *testing.T) {
	client := agentTestClient(t, func(server *tls.Conn) {
		cmd, err := readAgentCommand(server)
		if err != nil {
			return
		}
		if cmd.GetAgentVersion() == nil {
			t.Error("oversized command reached server")
			return
		}
		_ = writeAgentResponse(server, &agentpb.BluetoothResponse{Response: &agentpb.BluetoothResponse_AgentVersion{AgentVersion: &agentpb.AgentVersionResponse{Version: "test"}}})
	})
	if err := client.WifiConnect(strings.Repeat("x", 65536), ""); err == nil {
		t.Fatal("oversized command accepted")
	}
	if _, err := client.AgentVersion(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentClientCancellationClosesStalledWrite(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	client := agentTestClient(t, func(*tls.Conn) { <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.sendCommandContext(ctx, versionCommand())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("stalled write: %v", err)
	}
}

func TestAgentClientRejectsEmptyResponseAndCloses(t *testing.T) {
	client := agentTestClient(t, func(server *tls.Conn) {
		if _, err := readAgentCommand(server); err == nil {
			_, _ = server.Write([]byte{0, 0})
		}
	})
	if _, err := client.AgentVersion(); err == nil {
		t.Fatal("empty response accepted")
	}
	if _, err := client.AgentVersion(); err == nil {
		t.Fatal("failed stream reused")
	}
}

func TestAgentClientApplicationErrorPreservesSession(t *testing.T) {
	client := agentTestClient(t, func(server *tls.Conn) {
		if _, err := readAgentCommand(server); err != nil {
			return
		}
		if err := writeAgentResponse(server, &agentpb.BluetoothResponse{Response: &agentpb.BluetoothResponse_Error{Error: &agentpb.ErrorResponse{Message: "incorrect WiFi password"}}}); err != nil {
			return
		}
		if _, err := readAgentCommand(server); err != nil {
			return
		}
		_ = writeAgentResponse(server, &agentpb.BluetoothResponse{Response: &agentpb.BluetoothResponse_AgentVersion{AgentVersion: &agentpb.AgentVersionResponse{Version: "test"}}})
	})
	if err := client.WifiConnect("network", "wrong-password"); err == nil {
		t.Fatalf("application error=%v", err)
	}
	if response, err := client.AgentVersion(); err != nil || response.GetVersion() != "test" {
		t.Fatalf("application error poisoned stream: response=%v, err=%v", response, err)
	}
}
