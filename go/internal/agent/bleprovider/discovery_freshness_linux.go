//go:build linux

package bleprovider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

// GetManagedObjects retains ServiceData after an advertiser disappears. Only
// a discovery signal with the mesh ServiceData proves that an advertisement
// was heard recently. DuplicateData=true in our discovery filter makes BlueZ
// emit PropertiesChanged for each received ServiceData advertisement.
const advertisementFreshnessTTL = 15 * time.Second
const maxFreshAdvertisements = 256

type freshAdvertisement struct {
	data    []byte
	seen    time.Time
	pending bool
}

type advertisementFreshness struct {
	adapter           dbus.ObjectPath
	uuid, networkName string
	self              meshid.Device
	mu                sync.Mutex
	seen              map[dbus.ObjectPath]freshAdvertisement
}

func newAdvertisementFreshness(adapter dbus.ObjectPath, uuid, networkName string, self meshid.Device) *advertisementFreshness {
	return &advertisementFreshness{adapter: adapter, uuid: uuid, networkName: networkName, self: self, seen: make(map[dbus.ObjectPath]freshAdvertisement)}
}

func (f *advertisementFreshness) devicePath(path dbus.ObjectPath) bool {
	return strings.HasPrefix(string(path), string(f.adapter)+"/dev_")
}

func (f *advertisementFreshness) observe(path dbus.ObjectPath, props map[string]dbus.Variant, at time.Time) {
	if !f.devicePath(path) {
		return
	}
	value, present := props["ServiceData"]
	if !present {
		return // RSSI, Connected, and other cached device changes are not adverts.
	}
	data := serviceData(value)
	adv, valid := matchingServiceData(data, f.uuid, f.networkName)
	// BlueZ replaces Device1.ServiceData with the contents of each received
	// advertisement. A peer may also advertise another service concurrently;
	// that signal says nothing about whether its mesh advert has stopped.
	if !valid {
		for key := range data {
			if strings.EqualFold(key, f.uuid) {
				f.forget(path) // Same UUID now carries a different mesh/payload.
				return
			}
		}
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Limit churn between cache polls too. A rotating advertiser must not
	// allocate an unbounded set of radio addresses during the freshness TTL.
	for priorPath, entry := range f.seen {
		if at.Before(entry.seen) || at.Sub(entry.seen) >= advertisementFreshnessTTL {
			delete(f.seen, priorPath)
		}
	}
	if !mayDial(adv.DeviceHint, f.self) {
		delete(f.seen, path)
		return
	}
	if _, exists := f.seen[path]; !exists && len(f.seen) >= maxFreshAdvertisements {
		return
	}
	raw, _ := adv.MarshalBinary()
	f.seen[path] = freshAdvertisement{data: raw, seen: at, pending: true}
}

func (f *advertisementFreshness) handle(signal *dbus.Signal, at time.Time) {
	if signal == nil {
		return
	}
	switch signal.Name {
	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		if len(signal.Body) != 3 {
			return
		}
		iface, ok := signal.Body[0].(string)
		if !ok || iface != deviceInterface {
			return
		}
		props, ok := signal.Body[1].(map[string]dbus.Variant)
		if !ok {
			return
		}
		if _, ok := signal.Body[2].([]string); !ok {
			return
		}
		// A missing property is not proof the mesh advert stopped.
		f.observe(signal.Path, props, at)
	case "org.freedesktop.DBus.ObjectManager.InterfacesRemoved":
		if len(signal.Body) != 2 {
			return
		}
		path, ok := signal.Body[0].(dbus.ObjectPath)
		if !ok {
			return
		}
		interfaces, ok := signal.Body[1].([]string)
		if !ok {
			return
		}
		for _, iface := range interfaces {
			if iface == deviceInterface {
				f.forget(path)
				return
			}
		}
	}
}

func (f *advertisementFreshness) forget(path dbus.ObjectPath) {
	f.mu.Lock()
	delete(f.seen, path)
	f.mu.Unlock()
}

// candidates also returns the devices newly heard since the previous scan.
// Selection.Seen must follow advertisement events, not each cache poll.
func (f *advertisementFreshness) candidates(objects managedObjects, at time.Time) ([]candidate, []dbus.ObjectPath) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fresh := make(managedObjects)
	pendingDevices := make(map[dbus.ObjectPath]struct{})
	for path, entry := range f.seen {
		if at.Before(entry.seen) || at.Sub(entry.seen) >= advertisementFreshnessTTL {
			delete(f.seen, path)
			continue
		}
		ifaces := objects[path]
		props := ifaces[deviceInterface]
		if props == nil {
			delete(f.seen, path)
			continue
		}
		// The live signal holds the advertisement; the BlueZ object supplies
		// only its current address. Its ServiceData may now show a different
		// concurrent advertisement (for example SensorLink).
		device := make(map[string]dbus.Variant, len(props)+1)
		for key, value := range props {
			device[key] = value
		}
		device["ServiceData"] = dbus.MakeVariant(map[string]dbus.Variant{f.uuid: dbus.MakeVariant(entry.data)})
		fresh[path] = map[string]map[string]dbus.Variant{deviceInterface: device}
		if entry.pending {
			pendingDevices[path] = struct{}{}
			entry.pending = false
			f.seen[path] = entry
		}
	}
	peers := candidates(fresh, f.adapter, f.uuid, f.networkName, f.self)
	updates := make([]dbus.ObjectPath, 0, len(pendingDevices))
	for _, peer := range peers {
		if _, pending := pendingDevices[peer.path]; pending {
			updates = append(updates, peer.path)
		}
	}
	return peers, updates
}

// Subscribe before StartDiscovery. InterfacesAdded is deliberately ignored:
// a newly created Device1 may contain persisted ServiceData. DuplicateData
// reports the next live advertisement as PropertiesChanged. The owner is the
// unique bus name checked by scanOnce, so other processes cannot fake it.
func watchAdvertisements(ctx context.Context, bus *dbus.Conn, owner string, f *advertisementFreshness) (func(), error) {
	properties := []dbus.MatchOption{dbus.WithMatchSender(owner), dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchPathNamespace(f.adapter), dbus.WithMatchArg(0, deviceInterface)}
	removed := []dbus.MatchOption{dbus.WithMatchSender(owner), dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesRemoved")}
	rules := [][]dbus.MatchOption{properties, removed}
	for i, rule := range rules {
		if err := bus.AddMatchSignalContext(ctx, rule...); err != nil {
			removeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			for _, installed := range rules[:i] {
				_ = bus.RemoveMatchSignalContext(removeCtx, installed...)
			}
			cancel()
			return nil, fmt.Errorf("watch BlueZ advertisements: %w", err)
		}
	}
	signals := make(chan *dbus.Signal, 128)
	bus.Signal(signals)
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case signal := <-signals:
				if signal != nil && signal.Sender == owner {
					f.handle(signal, time.Now())
				}
			}
		}
	}()
	return func() {
		close(stop)
		bus.RemoveSignal(signals)
		<-done
		removeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for _, rule := range rules {
			_ = bus.RemoveMatchSignalContext(removeCtx, rule...)
		}
	}, nil
}
