//go:build linux

package bluetooth

import (
	"fmt"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"
)

const (
	agentObjectPath   = dbus.ObjectPath("/org/wendy/btagent")
	agentManagerIface = "org.bluez.AgentManager1"
	agentIface        = "org.bluez.Agent1"
	// agentCapability "NoInputNoOutput" selects "just works" pairing: BlueZ
	// completes pairing without prompting for a PIN or passkey, which is the
	// only viable mode on a headless device.
	agentCapability = "NoInputNoOutput"
)

// errRejected is returned for agent callbacks that should never be reached under
// NoInputNoOutput capability (PIN/passkey entry). Returning a BlueZ-recognised
// rejection lets the pairing attempt fail cleanly rather than hang.
var errRejected = dbus.NewError("org.bluez.Error.Rejected", nil)

// pairingAgent implements org.bluez.Agent1 as a headless agent for a single
// caller-initiated pairing. It auto-accepts pairing and service authorization
// for the one device being paired (expected) and rejects requests from any
// other device, so a nearby device cannot get itself bonded during the brief
// window the agent is registered.
type pairingAgent struct {
	logger   *zap.Logger
	expected dbus.ObjectPath
}

// accept auto-accepts an authorization callback when it is for the device this
// agent was registered to pair (expected), and rejects it otherwise. Both
// outcomes are logged at a level high enough to survive a production INFO log
// configuration, giving an audit trail of established device relationships.
func (a *pairingAgent) accept(device dbus.ObjectPath, action string, extra ...zap.Field) *dbus.Error {
	fields := append([]zap.Field{zap.String("device", string(device))}, extra...)
	if device != a.expected {
		a.logger.Warn("Rejecting Bluetooth "+action+" from unexpected device",
			append(fields, zap.String("expected", string(a.expected)))...)
		return errRejected
	}
	a.logger.Info("Auto-accepting Bluetooth "+action, fields...)
	return nil
}

// Release is called by BlueZ when the agent is unregistered.
func (a *pairingAgent) Release() *dbus.Error { return nil }

// RequestPinCode / RequestPasskey are only invoked for capabilities that can
// supply credentials. Under NoInputNoOutput they should not occur; reject them.
func (a *pairingAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	return "", errRejected
}

func (a *pairingAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, errRejected
}

// Display* callbacks are no-ops: a headless device has nothing to display.
func (a *pairingAgent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error {
	return nil
}

func (a *pairingAgent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}

// RequestConfirmation / RequestAuthorization / AuthorizeService complete "just
// works" pairing and service connections without prompting, but only for the
// device this agent was registered to pair (see accept).
func (a *pairingAgent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	return a.accept(device, "pairing confirmation", zap.Uint32("passkey", passkey))
}

func (a *pairingAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	return a.accept(device, "pairing authorization")
}

func (a *pairingAgent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	return a.accept(device, "service authorization", zap.String("uuid", uuid))
}

// Cancel is called by BlueZ when a request is cancelled (e.g. the remote aborts).
func (a *pairingAgent) Cancel() *dbus.Error { return nil }

// registerPairingAgent exports a headless pairing agent on conn and registers it
// with BlueZ as the default agent. The agent only auto-accepts pairing for
// expected (the device being paired). It stays registered for the lifetime of
// conn; BlueZ unregisters it automatically when conn closes.
func registerPairingAgent(conn *dbus.Conn, logger *zap.Logger, expected dbus.ObjectPath) error {
	agent := &pairingAgent{logger: logger, expected: expected}
	if err := conn.Export(agent, agentObjectPath, agentIface); err != nil {
		return fmt.Errorf("exporting pairing agent: %w", err)
	}

	mgr := conn.Object(bluezService, "/org/bluez")
	if call := mgr.Call(agentManagerIface+".RegisterAgent", 0, agentObjectPath, agentCapability); call.Err != nil {
		return fmt.Errorf("registering pairing agent: %w", call.Err)
	}
	// Become the default agent so BlueZ routes authentication requests here even
	// when other agents (e.g. a stray bluetoothctl) are present.
	if call := mgr.Call(agentManagerIface+".RequestDefaultAgent", 0, agentObjectPath); call.Err != nil {
		return fmt.Errorf("requesting default pairing agent: %w", call.Err)
	}

	return nil
}
