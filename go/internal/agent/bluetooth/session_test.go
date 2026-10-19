package bluetooth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type sessionPKI struct {
	server *tls.Config
	client *tls.Config
}

func newSessionPKI(t *testing.T, principal string) sessionPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "BLE test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	issue := func(serial int64, identity string) (tls.Certificate, string, string) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "BLE test peer"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), DNSNames: []string{"agent"},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
		if identity != "" {
			u, err := url.Parse(identity)
			if err != nil {
				t.Fatal(err)
			}
			leaf.URIs = []*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &k.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		kp, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kp})
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return pair, string(certPEM), string(keyPEM)
	}
	_, serverCert, serverKey := issue(2, "urn:wendy:org:7:asset:42")
	server, err := mtls.NewTLSConfig(serverCert, string(caPEM), serverKey, zap.NewNop(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	clientCert, _, _ := issue(3, principal)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	client := &tls.Config{Certificates: []tls.Certificate{clientCert}, RootCAs: pool, ServerName: "agent", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	return sessionPKI{server: server, client: client}
}

func startTestSession(t *testing.T, pki sessionPKI, limits bleSessionLimits, dispatch bleDispatch) (*tls.Conn, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	serverRaw, clientRaw := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveBLESession(ctx, serverRaw, zap.NewNop(), dispatch, pki.server, certs.Scope{OrgID: 7}, interceptor.OrgModeStrict, limits)
	}()
	client := tls.Client(clientRaw, pki.client)
	client.SetDeadline(time.Now().Add(3 * time.Second))
	t.Cleanup(func() { cancel(); clientRaw.Close(); waitSession(t, done) })
	if err := client.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	return client, cancel, done
}

func waitSession(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("BLE session did not stop")
	}
}

func versionCommand(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&agentpb.BluetoothCommand{Command: &agentpb.BluetoothCommand_AgentVersion{AgentVersion: &agentpb.AgentVersionCommand{}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBLESessionAuthorizesBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, principal string
		wantCalls       int32
	}{
		{"same org", "urn:wendy:org:7:user:test", 1},
		{"other org", "urn:wendy:org:8:user:test", 0},
		{"missing identity", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pki := newSessionPKI(t, tc.principal)
			var calls atomic.Int32
			client, cancel, done := startTestSession(t, pki, defaultBLESessionLimits, func(ctx context.Context, _ *agentpb.BluetoothCommand) *agentpb.BluetoothResponse {
				if err := interceptor.CheckMTLS(ctx, zap.NewNop(), certs.Scope{OrgID: 7}, interceptor.OrgModeStrict); err != nil {
					t.Errorf("handler missing authenticated peer: %v", err)
				}
				calls.Add(1)
				return errResp("response")
			})
			err := writeFrameToConn(client, versionCommand(t))
			if tc.wantCalls > 0 {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := readMessageFromConn(client); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			waitSession(t, done)
			if calls.Load() != tc.wantCalls {
				t.Fatalf("dispatches=%d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestBLESessionPartialFrameTimeoutCloses(t *testing.T) {
	pki := newSessionPKI(t, "urn:wendy:org:7:user:test")
	for _, prefix := range [][]byte{{0}, {0, 5, 1, 2}} {
		t.Run(fmt.Sprintf("prefix%d", len(prefix)), func(t *testing.T) {
			limits := defaultBLESessionLimits
			limits.idle = 30 * time.Millisecond
			var calls atomic.Int32
			client, _, done := startTestSession(t, pki, limits, func(context.Context, *agentpb.BluetoothCommand) *agentpb.BluetoothResponse {
				calls.Add(1)
				return errResp("unexpected")
			})
			if _, err := client.Write(prefix); err != nil {
				t.Fatal(err)
			}
			waitSession(t, done)
			if calls.Load() != 0 {
				t.Fatal("partial frame dispatched")
			}
			if _, err := client.Read(make([]byte, 1)); err == nil {
				t.Fatal("connection remained open")
			}
		})
	}
}

func TestBLESessionBoundedHandshakeAndCancellation(t *testing.T) {
	pki := newSessionPKI(t, "urn:wendy:org:7:user:test")
	t.Run("silent handshake", func(t *testing.T) {
		server, client := net.Pipe()
		defer client.Close()
		limits := defaultBLESessionLimits
		limits.handshake = 25 * time.Millisecond
		done := make(chan struct{})
		go func() {
			defer close(done)
			serveBLESession(context.Background(), server, zap.NewNop(), nil, pki.server, certs.Scope{OrgID: 7}, interceptor.OrgModeStrict, limits)
		}()
		waitSession(t, done)
	})
	t.Run("idle cancellation", func(t *testing.T) {
		_, cancel, done := startTestSession(t, pki, defaultBLESessionLimits, func(context.Context, *agentpb.BluetoothCommand) *agentpb.BluetoothResponse { return errResp("unused") })
		cancel()
		waitSession(t, done)
	})
	t.Run("blocked response", func(t *testing.T) {
		limits := defaultBLESessionLimits
		limits.write = 25 * time.Millisecond
		client, _, done := startTestSession(t, pki, limits, func(context.Context, *agentpb.BluetoothCommand) *agentpb.BluetoothResponse {
			return errResp("response")
		})
		if err := writeFrameToConn(client, versionCommand(t)); err != nil {
			t.Fatal(err)
		}
		waitSession(t, done)
	})
	t.Run("command cancellation", func(t *testing.T) {
		started := make(chan struct{})
		client, cancel, done := startTestSession(t, pki, defaultBLESessionLimits, func(ctx context.Context, _ *agentpb.BluetoothCommand) *agentpb.BluetoothResponse {
			close(started)
			<-ctx.Done()
			return errResp(ctx.Err().Error())
		})
		if err := writeFrameToConn(client, versionCommand(t)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("command did not start")
		}
		cancel()
		waitSession(t, done)
	})
	t.Run("command budget", func(t *testing.T) {
		limits := defaultBLESessionLimits
		limits.command = 25 * time.Millisecond
		client, _, _ := startTestSession(t, pki, limits, func(ctx context.Context, _ *agentpb.BluetoothCommand) *agentpb.BluetoothResponse {
			<-ctx.Done()
			return errResp(ctx.Err().Error())
		})
		if err := writeFrameToConn(client, versionCommand(t)); err != nil {
			t.Fatal(err)
		}
		payload, err := readMessageFromConn(client)
		if err != nil {
			t.Fatal(err)
		}
		var response agentpb.BluetoothResponse
		if err := proto.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		if response.GetError().GetMessage() != context.DeadlineExceeded.Error() {
			t.Fatalf("response=%v", &response)
		}
	})
	t.Run("session lifetime", func(t *testing.T) {
		limits := defaultBLESessionLimits
		limits.lifetime = 50 * time.Millisecond
		_, _, done := startTestSession(t, pki, limits, nil)
		waitSession(t, done)
	})
}

type shortFrameWriter struct{ bytes.Buffer }

func (w *shortFrameWriter) Write(p []byte) (int, error) { return w.Buffer.Write(p[:min(3, len(p))]) }
func TestBLEFramesBoundsAndShortWrites(t *testing.T) {
	for _, size := range []int{1, 256, maxFrameSize} {
		data := bytes.Repeat([]byte{0xa5}, size)
		w := new(shortFrameWriter)
		if err := writeFrameToConn(w, data); err != nil {
			t.Fatal(err)
		}
		got, err := readMessageFromConn(&w.Buffer)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: payload mismatch: %v", size, err)
		}
	}
	for _, data := range [][]byte{nil, make([]byte, maxFrameSize+1)} {
		if writeFrameToConn(io.Discard, data) == nil {
			t.Fatal("invalid frame accepted")
		}
	}
	for _, data := range [][]byte{{0, 0}, {0, 2, 1}, {0}} {
		if _, err := readMessageFromConn(bytes.NewReader(data)); err == nil {
			t.Fatal("invalid/truncated frame accepted")
		}
	}
}

func TestPeripheralInfoRequiresEnrolledTLSIdentity(t *testing.T) {
	pki := newSessionPKI(t, "urn:wendy:org:7:user:test")
	if _, err := peripheralInfo(pki.server, certs.Scope{OrgID: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := peripheralInfo(nil, certs.Scope{OrgID: 7}); err == nil {
		t.Fatal("unenrolled config accepted")
	}
	if _, err := peripheralInfo(pki.server, certs.Scope{OrgID: 8}); err == nil {
		t.Fatal("wrong authorization scope accepted")
	}
	unsafe := pki.server.Clone()
	unsafe.ClientAuth = tls.NoClientCert
	if _, err := peripheralInfo(unsafe, certs.Scope{OrgID: 7}); err == nil {
		t.Fatal("unauthenticated config accepted")
	}
}
