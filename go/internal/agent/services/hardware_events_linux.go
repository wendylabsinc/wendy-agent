//go:build linux

package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// CollectUSBHotplugEvents subscribes to kernel uevents (netlink
// NETLINK_KOBJECT_UEVENT, multicast group 1 — raw kernel events, not udevd's
// group-2 re-broadcasts) and publishes USB device connect/disconnect events as
// OTel log records under service.name "wendy.hardware". Blocks until ctx is
// cancelled.
//
// notify (may be nil) receives a non-blocking signal after every published
// event so the required-device reconciler can run a round immediately instead
// of waiting for its periodic tick.
//
// This is the event-history counterpart to the point-in-time
// ListHardwareCapabilities RPC: it makes "the CAN adapter dropped off the bus
// at 22:14" remotely visible instead of inferred from downstream app failures.
func CollectUSBHotplugEvents(ctx context.Context, logger *zap.Logger, publisher TelemetryPublisher, notify chan<- struct{}) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		logger.Warn("usb hotplug collection unavailable: netlink socket", zap.Error(err))
		return
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		logger.Warn("usb hotplug collection unavailable: netlink bind", zap.Error(err))
		_ = unix.Close(fd)
		return
	}
	// Enlarge the receive buffer so a burst of uevents (device with many
	// interfaces, re-enumeration storm) is dropped by our rate limiter with
	// accounting, rather than silently by the kernel. Best-effort.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 1<<20)

	// Wrap in os.File (non-blocking, runtime poller) so Close from the
	// ctx-cancel goroutine safely unblocks Read — same shutdown pattern as the
	// dmesg collector, without raw-fd close/reuse races.
	if err := unix.SetNonblock(fd, true); err != nil {
		logger.Warn("usb hotplug collection unavailable: set nonblock", zap.Error(err))
		_ = unix.Close(fd)
		return
	}
	f := os.NewFile(uintptr(fd), "netlink-uevent")

	var closeOnce sync.Once
	closeFile := func() { closeOnce.Do(func() { _ = f.Close() }) }
	go func() {
		<-ctx.Done()
		closeFile()
	}()
	defer closeFile()

	logger.Info("usb hotplug event collection started")
	defer logger.Info("usb hotplug event collection stopped")

	resource := hardwareEventsResource()

	// nameCache remembers the sysfs product string seen at connect time so the
	// disconnect event can be labelled after the sysfs entry is gone.
	nameCache := make(map[string]string)

	// Sliding one-second window rate limiter (see usbEventsMaxPerSec).
	var (
		windowStart = time.Now()
		windowCount int
		windowDrop  int
	)

	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		if err != nil {
			if !errors.Is(err, os.ErrClosed) && ctx.Err() == nil {
				logger.Warn("usb hotplug reader exited with error", zap.Error(err))
			}
			return
		}
		kv, ok := parseUEvent(buf[:n])
		if !ok {
			continue
		}
		ev, ok := usbEventFromUEvent(kv)
		if !ok {
			continue
		}

		switch ev.Action {
		case usbEventConnected:
			ev.Product = readUSBSysfsProduct(ev.DevPath)
			if ev.Product != "" {
				if len(nameCache) >= usbDeviceNameCacheMax {
					nameCache = make(map[string]string)
				}
				nameCache[ev.DevPath] = ev.Product
			}
		case usbEventDisconnected:
			ev.Product = nameCache[ev.DevPath]
			delete(nameCache, ev.DevPath)
		}

		now := time.Now()
		if now.Sub(windowStart) >= time.Second {
			if windowDrop > 0 {
				logger.Warn("usb hotplug rate limit: events suppressed in last second",
					zap.Int("suppressed", windowDrop),
					zap.Int("forwarded", windowCount),
				)
				publisher.PublishLogs(usbStormLogRecord(resource, windowDrop, windowCount, now))
			}
			windowStart = now
			windowCount = 0
			windowDrop = 0
		}
		if windowCount >= usbEventsMaxPerSec {
			windowDrop++
			continue
		}
		windowCount++

		publisher.PublishLogs(usbEventLogRecord(resource, ev, now))
		if notify != nil {
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}
}

// readUSBSysfsProduct reads the product string for a freshly added USB device.
// The kernel populates the descriptor attributes before emitting the add
// uevent, so no retry is needed; an empty result just leaves the event
// labelled by vendor:product id.
func readUSBSysfsProduct(devPath string) string {
	if devPath == "" || strings.Contains(devPath, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/sys", devPath, "product"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
