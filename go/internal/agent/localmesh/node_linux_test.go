//go:build linux

package localmesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// nodeTestConfig is handed to each re-executed peer process. Identity is the
// device principal; the underlay address is transport only and never an identity.
type nodeTestConfig struct {
	Device, Peer      string
	Cert, Chain, Key  string
	Dir               string
	Underlay, Address string
}

func TestNodePeerProcess(t *testing.T) {
	path := os.Getenv("WENDY_LOCALMESH_NODE_TEST_CONFIG")
	if path == "" {
		t.Skip("isolated test subprocess")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config nodeTestConfig
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	credentials, err := NewCredentials(config.Cert, config.Chain, config.Key)
	if err != nil {
		t.Fatal(err)
	}
	self := credentials.Device
	if self.Principal() != config.Device {
		t.Fatalf("credentials carry %s, config names %s", self.Principal(), config.Device)
	}
	peer := testDevice(t, config.Peer)
	if self.Principal() == peer.Principal() {
		t.Fatal("test config names itself as the peer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	node, err := NewNode(ctx, config.Dir, credentials, "test", 50052)
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- node.Run() }()
	defer func() { cancel(); <-runDone }()
	tlsConfig, err := credentials.PeerTLS(peer)
	if err != nil {
		t.Fatal(err)
	}
	// The higher UUID serves; the lower one drives the request. Which device is
	// which follows from the principals in the config, not from a numeric asset.
	if meshid.Compare(self, peer) > 0 {
		server := &http.Server{Addr: fmt.Sprintf("[%s]:18080", self.MeshAddress()), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.RemoteAddr, "["+peer.MeshAddress().String()+"]:") {
				http.Error(w, "request bypassed mesh source addressing", http.StatusForbidden)
				return
			}
			fmt.Fprint(w, "hello over Babel and QUIC")
		})}
		defer server.Close()
		go server.ListenAndServe()
		listener, err := quic.ListenAddr(config.Underlay, tlsConfig, QUICConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		conn, err := listener.Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = node.Attach(ctx, peer, conn)
		return
	}
	var conn *quic.Conn
	for ctx.Err() == nil {
		attempt, cancelAttempt := context.WithTimeout(ctx, time.Second)
		conn, err = quic.DialAddr(attempt, config.Address, tlsConfig, QUICConfig())
		cancelAttempt()
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	linkDone := make(chan error, 1)
	go func() { linkDone <- node.Attach(ctx, peer, conn) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	for ctx.Err() == nil {
		response, err := client.Get(fmt.Sprintf("http://[%s]:18080/", peer.MeshAddress()))
		if err == nil {
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err == nil && string(body) == "hello over Babel and QUIC" {
				found := false
				for _, m := range node.Snapshot().Devices {
					if m.Device == peer.UUID() {
						found = true
					}
				}
				if found {
					t.Log("HTTP and signed device discovery succeeded across live Babel/QUIC/TUN nodes")
					cancel()
					<-linkDone
					return
				}
			}
		}
		select {
		case err := <-linkDone:
			t.Fatal("link ended", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timed out", node.Snapshot())
}

// TestNodePairIsolatedNamespaces runs two real nodes in disposable namespaces on
// a veth underlay: authenticated QUIC link, Babel route exchange, TUN payload to
// the peer's ULA address, and signed device discovery keyed by device principal.
func TestNodePairIsolatedNamespaces(t *testing.T) {
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("requires disposable privileged container")
	}
	if os.Getenv("WENDY_LOCALMESH_NODE_TEST_CONFIG") != "" {
		t.Skip("parent test only")
	}
	names := []string{fmt.Sprintf("wlma%d", os.Getpid()), fmt.Sprintf("wlmb%d", os.Getpid())}
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("ip", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ip %v: %v %s", args, err, out)
		}
	}
	for _, name := range names {
		run("netns", "add", name)
		defer exec.Command("ip", "netns", "del", name).Run()
	}
	run("link", "add", "wlmtesta", "type", "veth", "peer", "name", "wlmtestb")
	run("link", "set", "wlmtesta", "netns", names[0])
	run("link", "set", "wlmtestb", "netns", names[1])
	for i, name := range names {
		iface := []string{"wlmtesta", "wlmtestb"}[i]
		run("-n", name, "addr", "add", fmt.Sprintf("10.200.0.%d/30", i+1), "dev", iface)
		run("-n", name, "link", "set", iface, "up")
		run("-n", name, "link", "set", "lo", "up")
	}
	dir := t.TempDir()
	configs := map[string]string{}
	testCredentialsWithPEM(t, func(device meshid.Device, cert, chain, key string) {
		lower := testDevice(t, testPrincipal(testTenantUUID, testDeviceAID))
		higher := testDevice(t, testPrincipal(testTenantUUID, testDeviceBID))
		peer, underlay, address := higher, "10.200.0.2:18500", "10.200.0.2:18500"
		if device.Principal() == higher.Principal() {
			peer, underlay, address = lower, "10.200.0.2:18500", "10.200.0.1:18500"
		}
		cfg := nodeTestConfig{Device: device.Principal(), Peer: peer.Principal(), Cert: cert, Chain: chain, Key: key, Dir: filepath.Join(dir, device.UUID().String()), Underlay: underlay, Address: address}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, device.UUID().String()+".json")
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		configs[device.Principal()] = path
	})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lower := testPrincipal(testTenantUUID, testDeviceAID)
	higher := testPrincipal(testTenantUUID, testDeviceBID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, "ip", "netns", "exec", names[1], binary, "-test.run", "^TestNodePeerProcess$", "-test.v")
	server.Env = append(os.Environ(), "WENDY_LOCALMESH_NODE_TEST_CONFIG="+configs[higher])
	var serverOutput bytes.Buffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	client := exec.CommandContext(ctx, "ip", "netns", "exec", names[0], binary, "-test.run", "^TestNodePeerProcess$", "-test.v")
	client.Env = append(os.Environ(), "WENDY_LOCALMESH_NODE_TEST_CONFIG="+configs[lower])
	out, clientErr := client.CombinedOutput()
	serverErr := server.Wait()
	if clientErr != nil || serverErr != nil {
		t.Fatalf("client: %v\n%s\nserver: %v\n%s", clientErr, out, serverErr, serverOutput.String())
	}
	if !strings.Contains(string(out), "HTTP and signed device discovery succeeded") {
		t.Fatal(string(out))
	}
	t.Log(string(out))
}
