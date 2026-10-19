package commands

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/agent/trustedkeys"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// dialLocalDevice connects to dev through the real ladder with this
// computer's local key, as `wendy run` does before it pushes.
func dialLocalDevice(t *testing.T, dev localDevice) *grpcclient.AgentConnection {
	t.Helper()
	conn, _, err := dialAgentLadderWithCerts(ladderCtx(t), newDialTarget("box", dev.grpcAddr), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if !conn.IsLocalOperator {
		t.Fatal("not a local-operator connection")
	}
	return conn
}

// getV2 asks the device registry for /v2/ through the plain-HTTP address a
// push tool is handed.
func getV2(t *testing.T, addr string) (int, error) {
	t.Helper()
	// The buildx proxy names host.docker.internal off Linux; the proxy
	// itself listens on loopback.
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "host.docker.internal" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + addr + "/v2/")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// registryPaths are the push paths that reach a LAN device's registry, each
// returning the plain-HTTP loopback address its push tool is handed.
var registryPaths = map[string]func(context.Context, *grpcclient.AgentConnection, int) (string, error){
	// Swift container plugin and the OCI-layout push (pushOCILayoutToRegistry
	// is handed this same address).
	"swift and oci-layout": func(ctx context.Context, conn *grpcclient.AgentConnection, port int) (string, error) {
		addr, useMTLS, _, _, err := resolveRegistryForSwiftAgent(ctx, conn, port)
		if err == nil && useMTLS {
			err = errors.New("swift path asked the tool to do mTLS itself")
		}
		return addr, err
	},
	"apple container": func(ctx context.Context, conn *grpcclient.AgentConnection, port int) (string, error) {
		addr, _, useMTLS, _, err := resolveRegistryForAppleContainer(ctx, conn, port)
		if err == nil && useMTLS {
			err = errors.New("apple container path asked the tool to do mTLS itself")
		}
		return addr, err
	},
	"docker buildx": func(ctx context.Context, conn *grpcclient.AgentConnection, port int) (string, error) {
		addr, _, useMTLS, _, err := resolveRegistryForImageBuilder(ctx, conn, port, imageBuilderDocker)
		if err == nil && useMTLS {
			// The local key must never be handed to BuildKit.
			err = errors.New("docker path handed BuildKit an mTLS registry for a local device")
		}
		return addr, err
	},
}

func TestRegistryPathsPushWithLocalKeyToLocalDevice(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	operator := useLocalKey(t)
	owner, _ := localkey.LoadOrCreate(t.TempDir() + "/" + localkey.FileName)
	dev := startLocalDevice(t, map[crypto.Signer]trustedkeys.Role{owner: trustedkeys.RoleOwner, operator: trustedkeys.RoleOperator})
	conn := dialLocalDevice(t, dev)
	if err := requireRegistryAuth(ladderCtx(t), conn); err != nil {
		t.Fatalf("requireRegistryAuth: %v", err)
	}
	for name, resolve := range registryPaths {
		t.Run(name, func(t *testing.T) {
			addr, err := resolve(ladderCtx(t), conn, dev.registryPort)
			if err != nil {
				t.Fatal(err)
			}
			if code, err := getV2(t, addr); err != nil || code != http.StatusOK {
				t.Fatalf("GET /v2/ via %s = %d, %v; want 200 from the agent's registry", addr, code, err)
			}
		})
	}
}

// The agent's registry admits operator and owner keys only.
func TestRegistryPathsRefusedForViewerKey(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	viewer := useLocalKey(t)
	owner, _ := localkey.LoadOrCreate(t.TempDir() + "/" + localkey.FileName)
	dev := startLocalDevice(t, map[crypto.Signer]trustedkeys.Role{owner: trustedkeys.RoleOwner, viewer: trustedkeys.RoleViewer})
	conn := dialLocalDevice(t, dev)
	for name, resolve := range registryPaths {
		t.Run(name, func(t *testing.T) {
			addr, err := resolve(ladderCtx(t), conn, dev.registryPort)
			if err != nil {
				t.Fatal(err)
			}
			if code, err := getV2(t, addr); err == nil && code == http.StatusOK {
				t.Fatal("viewer key pushed to the registry")
			}
		})
	}
}

// A registry answering under another key than the device the gRPC connection
// verified is not that device's registry.
func TestRegistryPathsRefuseAnotherDeviceKey(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	forbidPlaintext(t)
	owner := useLocalKey(t)
	dev := startLocalDevice(t, map[crypto.Signer]trustedkeys.Role{owner: trustedkeys.RoleOwner})
	conn := dialLocalDevice(t, dev)
	otherKeyPEM, _ := certs.GenerateKeyPair()
	otherCert, _ := mtls.SelfSignedCert(otherKeyPEM, time.Now())
	impostor := startLocalRegistry(t, otherCert, otherKeyPEM, dev.keys)
	for name, resolve := range registryPaths {
		t.Run(name, func(t *testing.T) {
			addr, err := resolve(ladderCtx(t), conn, impostor)
			if err != nil {
				t.Fatal(err)
			}
			if code, err := getV2(t, addr); err == nil && code == http.StatusOK {
				t.Fatal("pushed to a registry with another device key")
			}
		})
	}
}

func TestRegistryTLSConfigChoosesTheConnectionsCredential(t *testing.T) {
	setTempConfig(t, &config.Config{})

	// Plaintext connection: the device serves no TLS, so neither does its registry.
	if cfg, err := registryTLSConfig(grpcclient.NewFromConn(nil)); cfg != nil || err != nil {
		t.Fatalf("plaintext: (%v, %v), want (nil, nil)", cfg, err)
	}

	// Cloud: the certificate the connection authenticated with.
	cert := selfSignedCLICert(t, 7)
	cloud := grpcclient.NewFromConn(nil)
	cloud.IsMTLS, cloud.CertInfo = true, &cert
	cfg, err := registryTLSConfig(cloud)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := certs.ParseCertsFromPEM([]byte(cert.PemCertificate))
	if string(cfg.Certificates[0].Certificate[0]) != string(leaf[0].Raw) {
		t.Fatal("cloud registry TLS does not present the connection's certificate")
	}

	// mTLS without any credential: refused, never plaintext.
	bare := grpcclient.NewFromConn(nil)
	bare.IsMTLS = true
	if _, err := registryTLSConfig(bare); !errors.Is(err, errRegistryAuth) {
		t.Fatalf("mTLS without certificates: %v, want errRegistryAuth", err)
	}
	if err := requireRegistryAuth(context.Background(), bare); !errors.Is(err, errRegistryAuth) {
		t.Fatalf("requireRegistryAuth without certificates: %v", err)
	}

	// Local operator connection whose device key was never verified: refused.
	local := grpcclient.NewFromConn(nil)
	local.IsMTLS, local.IsLocalOperator = true, true
	if _, err := registryTLSConfig(local); !errors.Is(err, errRegistryAuth) || !strings.Contains(fmt.Sprint(err), "not verified") {
		t.Fatalf("unverified local device: %v", err)
	}
}
