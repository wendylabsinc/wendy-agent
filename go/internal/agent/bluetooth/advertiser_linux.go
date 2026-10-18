//go:build linux

package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"go.uber.org/zap"
)

const (
	wendyServiceUUID     = agentinfo.ServiceUUID
	advObjectPath        = dbus.ObjectPath("/org/wendy/advertisement0")
	bluezService         = "org.bluez"
	advManagerIface      = "org.bluez.LEAdvertisingManager1"
	advIface             = "org.bluez.LEAdvertisement1"
	bleDBusTimeout       = 5 * time.Second
	bleRegisterTimeout   = 30 * time.Second
	bleRegisterRetryWait = time.Second
)

func advertisingAdapterPath(ctx context.Context, conn *dbus.Conn) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, bleDBusTimeout)
	defer cancel()
	managed, err := getManagedObjects(ctx, conn)
	if err != nil {
		return "", err
	}
	return resolveAdvertisingAdapter(managed)
}

func resolveAdvertisingAdapter(managed managedObjects) (string, error) {
	if p := os.Getenv("WENDY_BT_ADAPTER"); p != "" {
		path := dbus.ObjectPath(p)
		if !path.IsValid() || findAdvertisingAdapter(managedObjects{path: managed[path]}) == "" {
			return "", fmt.Errorf("Bluetooth adapter %q must implement %s and %s", p, advManagerIface, gattManagerIface)
		}
		return p, nil
	}
	if p := findAdvertisingAdapter(managed); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no Bluetooth adapter implements both %s and %s", advManagerIface, gattManagerIface)
}

// findAdapterByInterface selects a stable adapter despite map iteration order.
func findAdapterByInterface(managed managedObjects, iface string) string {
	var best string
	for path, ifaces := range managed {
		if _, ok := ifaces[iface]; !ok {
			continue
		}
		if s := string(path); best == "" || s < best {
			best = s
		}
	}
	return best
}

// Discovery and its GATT metadata must be available on the same controller.
func findAdvertisingAdapter(managed managedObjects) string {
	eligible := make(managedObjects)
	for path, ifaces := range managed {
		if _, ok := ifaces[gattManagerIface]; ok {
			eligible[path] = ifaces
		}
	}
	return findAdapterByInterface(eligible, advManagerIface)
}

type advertisement struct{}

func (*advertisement) Release() *dbus.Error { return nil }

func startAdvertising(ctx context.Context, logger *zap.Logger, info agentinfo.Info) (func() error, error) {
	value, err := agentinfo.Encode(info)
	if err != nil {
		return nil, fmt.Errorf("encode agent BLE metadata: %w", err)
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	// Before registration starts every failure closes this private connection,
	// which also removes any exported objects or partially registered services.
	handedOff := false
	defer func() {
		if !handedOff {
			_ = conn.Close()
		}
	}()
	adapterPath, err := advertisingAdapterPath(ctx, conn)
	if err != nil {
		return nil, err
	}
	// Subscribe before registration so a restart during startup cannot leave
	// the endpoint registered only with the departed bluetoothd process.
	ownerChanges := make(chan *dbus.Signal, 16)
	conn.Signal(ownerChanges)
	watchCtx, watchCancel := context.WithTimeout(ctx, bleDBusTimeout)
	err = conn.AddMatchSignalContext(watchCtx,
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, bluezService))
	watchCancel()
	if err != nil {
		return nil, fmt.Errorf("watch bluetoothd restarts: %w", err)
	}
	if err := exportAgentInfo(conn, value); err != nil {
		return nil, err
	}
	if err := conn.Export(&advertisement{}, advObjectPath, advIface); err != nil {
		return nil, fmt.Errorf("export advertisement object: %w", err)
	}
	// Discoverable is omitted for compatibility with older BlueZ versions.
	props := prop.Map{
		advIface: {
			"Type":         {Value: "peripheral", Emit: prop.EmitFalse},
			"ServiceUUIDs": {Value: []string{wendyServiceUUID}, Emit: prop.EmitFalse},
			"LocalName":    {Value: info.Hostname, Emit: prop.EmitFalse},
		},
	}
	if _, err := prop.Export(conn, advObjectPath, props); err != nil {
		return nil, fmt.Errorf("export advertisement properties: %w", err)
	}
	powerCtx, cancel := context.WithTimeout(ctx, bleDBusTimeout)
	if err := powerOnAdapter(powerCtx, conn, adapterPath); err != nil {
		logger.Warn("BLE adapter power-on failed", zap.Error(err))
	}
	cancel()
	handedOff = true
	cleanup, err := registerBLEPeripheral(ctx, logger, conn.Object(bluezService, dbus.ObjectPath(adapterPath)), conn.Close, ownerChanges)
	if err != nil {
		return nil, err
	}
	logger.Info("BLE advertisement and agent info registered",
		zap.String("adapter", adapterPath), zap.String("uuid", wendyServiceUUID), zap.String("name", info.Hostname))
	return cleanup, nil
}

type bleRegistrationObject interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

// registerBLEPeripheral owns the bus once registration starts. Cleanup waits
// for unregistration to finish, so a restart cannot race the old application.
func registerBLEPeripheral(parent context.Context, logger *zap.Logger, hci bleRegistrationObject, closeBus func() error, ownerChanges <-chan *dbus.Signal) (func() error, error) {
	ctx, cancel := context.WithCancel(parent)
	appRegistered, advRegistered := false, false
	var registrationMu sync.Mutex
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			cancel()
			registrationMu.Lock()
			defer registrationMu.Unlock()
			if advRegistered {
				cleanupErr = errors.Join(cleanupErr, unregisterBLEObject(hci, advManagerIface+".UnregisterAdvertisement", advObjectPath))
			}
			if appRegistered {
				cleanupErr = errors.Join(cleanupErr, unregisterBLEObject(hci, gattManagerIface+".UnregisterApplication", gattApplicationPath))
			}
			cleanupErr = errors.Join(cleanupErr, closeBus())
		})
		return cleanupErr
	}
	register := func(restarting bool) error {
		registrationMu.Lock()
		defer registrationMu.Unlock()
		registerCtx, registerCancel := context.WithTimeout(ctx, bleRegisterTimeout)
		defer registerCancel()
		if err := registerBLEObject(registerCtx, logger, hci, gattManagerIface+".RegisterApplication", gattApplicationPath, restarting); err != nil {
			return err
		}
		appRegistered = true
		if err := registerBLEObject(registerCtx, logger, hci, advManagerIface+".RegisterAdvertisement", advObjectPath, restarting); err != nil {
			return err
		}
		advRegistered = true
		return nil
	}
	if err := register(false); err != nil {
		return nil, errors.Join(err, cleanup())
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		defer func() {
			if err := cleanup(); err != nil {
				logger.Warn("BLE advertisement cleanup failed", zap.Error(err))
			}
		}()
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case signal, ok := <-ownerChanges:
				if !ok {
					return
				}
				owner, changed := bluezOwnerChange(signal)
				if !changed {
					continue
				}
				// Registrations belong to the departed daemon. Clear their
				// state even when ownership changes without a vacant interval.
				registrationMu.Lock()
				appRegistered, advRegistered = false, false
				registrationMu.Unlock()
				if owner == "" {
					continue
				}
				if err := register(true); err != nil {
					if ctx.Err() == nil {
						logger.Warn("BLE re-registration after bluetoothd restart failed", zap.Error(err))
					}
					continue
				}
				logger.Info("BLE advertisement and agent info restored after bluetoothd restart")
			}
		}
	}()
	return func() error {
		err := cleanup()
		<-watcherDone
		return err
	}, nil
}

func bluezOwnerChange(signal *dbus.Signal) (string, bool) {
	if signal == nil || signal.Sender != "org.freedesktop.DBus" || signal.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(signal.Body) != 3 {
		return "", false
	}
	name, nameOK := signal.Body[0].(string)
	oldOwner, oldOK := signal.Body[1].(string)
	newOwner, newOK := signal.Body[2].(string)
	return newOwner, nameOK && oldOK && newOK && name == bluezService && oldOwner != newOwner
}

func registerBLEObject(ctx context.Context, logger *zap.Logger, hci bleRegistrationObject, method string, path dbus.ObjectPath, allowExisting bool) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		callCtx, cancel := context.WithTimeout(ctx, bleDBusTimeout)
		err := hci.CallWithContext(callCtx, method, 0, path, map[string]dbus.Variant{}).Err
		cancel()
		if err == nil {
			return nil
		}
		// A queued owner change may describe a restart that initial
		// registration already survived. Our private bus owns this path.
		if name, _, ok := dbusErrorInfo(err); allowExisting && ok && name == "org.bluez.Error.AlreadyExists" {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s: %w", method, err)
		}
		if attempt == 0 {
			logger.Debug("BLE registration attempt failed", zap.String("method", method), zap.Error(err))
		}
		timer := time.NewTimer(bleRegisterRetryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", method, errors.Join(ctx.Err(), err))
		case <-timer.C:
		}
	}
}

func unregisterBLEObject(hci bleRegistrationObject, method string, path dbus.ObjectPath) error {
	ctx, cancel := context.WithTimeout(context.Background(), bleDBusTimeout)
	defer cancel()
	if err := hci.CallWithContext(ctx, method, 0, path).Err; err != nil {
		if name, _, ok := dbusErrorInfo(err); ok && name == "org.bluez.Error.DoesNotExist" {
			return nil
		}
		return fmt.Errorf("%s: %w", method, err)
	}
	return nil
}
