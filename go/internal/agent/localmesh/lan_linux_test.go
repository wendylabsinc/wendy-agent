//go:build linux

package localmesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"go.uber.org/zap"
)

type lanProcessConfig struct {
	Identity    TCPIdentity
	Dir         string
	Dual        bool
	DisableFile string
}

func TestLANInterfaceClassification(t *testing.T) {
	root := t.TempDir()
	makeIface := func(name, kind string, paths ...string) {
		t.Helper()
		base := filepath.Join(root, name)
		if err := os.MkdirAll(base, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "type"), []byte(kind), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if err := os.Mkdir(filepath.Join(base, path), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	makeIface("eth0", "1", "device")
	makeIface("wlan0", "1", "device", "wireless")
	makeIface("ap0", "1", "device", "wireless")
	makeIface("br0", "1")
	makeIface("mesh0", "65534", "device")
	managed := func(name string) bool { return name == "wlan0" }
	tests := []struct {
		name   string
		eth    bool
		wifi   bool
		want   uint16
		wantOK bool
	}{
		{"eth0", true, false, EthernetLinkCost, true},
		{"eth0", false, true, 0, false},
		{"wlan0", false, true, WiFiLinkCost, true},
		{"wlan0", true, false, 0, false},
		{"ap0", true, true, 0, false},
		{"br0", true, true, 0, false},
		{"mesh0", true, true, 0, false},
	}
	for _, tt := range tests {
		cost, ok := classifyLANInterface(tt.name, root, tt.eth, tt.wifi, managed)
		if cost != tt.want || ok != tt.wantOK {
			t.Errorf("%s Ethernet=%v WiFi=%v: got cost=%d ok=%v; want %d %v", tt.name, tt.eth, tt.wifi, cost, ok, tt.want, tt.wantOK)
		}
	}
}

func TestLANPeerProcess(t *testing.T) {
	path := os.Getenv("WENDY_LOCALMESH_LAN_TEST_CONFIG")
	if path == "" {
		t.Skip("isolated LAN peer subprocess")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg lanProcessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	iface, err := net.InterfaceByName("wllan0")
	if err != nil {
		t.Fatal(err)
	}
	ip := net.IPv4(10, 201, 0, 1)
	if lanIdentityDevice(t, cfg.Identity) == lanDevice(t, 460) {
		ip = net.IPv4(10, 201, 0, 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	meshDone := make(chan error, 1)
	nodeReady := make(chan *Node, 1)
	go func() {
		meshDone <- RunConfiguredWithNode(ctx, cfg.Dir, cfg.Identity, func(node *Node, config *TCPConfig) {
			if node != nil {
				nodeReady <- node
				go func() {
					logger, _ := zap.NewDevelopment()
					_ = runLANWithScan(ctx, LANConfig{Credentials: node.Credentials, Node: node, Ethernet: true, Selection: NewPeerSelection(node.Snapshot), Logger: logger}, func() ([]lanInterface, error) {
						result := []lanInterface{{iface: *iface, ip: ip, net: &net.IPNet{IP: net.IPv4(10, 201, 0, 0), Mask: net.CIDRMask(30, 32)}, cost: EthernetLinkCost}}
						if cfg.Dual {
							if _, err := os.Stat(cfg.DisableFile); err == nil {
								result = nil
							}
							wifiIface, err := net.InterfaceByName("wllan1")
							if err != nil {
								return nil, err
							}
							wifiIP := net.IPv4(10, 202, 0, 1)
							if lanIdentityDevice(t, cfg.Identity) == lanDevice(t, 460) {
								wifiIP = net.IPv4(10, 202, 0, 2)
							}
							result = append(result, lanInterface{iface: *wifiIface, ip: wifiIP, net: &net.IPNet{IP: net.IPv4(10, 202, 0, 0), Mask: net.CIDRMask(30, 32)}, cost: WiFiLinkCost})
						}
						return result, nil
					})
				}()
			}
		})
	}()
	var node *Node
	select {
	case node = <-nodeReady:
	case <-ctx.Done():
		t.Fatal("mesh node did not start")
	}
	waitCost := func(cost uint16) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			links := node.Snapshot().Links
			if len(links) == 1 && links[0].Device == map[bool]meshid.Device{true: lanDevice(t, 445), false: lanDevice(t, 460)}[lanIdentityDevice(t, cfg.Identity) == lanDevice(t, 460)] && links[0].Cost == cost {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("LAN link did not settle to cost %d: %+v", cost, node.Snapshot().Links)
	}
	if lanIdentityDevice(t, cfg.Identity) == lanDevice(t, 460) {
		var listener net.Listener
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			listener, err = net.Listen("tcp", net.JoinHostPort(lanDevice(t, 460).MeshAddress().String(), "18080"))
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		seen := make(chan struct{}, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "authenticated LAN mesh")
			select {
			case seen <- struct{}{}:
			default:
			}
		})}
		go server.Serve(listener)
		requests := 1
		if cfg.Dual {
			requests = 2
		}
		for i := 0; i < requests; i++ {
			select {
			case <-seen:
			case <-ctx.Done():
				t.Fatal("no routed LAN request")
			}
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(cfg.DisableFile + ".client-done"); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	} else {
		request := func() {
			t.Helper()
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
				response, err := client.Get("http://" + net.JoinHostPort(lanDevice(t, 460).MeshAddress().String(), "18080") + "/")
				if err == nil {
					body, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					if readErr == nil && response.StatusCode == 200 && string(body) == "authenticated LAN mesh" {
						return
					}
				}
				time.Sleep(150 * time.Millisecond)
			}
			t.Fatal("LAN mDNS/QUIC link did not route HTTP")
		}
		waitCost(EthernetLinkCost)
		request()
		if cfg.Dual {
			if err := os.WriteFile(cfg.DisableFile, []byte("off"), 0600); err != nil {
				t.Fatal(err)
			}
			waitCost(WiFiLinkCost)
			request()
		}
		if err := os.WriteFile(cfg.DisableFile+".client-done", []byte("done"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if err := <-meshDone; err != nil {
		t.Fatal(err)
	}
}

func TestLANPairIsolatedNamespaces(t *testing.T) {
	testLANPairNamespaces(t, false)
}

func TestLANEthernetPreferenceAndWiFiFailover(t *testing.T) {
	testLANPairNamespaces(t, true)
}

func testLANPairNamespaces(t *testing.T, dual bool) {
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("requires disposable privileged container")
	}
	if os.Getenv("WENDY_LOCALMESH_LAN_TEST_CONFIG") != "" {
		t.Skip("parent test only")
	}
	names := []string{fmt.Sprintf("wllana%d", os.Getpid()), fmt.Sprintf("wllanb%d", os.Getpid())}
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
	run("link", "add", "wllan0", "netns", names[0], "type", "veth", "peer", "name", "wllan0", "netns", names[1])
	if dual {
		run("link", "add", "wllan1", "netns", names[0], "type", "veth", "peer", "name", "wllan1", "netns", names[1])
	}
	for i, name := range names {
		run("-n", name, "addr", "add", fmt.Sprintf("10.201.0.%d/30", i+1), "dev", "wllan0")
		run("-n", name, "link", "set", "wllan0", "up")
		run("-n", name, "link", "set", "lo", "up")
		if dual {
			run("-n", name, "addr", "add", fmt.Sprintf("10.202.0.%d/30", i+1), "dev", "wllan1")
			run("-n", name, "link", "set", "wllan1", "up")
		}
	}
	dir := t.TempDir()
	disableFile := filepath.Join(dir, "ethernet-disabled")
	configs := map[meshid.Device]string{}
	testCredentialsWithPEM(t, func(asset meshid.Device, cert, chain, key string) {
		configDir := filepath.Join(dir, fmt.Sprint(asset))
		if err := os.MkdirAll(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, tcpConfigName), []byte("{\"ethernet\":true}"), 0600); err != nil {
			t.Fatal(err)
		}
		processData, _ := json.Marshal(lanProcessConfig{Identity: TCPIdentity{Name: "lan-test", AgentPort: 50052, Certificate: cert, Chain: chain, Key: key}, Dir: configDir, Dual: dual, DisableFile: disableFile})
		path := filepath.Join(dir, fmt.Sprintf("%s.json", asset.UUID()))
		if err := os.WriteFile(path, processData, 0600); err != nil {
			t.Fatal(err)
		}
		configs[asset] = path
	})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, "ip", "netns", "exec", names[1], binary, "-test.run", "^TestLANPeerProcess$", "-test.v")
	server.Env = append(os.Environ(), "WENDY_LOCALMESH_LAN_TEST_CONFIG="+configs[lanDevice(t, 460)])
	var serverOutput bytes.Buffer
	server.Stdout, server.Stderr = &serverOutput, &serverOutput
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	client := exec.CommandContext(ctx, "ip", "netns", "exec", names[0], binary, "-test.run", "^TestLANPeerProcess$", "-test.v")
	client.Env = append(os.Environ(), "WENDY_LOCALMESH_LAN_TEST_CONFIG="+configs[lanDevice(t, 445)])
	out, clientErr := client.CombinedOutput()
	serverErr := server.Wait()
	if clientErr != nil || serverErr != nil {
		t.Fatalf("client: %v\n%s\nserver: %v\n%s", clientErr, out, serverErr, serverOutput.String())
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatal(string(out))
	}
}

func TestLANAdvertisementFilters(t *testing.T) {
	self, peer := lanDevice(t, 445), lanDevice(t, 460)
	base := mdns.ServiceEntry{Port: LANPort, AddrV4: net.IPv4(192, 168, 2, 3), InfoFields: []string{"principal=" + peer.Principal(), "mesh=" + lanMeshHash(self.Tenant())}}
	if device, ip, ok := parseLANEntry(&base, self); !ok || device != peer || !ip.Equal(base.AddrV4) {
		t.Fatalf("valid entry rejected: %v %v", device, ok)
	}
	for _, mutate := range []func(*mdns.ServiceEntry){
		func(e *mdns.ServiceEntry) { e.Port++ },
		func(e *mdns.ServiceEntry) { e.AddrV4 = net.IPv4(127, 0, 0, 1) },
		func(e *mdns.ServiceEntry) {
			e.InfoFields[0] = "principal=" + testPrincipal(testOtherTenant, testDeviceBID)
		},
		func(e *mdns.ServiceEntry) { e.InfoFields[0] = "principal=" + self.Principal() },
		func(e *mdns.ServiceEntry) { e.InfoFields[1] = "mesh=wrong" },
		func(e *mdns.ServiceEntry) { e.InfoFields = append(e.InfoFields, "principal="+peer.Principal()) },
	} {
		entry := base
		entry.InfoFields = append([]string(nil), base.InfoFields...)
		mutate(&entry)
		if _, _, ok := parseLANEntry(&entry, self); ok {
			t.Fatalf("invalid entry accepted: %+v", entry)
		}
	}
}
func lanDevice(t *testing.T, number int) meshid.Device {
	return testDevice(t, testPrincipal(testTenantUUID, fmt.Sprintf("2b1c0000-0000-4000-8000-%012d", number)))
}
func lanIdentityDevice(t *testing.T, identity TCPIdentity) meshid.Device {
	t.Helper()
	c, err := NewCredentials(identity.Certificate, identity.Chain, identity.Key)
	if err != nil {
		t.Fatal(err)
	}
	return c.Device
}
