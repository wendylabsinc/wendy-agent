// Package ble is the Wendy protocol layer over the generic BLE central in
// internal/shared/ble: the WendyOS agent's RPC over mTLS-over-L2CAP, its TLS
// configuration, and Wendy Lite Wi-Fi provisioning over GATT. Every Wendy UUID,
// PSM and framing rule the CLI needs lives here; internal/shared/ble/central
// and .../scan know none of it.
//
// The Lite info service is the exception — it sits in internal/shared/ble,
// which is also named ble, because internal/shared/discovery needs it. No file
// currently imports both; a future one must alias.
package ble

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/ble/central"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/protobuf/proto"
)

const (
	agentCommandTimeout   = 60 * time.Second
	agentHandshakeTimeout = 15 * time.Second
	maxAgentFrameSize     = 65535
)

// DefaultL2CAPPSM is the fallback PSM for agents without discovered metadata.
const DefaultL2CAPPSM uint16 = 128

// AgentClient exchanges sequential protobuf frames over an authenticated BLE
// stream. A failed exchange closes the stream, since it cannot safely associate
// a delayed response with the next command.
type AgentClient struct {
	conn      net.Conn
	tlsConn   *tls.Conn
	ctx       context.Context
	exchange  chan struct{}
	closeOnce sync.Once
	identity  certs.WendyIdentity
}

func ConnectAgent(device *models.BluetoothDevice, tlsConfig *tls.Config) (*AgentClient, error) {
	return ConnectAgentContext(context.Background(), device, tlsConfig)
}

// ConnectAgentContext bounds the link setup and TLS handshake. The context also
// owns subsequent commands until Close, including writes stalled on BLE credits.
func ConnectAgentContext(ctx context.Context, device *models.BluetoothDevice, tlsConfig *tls.Config) (*AgentClient, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := central.Connect(device.Address, 10)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", device.DisplayName, err)
	}
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	psm := DefaultL2CAPPSM
	if device.L2CAPPSM != 0 {
		psm = device.L2CAPPSM
	}
	if err := conn.OpenL2CAP(psm, 10); err != nil {
		conn.Close()
		return nil, fmt.Errorf("opening L2CAP channel (PSM %d): %w", psm, err)
	}
	return connectAgentStream(ctx, central.NewL2CAPStream(conn), tlsConfig)
}

// connectAgentStream keeps TLS and framing testable without a radio.
func connectAgentStream(ctx context.Context, stream net.Conn, tlsConfig *tls.Config) (*AgentClient, error) {
	client := &AgentClient{conn: stream, ctx: ctx, exchange: make(chan struct{}, 1)}
	// Sending a TLS rejection alert can itself stall on BLE. Retain the
	// verifier's identity refusal even if cancellation interrupts that alert.
	var verificationErr error
	tlsConfig = tlsConfig.Clone()
	if verify := tlsConfig.VerifyConnection; verify != nil {
		tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
			verificationErr = verify(state)
			return verificationErr
		}
	}
	client.tlsConn = tls.Client(stream, tlsConfig)
	handshakeCtx, cancel := context.WithTimeout(ctx, agentHandshakeTimeout)
	defer cancel()
	stop := context.AfterFunc(handshakeCtx, client.Close)
	defer stop()
	deadline, _ := handshakeCtx.Deadline()
	if err := stream.SetDeadline(deadline); err != nil {
		client.Close()
		return nil, err
	}
	if err := client.tlsConn.HandshakeContext(handshakeCtx); err != nil {
		client.Close()
		if verificationErr != nil {
			err = verificationErr
		}
		return nil, fmt.Errorf("BLE mTLS handshake: %w", err)
	}
	if err := handshakeCtx.Err(); err != nil {
		client.Close()
		return nil, err
	}
	// Inspect identity only after the configured verifier and handshake accept it.
	state := client.tlsConn.ConnectionState()
	if len(state.PeerCertificates) > 0 {
		client.identity, _, _ = certs.IdentityFromCert(state.PeerCertificates[0])
	}
	if client.identity.EntityType != certs.EntityAsset {
		client.Close()
		return nil, fmt.Errorf("BLE agent certificate has no device identity")
	}
	if err := stream.SetDeadline(time.Time{}); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// ObservedServerIdentity returns the device identity accepted by the TLS verifier.
func (c *AgentClient) ObservedServerIdentity() (certs.WendyIdentity, bool) {
	return c.identity, c.identity.EntityType == certs.EntityAsset
}

// Close closes the transport directly. TLS close_notify can itself block when a
// peripheral has stopped accepting data, so it is inappropriate on this path.
func (c *AgentClient) Close() { c.closeOnce.Do(func() { _ = c.conn.Close() }) }

func (c *AgentClient) sendCommand(cmd *agentpb.BluetoothCommand) (*agentpb.BluetoothResponse, error) {
	return c.sendCommandContext(c.ctx, cmd)
}

func (c *AgentClient) sendCommandContext(ctx context.Context, cmd *agentpb.BluetoothCommand) (resp *agentpb.BluetoothResponse, err error) {
	ctx, cancel := context.WithTimeout(ctx, agentCommandTimeout)
	defer cancel()
	select {
	case c.exchange <- struct{}{}:
		defer func() { <-c.exchange }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := proto.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshaling command: %w", err)
	}
	if len(data) == 0 || len(data) > maxAgentFrameSize {
		return nil, fmt.Errorf("BLE command size %d outside 1..%d", len(data), maxAgentFrameSize)
	}
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	exchangeComplete := false
	defer func() {
		if err != nil && !exchangeComplete {
			c.Close()
		}
		if ctx.Err() != nil {
			resp, err = nil, ctx.Err()
		} else if deadline, ok := ctx.Deadline(); err != nil && ok && !time.Now().Before(deadline) {
			// A socket deadline can fire before the context timer is scheduled.
			// Both represent the same exhausted operation budget.
			resp, err = nil, context.DeadlineExceeded
		}
	}()
	deadline, _ := ctx.Deadline()
	if err = c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	defer c.conn.SetDeadline(time.Time{}) //nolint:errcheck
	frame := make([]byte, 2+len(data))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(data)))
	copy(frame[2:], data)
	if _, err = c.tlsConn.Write(frame); err != nil {
		return nil, fmt.Errorf("sending command: %w", err)
	}
	var header [2]byte
	if _, err = io.ReadFull(c.tlsConn, header[:]); err != nil {
		return nil, fmt.Errorf("reading response header: %w", err)
	}
	msgLen := binary.BigEndian.Uint16(header[:])
	if msgLen == 0 {
		return nil, fmt.Errorf("empty BLE response frame")
	}
	body := make([]byte, msgLen)
	if _, err = io.ReadFull(c.tlsConn, body); err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	resp = &agentpb.BluetoothResponse{}
	if err = proto.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}
	if resp.Response == nil {
		return nil, fmt.Errorf("BLE response contains no result")
	}
	// Application errors complete the exchange and leave the stream usable,
	// for example when a user retries WiFi with a corrected password.
	exchangeComplete = true
	if errResp := resp.GetError(); errResp != nil {
		return nil, fmt.Errorf("agent error: %s", errResp.GetMessage())
	}
	return resp, nil
}

// WifiConnect sends a WiFi connect command over BLE.
func (c *AgentClient) WifiConnect(ssid, password string) error {
	return c.WifiConnectWith(ssid, password, agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_UNSPECIFIED, false)
}

// WifiConnectWith sends a WiFi connect command over BLE with optional security
// hint and hidden-network flag.
func (c *AgentClient) WifiConnectWith(ssid, password string, security agentpb.WiFiSecurityType, hidden bool) error {
	inner := &agentpb.WifiConnectCommand{
		Ssid:     ssid,
		Password: password,
	}
	if security != agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_UNSPECIFIED {
		s := security
		inner.Security = &s
	}
	if hidden {
		h := true
		inner.Hidden = &h
	}
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiConnect{
			WifiConnect: inner,
		},
	}

	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}

	wifiResp := resp.GetWifiConnect()
	if wifiResp == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !wifiResp.GetSuccess() {
		msg := wifiResp.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("WiFi connect failed: %s", msg)
	}

	return nil
}

// WifiList lists available WiFi networks over BLE.
func (c *AgentClient) WifiList() ([]*agentpb.WifiNetworkInfo, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiList{
			WifiList: &agentpb.WifiListCommand{},
		},
	}

	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}

	wifiResp := resp.GetWifiList()
	if wifiResp == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return wifiResp.GetNetworks(), nil
}

func (c *AgentClient) WifiStatus() (*agentpb.WifiStatusResponse, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiStatus{
			WifiStatus: &agentpb.WifiStatusCommand{},
		},
	}

	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}

	wifiResp := resp.GetWifiStatus()
	if wifiResp == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return wifiResp, nil
}

// WifiDisconnect disconnects from the current WiFi network over BLE.
func (c *AgentClient) WifiDisconnect() error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiDisconnect{
			WifiDisconnect: &agentpb.WifiDisconnectCommand{},
		},
	}

	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}

	wifiResp := resp.GetWifiDisconnect()
	if wifiResp == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !wifiResp.GetSuccess() {
		msg := wifiResp.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("WiFi disconnect failed: %s", msg)
	}
	return nil
}

// WifiKnownList lists saved WiFi profiles on the device over BLE.
func (c *AgentClient) WifiKnownList() ([]*agentpb.KnownWifiNetworkInfo, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiKnownList{
			WifiKnownList: &agentpb.WifiKnownListCommand{},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}
	inner := resp.GetWifiKnownList()
	if inner == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return inner.GetNetworks(), nil
}

// WifiSetPriority sets the priority for a saved network by SSID.
func (c *AgentClient) WifiSetPriority(ssid string, priority int32) error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiSetPriority{
			WifiSetPriority: &agentpb.WifiSetPriorityCommand{
				Ssid:     ssid,
				Priority: priority,
			},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}
	inner := resp.GetWifiSetPriority()
	if inner == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !inner.GetSuccess() {
		msg := inner.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("WiFi set priority failed: %s", msg)
	}
	return nil
}

// WifiReorder reorders saved networks by SSID.
func (c *AgentClient) WifiReorder(order []string) error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiReorder{
			WifiReorder: &agentpb.WifiReorderCommand{OrderSsids: order},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}
	inner := resp.GetWifiReorder()
	if inner == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !inner.GetSuccess() {
		msg := inner.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("WiFi reorder failed: %s", msg)
	}
	return nil
}

func (c *AgentClient) AgentVersion() (*agentpb.AgentVersionResponse, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AgentVersion{
			AgentVersion: &agentpb.AgentVersionCommand{},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}
	inner := resp.GetAgentVersion()
	if inner == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return inner, nil
}

func (c *AgentClient) AppsList() ([]*agentpb.AppInfo, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsList{
			AppsList: &agentpb.AppsListCommand{},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}
	inner := resp.GetAppsList()
	if inner == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return inner.GetApps(), nil
}

// AppsStop stops an app by name over BLE.
func (c *AgentClient) AppsStop(appName string) error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsStop{
			AppsStop: &agentpb.AppsStopCommand{AppName: appName},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}
	inner := resp.GetAppsStop()
	if inner == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !inner.GetSuccess() {
		msg := inner.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("apps stop failed: %s", msg)
	}
	return nil
}

// AppsRemove removes an app over BLE. purgeImage removes the container image.
func (c *AgentClient) AppsRemove(appName string, purgeImage bool) error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_AppsRemove{
			AppsRemove: &agentpb.AppsRemoveCommand{
				AppName:    appName,
				PurgeImage: purgeImage,
			},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}
	inner := resp.GetAppsRemove()
	if inner == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !inner.GetSuccess() {
		msg := inner.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("apps remove failed: %s", msg)
	}
	return nil
}

// HardwareList returns hardware capabilities over BLE.
func (c *AgentClient) HardwareList() ([]*agentpb.HardwareInfo, error) {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_HardwareList{
			HardwareList: &agentpb.HardwareListCommand{},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return nil, err
	}
	inner := resp.GetHardwareList()
	if inner == nil {
		return nil, fmt.Errorf("unexpected response type")
	}
	return inner.GetCapabilities(), nil
}

// WifiForget removes a saved WiFi profile by SSID.
func (c *AgentClient) WifiForget(ssid string) error {
	cmd := &agentpb.BluetoothCommand{
		Command: &agentpb.BluetoothCommand_WifiForget{
			WifiForget: &agentpb.WifiForgetCommand{Ssid: ssid},
		},
	}
	resp, err := c.sendCommand(cmd)
	if err != nil {
		return err
	}
	inner := resp.GetWifiForget()
	if inner == nil {
		return fmt.Errorf("unexpected response type")
	}
	if !inner.GetSuccess() {
		msg := inner.GetErrorMessage()
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("WiFi forget failed: %s", msg)
	}
	return nil
}
