package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

var wifiHandoffStreamFn = discovery.StreamLAN
var wifiHandoffDialFn = dialAgentLadder
var wifiHandoffIdentityFn = func(conn *grpcclient.AgentConnection) (certs.WendyIdentity, bool) {
	return conn.ObservedServerIdentity()
}

// Advertisement fields limit connection attempts. Only TLS proves identity.
func wifiHandoffCandidate(device models.LANDevice, hostname string, expected certs.WendyIdentity) bool {
	if hostname != "" && device.HostKey() == hostname {
		return true
	}
	if device.PrincipalURI != "" {
		identity, err := certs.ParsePrincipal(device.PrincipalURI)
		return err == nil && identity.SameEntity(expected)
	}
	return false
}

func waitForBLEAgentLAN(ctx context.Context, device *models.BluetoothDevice, expected certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
	if expected.EntityType != certs.EntityAsset || expected.EntityID == "" {
		return nil, fmt.Errorf("LAN handoff requires the authenticated BLE device identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream := wifiHandoffStreamFn(ctx, discovery.StreamOptions{})
	retry := time.NewTicker(2 * time.Second)
	defer retry.Stop()
	var candidates []models.LANDevice
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("LAN handoff: %w", lastErr)
			}
			return nil, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				return nil, fmt.Errorf("LAN discovery ended before the device was authenticated")
			}
			if event.Kind != discovery.LANFound && event.Kind != discovery.LANUpdated {
				continue
			}
			if !wifiHandoffCandidate(event.Device, device.HostKey(), expected) {
				continue
			}
			candidates = []models.LANDevice{event.Device}
		case <-retry.C:
		}
		for _, candidate := range candidates {
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 5*time.Second)
			addresses := lanDialCandidates(attemptCtx, candidate)
			if len(addresses) == 0 {
				attemptCancel()
				continue
			}
			// The identity comes from BLE TLS, never from GATT or mDNS.
			target := dialTarget{PinKey: blePinKey(device), PinnedKey: blePinKey(device), Addr: addresses[0], Candidates: addresses, Expected: &expected}
			conn, _, err := wifiHandoffDialFn(attemptCtx, target)
			attemptCancel()
			if err != nil {
				lastErr = err
				if errors.Is(err, errDeviceIdentityRefused) {
					return nil, err
				}
				continue
			}
			if conn == nil {
				lastErr = fmt.Errorf("LAN dial returned no connection")
				continue
			}
			identity, ok := wifiHandoffIdentityFn(conn)
			if !conn.IsMTLS || !ok || !identity.SameEntity(expected) {
				conn.Close()
				return nil, fmt.Errorf("LAN peer did not prove the authenticated BLE identity")
			}
			return conn, nil
		}
	}
}

func handoffWiFiBLE(ctx context.Context, client *wifiClient) error {
	if client.ble == nil {
		return nil
	}
	identity, ok := client.ble.ObservedServerIdentity()
	if !ok {
		return fmt.Errorf("BLE peer has no authenticated device identity")
	}
	conn, err := waitForBLEAgentLAN(ctx, client.target.Bluetooth, identity)
	if err != nil {
		return err
	}
	client.ble.Close()
	client.ble = nil
	client.agent = conn.AgentService
	client.target.Agent = conn
	client.target.Bluetooth = nil
	cliSuccess("Device identity verified over LAN at %s", conn.Addr)
	return nil
}
