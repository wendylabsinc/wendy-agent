package discovery

import (
	"log"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
)

// SerialDevice is a Wendy Lite device reachable over a serial port.
type SerialDevice struct {
	Port        string
	ID          string
	Name        string
	DisplayName string
}

// ListenerID identifies a registered listener and is used to remove it.
type ListenerID uint64

// SerialDiscovery probes all ESP32 serial ports and builds a list of
// reachable Wendy Lite devices. Each port is probed concurrently; all
// registered listeners are invoked (serially) whenever the list changes.
type SerialDiscovery struct {
	mu             sync.Mutex
	notifyMu       sync.Mutex
	running        bool
	repeatInterval time.Duration
	probing        map[string]bool
	probeSem       chan struct{} // limits concurrent probes
	devices        []SerialDevice
	listeners      map[ListenerID]func([]SerialDevice)
	nextID         ListenerID
}

var (
	serialDiscoveryInstance     *SerialDiscovery
	serialDiscoveryInstanceOnce sync.Once
)

// GetSerialDiscovery returns the singleton SerialDiscovery.
func GetSerialDiscovery() *SerialDiscovery {
	serialDiscoveryInstanceOnce.Do(func() {
		serialDiscoveryInstance = &SerialDiscovery{
			probing:   make(map[string]bool),
			probeSem:  make(chan struct{}, 4),
			listeners: make(map[ListenerID]func([]SerialDevice)),
		}
	})
	return serialDiscoveryInstance
}

// AddListener registers a function to be called whenever the device list
// changes. Returns an ID that can be passed to RemoveListener.
func (d *SerialDiscovery) AddListener(cb func([]SerialDevice)) ListenerID {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	d.listeners[d.nextID] = cb
	return d.nextID
}

// RemoveListener unregisters the listener identified by id.
func (d *SerialDiscovery) RemoveListener(id ListenerID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.listeners, id)
}

// StartScan resolves all ESP32 serial ports and probes each one concurrently
// in the background. A port is added to the list if a WendyCom connection can
// be established; the connection is closed immediately after the check. Ports
// that are no longer present are removed. Returns immediately.
//
// repeatInterval controls automatic re-scanning: zero means run once, any
// positive value causes the scan to repeat after each run. Calling StartScan
// again while a scan loop is active updates the interval immediately; the
// change takes effect after the current iteration completes.
func (d *SerialDiscovery) StartScan(repeatInterval time.Duration) {
	d.mu.Lock()
	d.repeatInterval = repeatInterval
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()

	go func() {
		for {
			ports, err := ResolveESP32SerialPorts()
			if err == nil {
				portSet := make(map[string]bool, len(ports))
				for _, p := range ports {
					portSet[p.Port] = true
				}

				// Remove devices whose ports are no longer present.
				d.mu.Lock()
				kept := make([]SerialDevice, 0, len(d.devices))
				var removed int
				for _, dev := range d.devices {
					if portSet[dev.Port] {
						kept = append(kept, dev)
					} else {
						removed++
					}
				}
				d.devices = kept
				d.mu.Unlock()

				if removed > 0 {
					d.notify()
				}

				// Probe ports not already in the list.
				d.mu.Lock()
				existing := make(map[string]bool, len(d.devices))
				for _, dev := range d.devices {
					existing[dev.Port] = true
				}
				d.mu.Unlock()

				for _, p := range ports {
					d.mu.Lock()
					skip := existing[p.Port] || d.probing[p.Port]
					if !skip {
						d.probing[p.Port] = true
					}
					d.mu.Unlock()
					if skip {
						continue
					}
					go func(port string) {
						d.probeSem <- struct{}{}
						defer func() {
							<-d.probeSem
							d.mu.Lock()
							delete(d.probing, port)
							d.mu.Unlock()
						}()
						client := liteclient.NewWendyLiteClient()
						if err := client.ConnectToSerial(port); err != nil {
							return
						}
						identity, err := client.GetDeviceIdentity(3 * time.Second)
						client.Close()
						if err != nil || identity == nil {
							return
						}

						d.mu.Lock()
						d.devices = append(d.devices, SerialDevice{
							Port:        port,
							ID:          identity.ID,
							Name:        identity.Name,
							DisplayName: identity.DisplayName,
						})
						d.mu.Unlock()

						d.notify()
					}(p.Port)
				}
			}

			d.mu.Lock()
			next := d.repeatInterval
			if next <= 0 {
				d.running = false
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()

			time.Sleep(next)

			d.mu.Lock()
			if d.repeatInterval == 0 {
				d.running = false
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()
		}
	}()
}

func (d *SerialDiscovery) StopScan() {
	d.mu.Lock()
	d.repeatInterval = 0
	d.mu.Unlock()
}

// notify snapshots the device list and calls all registered listeners serially.
func (d *SerialDiscovery) notify() {
	d.mu.Lock()
	snap := d.snapshotLocked()
	cbs := make([]func([]SerialDevice), 0, len(d.listeners))
	for _, cb := range d.listeners {
		cbs = append(cbs, cb)
	}
	d.mu.Unlock()

	d.notifyMu.Lock()
	for _, cb := range cbs {
		callListener(cb, snap)
	}
	d.notifyMu.Unlock()
}

func callListener(cb func([]SerialDevice), snap []SerialDevice) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("discovery: listener panic: %v", r)
		}
	}()
	cb(snap)
}

// Devices returns a snapshot of the current device list.
func (d *SerialDiscovery) Devices() []SerialDevice {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

// snapshotLocked returns a copy of the device list. Must be called with mu held.
func (d *SerialDiscovery) snapshotLocked() []SerialDevice {
	snap := make([]SerialDevice, len(d.devices))
	copy(snap, d.devices)
	return snap
}
