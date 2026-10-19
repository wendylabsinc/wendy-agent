// go/internal/agent/bluetooth/dispatcher_test.go
package bluetooth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/bluetooth"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/protobuf/proto"
)

// ── Mocks ────────────────────────────────────────────────────────────────────

type mockNet struct {
	networks       []*agentpb.ListWiFiNetworksResponse_WiFiNetwork
	knownNetworks  []*agentpb.ListKnownWiFiNetworksResponse_KnownWiFiNetwork
	connectRequest *agentpb.ConnectToWiFiRequest
	connected      bool
	ssid           string
	prioritySSID   string
	priority       int32
	order          []string
	forgotten      string
	err            error
}

func (m *mockNet) ListWiFiNetworks(_ context.Context) ([]*agentpb.ListWiFiNetworksResponse_WiFiNetwork, error) {
	return m.networks, m.err
}
func (m *mockNet) ConnectToWiFi(_ context.Context, req *agentpb.ConnectToWiFiRequest) error {
	m.connectRequest = req
	return m.err
}
func (m *mockNet) GetWiFiStatus(_ context.Context) (bool, string, error) {
	return m.connected, m.ssid, m.err
}
func (m *mockNet) DisconnectWiFi(_ context.Context) error { return m.err }
func (m *mockNet) ListKnownWiFiNetworks(_ context.Context) ([]*agentpb.ListKnownWiFiNetworksResponse_KnownWiFiNetwork, error) {
	return m.knownNetworks, m.err
}
func (m *mockNet) SetWiFiNetworkPriority(_ context.Context, ssid string, priority int32) error {
	m.prioritySSID, m.priority = ssid, priority
	return m.err
}
func (m *mockNet) ReorderKnownWiFiNetworks(_ context.Context, order []string) error {
	m.order = order
	return m.err
}
func (m *mockNet) ForgetWiFiNetwork(_ context.Context, ssid string) error {
	m.forgotten = ssid
	return m.err
}

type mockContainer struct {
	containers []*agentpb.AppContainer
	err        error
	stopped    string
	deleted    string
}

func (m *mockContainer) ListContainers(_ context.Context) ([]*agentpb.AppContainer, error) {
	return m.containers, m.err
}
func (m *mockContainer) StopContainer(_ context.Context, name string) error {
	m.stopped = name
	return m.err
}
func (m *mockContainer) DeleteContainer(_ context.Context, name string, _ bool) error {
	m.deleted = name
	return m.err
}

type mockHardware struct {
	caps []*agentpb.ListHardwareCapabilitiesResponse_HardwareCapability
	err  error
}

func (m *mockHardware) Discover(_ context.Context, _ string) ([]*agentpb.ListHardwareCapabilitiesResponse_HardwareCapability, error) {
	return m.caps, m.err
}

type mockBluetooth struct {
	peripherals  []*agentpb.DiscoveredBluetoothPeripheral
	err          error
	connected    string
	disconnected string
	forgotten    string
}

func (m *mockBluetooth) Scan(_ context.Context) (<-chan []*agentpb.DiscoveredBluetoothPeripheral, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan []*agentpb.DiscoveredBluetoothPeripheral, 1)
	if len(m.peripherals) > 0 {
		ch <- m.peripherals
	}
	close(ch)
	return ch, nil
}
func (m *mockBluetooth) Connect(_ context.Context, addr string, _, _ bool) (bool, error) {
	m.connected = addr
	return m.err == nil, m.err
}
func (m *mockBluetooth) Disconnect(_ context.Context, addr string) error {
	m.disconnected = addr
	return m.err
}
func (m *mockBluetooth) Forget(_ context.Context, addr string) error {
	m.forgotten = addr
	return m.err
}

func newTestDispatcher(net *mockNet, ctr *mockContainer, hw *mockHardware, bt *mockBluetooth) *bluetooth.Dispatcher {
	return bluetooth.NewDispatcher(net, ctr, hw, bt)
}

// ── WiFi tests ────────────────────────────────────────────────────────────────

func TestDispatch_WifiList(t *testing.T) {
	net := &mockNet{networks: []*agentpb.ListWiFiNetworksResponse_WiFiNetwork{
		{
			Ssid:           "MyNet",
			SignalStrength: proto.Int32(81),
			Security:       agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE,
			IsKnown:        true,
			IsConnected:    true,
			Priority:       proto.Int32(0),
			RssiDbm:        proto.Int32(-56),
		},
		{Ssid: "NoSignal"},
	}}
	d := newTestDispatcher(net, nil, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiList{WifiList: &agentpb.WifiListCommand{}},
	})
	wl := resp.GetWifiList()
	if wl == nil {
		t.Fatal("expected WifiListResponse, got nil")
	}
	want := &agentpb.WifiListResponse{Networks: []*agentpb.WifiNetworkInfo{
		{
			Ssid:           "MyNet",
			SignalStrength: proto.Int32(81),
			Security:       agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE,
			IsKnown:        true,
			IsConnected:    true,
			Priority:       proto.Int32(0),
			RssiDbm:        proto.Int32(-56),
		},
		{Ssid: "NoSignal"},
	}}
	if !proto.Equal(wl, want) {
		t.Errorf("WiFi scan metadata = %v, want %v", wl, want)
	}
}

func TestDispatch_WifiList_NilManager(t *testing.T) {
	d := newTestDispatcher(nil, nil, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiList{WifiList: &agentpb.WifiListCommand{}},
	})
	if resp.GetError() == nil {
		t.Fatal("expected error response for nil network manager")
	}
}

func TestDispatch_WifiConnect(t *testing.T) {
	for _, tt := range []struct {
		name     string
		security *agentpb.WiFiSecurityType
		hidden   *bool
	}{
		{name: "autodetect"},
		{name: "hidden WPA3", security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE.Enum(), hidden: proto.Bool(true)},
		{name: "visible open", security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN.Enum(), hidden: proto.Bool(false)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			net := &mockNet{}
			d := newTestDispatcher(net, nil, nil, nil)
			resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
				Command: &agentpb.BluetoothCommand_WifiConnect{
					WifiConnect: &agentpb.WifiConnectCommand{Ssid: "net", Password: "pass", Security: tt.security, Hidden: tt.hidden},
				},
			})
			if !resp.GetWifiConnect().GetSuccess() {
				t.Fatalf("expected successful WifiConnectResponse, got %v", resp)
			}
			want := &agentpb.ConnectToWiFiRequest{Ssid: "net", Password: "pass", Security: tt.security, Hidden: tt.hidden}
			if !proto.Equal(net.connectRequest, want) {
				t.Errorf("WiFi connect options = %v, want %v", net.connectRequest, want)
			}
		})
	}
}

func TestDispatch_WifiStatus(t *testing.T) {
	net := &mockNet{connected: true, ssid: "HomeNet"}
	d := newTestDispatcher(net, nil, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiStatus{WifiStatus: &agentpb.WifiStatusCommand{}},
	})
	ws := resp.GetWifiStatus()
	if ws == nil {
		t.Fatal("expected WifiStatusResponse")
	}
	if !ws.GetConnected() || ws.GetSsid() != "HomeNet" {
		t.Errorf("unexpected status: connected=%v ssid=%q", ws.GetConnected(), ws.GetSsid())
	}
}

func TestDispatch_WifiDisconnect(t *testing.T) {
	net := &mockNet{}
	d := newTestDispatcher(net, nil, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiDisconnect{WifiDisconnect: &agentpb.WifiDisconnectCommand{}},
	})
	if resp.GetWifiDisconnect() == nil {
		t.Fatal("expected WifiDisconnectResponse")
	}
	if !resp.GetWifiDisconnect().GetSuccess() {
		t.Error("expected success=true")
	}
}

func TestDispatch_WifiKnownList(t *testing.T) {
	net := &mockNet{knownNetworks: []*agentpb.ListKnownWiFiNetworksResponse_KnownWiFiNetwork{
		{Ssid: "Home", Uuid: "home-profile", Priority: 10, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE},
		{Ssid: "Guest", Uuid: "guest-profile", Priority: -1, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN},
	}}
	resp := newTestDispatcher(net, nil, nil, nil).Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiKnownList{WifiKnownList: &agentpb.WifiKnownListCommand{}},
	})
	want := &agentpb.WifiKnownListResponse{Networks: []*agentpb.KnownWifiNetworkInfo{
		{Ssid: "Home", Uuid: "home-profile", Priority: 10, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA3_SAE},
		{Ssid: "Guest", Uuid: "guest-profile", Priority: -1, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN},
	}}
	if !proto.Equal(resp.GetWifiKnownList(), want) {
		t.Errorf("known WiFi profiles = %v, want %v", resp, want)
	}
}

func TestDispatch_WifiSetPriority(t *testing.T) {
	net := &mockNet{}
	resp := newTestDispatcher(net, nil, nil, nil).Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiSetPriority{
			WifiSetPriority: &agentpb.WifiSetPriorityCommand{Ssid: "Home", Priority: -10},
		},
	})
	if !resp.GetWifiSetPriority().GetSuccess() || net.prioritySSID != "Home" || net.priority != -10 {
		t.Errorf("set priority: response=%v SSID=%q priority=%d", resp, net.prioritySSID, net.priority)
	}
}

func TestDispatch_WifiReorder(t *testing.T) {
	net := &mockNet{}
	order := []string{"Home", "Guest"}
	resp := newTestDispatcher(net, nil, nil, nil).Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiReorder{WifiReorder: &agentpb.WifiReorderCommand{OrderSsids: order}},
	})
	if !resp.GetWifiReorder().GetSuccess() || !slices.Equal(net.order, order) {
		t.Errorf("reorder: response=%v order=%v", resp, net.order)
	}
}

func TestDispatch_WifiForget(t *testing.T) {
	net := &mockNet{}
	resp := newTestDispatcher(net, nil, nil, nil).Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiForget{WifiForget: &agentpb.WifiForgetCommand{Ssid: "Guest"}},
	})
	if !resp.GetWifiForget().GetSuccess() || net.forgotten != "Guest" {
		t.Errorf("forget: response=%v SSID=%q", resp, net.forgotten)
	}
}

func TestDispatch_WifiErrors(t *testing.T) {
	commands := map[string]*agentpb.BluetoothCommand{
		"list":         {Command: &agentpb.BluetoothCommand_WifiList{WifiList: &agentpb.WifiListCommand{}}},
		"connect":      {Command: &agentpb.BluetoothCommand_WifiConnect{WifiConnect: &agentpb.WifiConnectCommand{Ssid: "Home"}}},
		"status":       {Command: &agentpb.BluetoothCommand_WifiStatus{WifiStatus: &agentpb.WifiStatusCommand{}}},
		"disconnect":   {Command: &agentpb.BluetoothCommand_WifiDisconnect{WifiDisconnect: &agentpb.WifiDisconnectCommand{}}},
		"known list":   {Command: &agentpb.BluetoothCommand_WifiKnownList{WifiKnownList: &agentpb.WifiKnownListCommand{}}},
		"set priority": {Command: &agentpb.BluetoothCommand_WifiSetPriority{WifiSetPriority: &agentpb.WifiSetPriorityCommand{Ssid: "Home", Priority: 5}}},
		"reorder":      {Command: &agentpb.BluetoothCommand_WifiReorder{WifiReorder: &agentpb.WifiReorderCommand{OrderSsids: []string{"Home"}}}},
		"forget":       {Command: &agentpb.BluetoothCommand_WifiForget{WifiForget: &agentpb.WifiForgetCommand{Ssid: "Home"}}},
	}
	for name, cmd := range commands {
		t.Run(name, func(t *testing.T) {
			for _, tt := range []struct {
				name string
				net  *mockNet
				want string
			}{
				{name: "unavailable", want: "network manager unavailable"},
				{name: "operation failed", net: &mockNet{err: errors.New("WiFi operation failed")}, want: "WiFi operation failed"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					resp := newTestDispatcher(tt.net, nil, nil, nil).Dispatch(context.Background(), cmd)
					if resp.GetError().GetMessage() != tt.want {
						t.Errorf("response = %v, want error %q", resp, tt.want)
					}
				})
			}
		})
	}
}

// ── Apps tests ────────────────────────────────────────────────────────────────

func TestDispatch_AppsList(t *testing.T) {
	ctr := &mockContainer{containers: []*agentpb.AppContainer{
		{AppName: "myapp", AppVersion: "1.0"},
	}}
	d := newTestDispatcher(nil, ctr, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsList{AppsList: &agentpb.AppsListCommand{}},
	})
	al := resp.GetAppsList()
	if al == nil {
		t.Fatal("expected AppsListResponse")
	}
	if len(al.GetApps()) != 1 || al.GetApps()[0].GetAppName() != "myapp" {
		t.Errorf("unexpected apps: %v", al.GetApps())
	}
}

func TestDispatch_AppsStop(t *testing.T) {
	ctr := &mockContainer{}
	d := newTestDispatcher(nil, ctr, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsStop{AppsStop: &agentpb.AppsStopCommand{AppName: "myapp"}},
	})
	if resp.GetAppsStop() == nil {
		t.Fatal("expected AppsStopResponse")
	}
	if !resp.GetAppsStop().GetSuccess() {
		t.Error("expected success=true")
	}
	if ctr.stopped != "myapp" {
		t.Errorf("expected stopped=myapp, got %q", ctr.stopped)
	}
}

func TestDispatch_AppsRemove(t *testing.T) {
	ctr := &mockContainer{}
	d := newTestDispatcher(nil, ctr, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsRemove{
			AppsRemove: &agentpb.AppsRemoveCommand{AppName: "myapp", PurgeImage: true},
		},
	})
	if resp.GetAppsRemove() == nil {
		t.Fatal("expected AppsRemoveResponse")
	}
	if !resp.GetAppsRemove().GetSuccess() {
		t.Error("expected success=true")
	}
	if ctr.deleted != "myapp" {
		t.Errorf("expected deleted=myapp, got %q", ctr.deleted)
	}
}

// ── Agent version ──────────────────────────────────────────────────────────────

func TestDispatch_AgentVersion(t *testing.T) {
	d := newTestDispatcher(nil, nil, nil, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AgentVersion{AgentVersion: &agentpb.AgentVersionCommand{}},
	})
	av := resp.GetAgentVersion()
	if av == nil {
		t.Fatal("expected AgentVersionResponse")
	}
	if av.GetVersion() == "" {
		t.Error("expected non-empty version")
	}
}

// ── Hardware ───────────────────────────────────────────────────────────────────

func TestDispatch_HardwareList(t *testing.T) {
	hw := &mockHardware{caps: []*agentpb.ListHardwareCapabilitiesResponse_HardwareCapability{
		{Category: "gpu", Description: "NVIDIA GPU"},
	}}
	d := newTestDispatcher(nil, nil, hw, nil)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_HardwareList{HardwareList: &agentpb.HardwareListCommand{}},
	})
	hl := resp.GetHardwareList()
	if hl == nil {
		t.Fatal("expected HardwareListResponse")
	}
	if len(hl.GetCapabilities()) != 1 || hl.GetCapabilities()[0].GetType() != "gpu" {
		t.Errorf("unexpected capabilities: %v", hl.GetCapabilities())
	}
}

// ── Bluetooth ──────────────────────────────────────────────────────────────────

func TestDispatch_BluetoothList(t *testing.T) {
	bt := &mockBluetooth{peripherals: []*agentpb.DiscoveredBluetoothPeripheral{
		{Name: "Headphones", Address: "AA:BB:CC:DD:EE:FF"},
	}}
	d := newTestDispatcher(nil, nil, nil, bt)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_BluetoothList{BluetoothList: &agentpb.BluetoothListCommand{}},
	})
	bl := resp.GetBluetoothList()
	if bl == nil {
		t.Fatal("expected BluetoothListResponse")
	}
	if len(bl.GetDevices()) != 1 || bl.GetDevices()[0].GetName() != "Headphones" {
		t.Errorf("unexpected devices: %v", bl.GetDevices())
	}
}

func TestDispatch_BluetoothConnect(t *testing.T) {
	bt := &mockBluetooth{}
	d := newTestDispatcher(nil, nil, nil, bt)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_BluetoothConnect{
			BluetoothConnect: &agentpb.BluetoothConnectCommand{Address: "AA:BB:CC:DD:EE:FF"},
		},
	})
	if resp.GetBluetoothConnect() == nil {
		t.Fatal("expected BluetoothConnectResponse")
	}
	if !resp.GetBluetoothConnect().GetSuccess() {
		t.Error("expected success=true")
	}
	if bt.connected != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("expected connected addr, got %q", bt.connected)
	}
}

func TestDispatch_BluetoothDisconnect(t *testing.T) {
	bt := &mockBluetooth{}
	d := newTestDispatcher(nil, nil, nil, bt)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_BluetoothDisconnect{
			BluetoothDisconnect: &agentpb.BluetoothDisconnectCommand{Address: "AA:BB:CC:DD:EE:FF"},
		},
	})
	if resp.GetBluetoothDisconnect() == nil {
		t.Fatal("expected BluetoothDisconnectResponse")
	}
	if !resp.GetBluetoothDisconnect().GetSuccess() {
		t.Error("expected success=true")
	}
	if bt.disconnected != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("expected disconnected addr, got %q", bt.disconnected)
	}
}

func TestDispatch_BluetoothForget(t *testing.T) {
	bt := &mockBluetooth{}
	d := newTestDispatcher(nil, nil, nil, bt)
	resp := d.Dispatch(context.Background(), &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_BluetoothForget{
			BluetoothForget: &agentpb.BluetoothForgetCommand{Address: "AA:BB:CC:DD:EE:FF"},
		},
	})
	if resp.GetBluetoothForget() == nil {
		t.Fatal("expected BluetoothForgetResponse")
	}
	if !resp.GetBluetoothForget().GetSuccess() {
		t.Error("expected success=true")
	}
	if bt.forgotten != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("expected forgotten addr, got %q", bt.forgotten)
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	d := newTestDispatcher(nil, nil, nil, nil)
	for _, cmd := range []*agentpb.BluetoothCommand{nil, {}} {
		resp := d.Dispatch(context.Background(), cmd)
		if resp.GetError() == nil {
			t.Fatal("expected error response for unknown command")
		}
	}
}
