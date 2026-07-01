//go:build darwin

package discovery

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/IOKitLib.h>
#include <IOKit/serial/IOSerialKeys.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

static char* stringProp(io_service_t svc, CFStringRef key) {
	CFTypeRef v = IORegistryEntryCreateCFProperty(svc, key, kCFAllocatorDefault, 0);
	if (!v || CFGetTypeID(v) != CFStringGetTypeID()) {
		if (v) CFRelease(v);
		return NULL;
	}
	CFIndex n = CFStringGetMaximumSizeForEncoding(
		CFStringGetLength((CFStringRef)v), kCFStringEncodingUTF8) + 1;
	char *buf = (char*)malloc(n);
	if (!CFStringGetCString((CFStringRef)v, buf, n, kCFStringEncodingUTF8)) {
		free(buf);
		CFRelease(v);
		return NULL;
	}
	CFRelease(v);
	return buf;
}

static int intProp(io_service_t svc, CFStringRef key, int *out) {
	CFTypeRef v = IORegistryEntryCreateCFProperty(svc, key, kCFAllocatorDefault, 0);
	if (!v || CFGetTypeID(v) != CFNumberGetTypeID()) {
		if (v) CFRelease(v);
		return 0;
	}
	CFNumberGetValue((CFNumberRef)v, kCFNumberIntType, out);
	CFRelease(v);
	return 1;
}

typedef struct { char **paths; int count; } WendySerialList;

static WendySerialList wendy_find_usb_serial(int wantVID, int wantPID) {
	WendySerialList result = {NULL, 0};
	int cap = 8;
	result.paths = (char**)malloc(cap * sizeof(char*));

	CFMutableDictionaryRef match = IOServiceMatching(kIOSerialBSDServiceValue);
	if (!match) return result;

	io_iterator_t iter = IO_OBJECT_NULL;
	if (IOServiceGetMatchingServices(0, match, &iter) != KERN_SUCCESS)
		return result;

	io_service_t svc;
	while ((svc = IOIteratorNext(iter)) != IO_OBJECT_NULL) {
		char *path = stringProp(svc, CFSTR(kIOCalloutDeviceKey));
		if (!path) { IOObjectRelease(svc); continue; }

		// Walk up the registry tree to find the USB node with VID/PID.
		int matched = 0;
		io_service_t cur = svc;
		IOObjectRetain(cur);
		io_service_t parent;
		while (IORegistryEntryGetParentEntry(cur, kIOServicePlane, &parent) == KERN_SUCCESS) {
			IOObjectRelease(cur);
			cur = parent;
			int vid = 0, pid = 0;
			if (intProp(cur, CFSTR("idVendor"), &vid) && intProp(cur, CFSTR("idProduct"), &pid)) {
				matched = (vid == wantVID && pid == wantPID);
				break;
			}
		}
		IOObjectRelease(cur);

		if (matched) {
			if (result.count >= cap) {
				cap *= 2;
				char **tmp = (char**)realloc(result.paths, cap * sizeof(char*));
				if (!tmp) {
					free(path);
					IOObjectRelease(svc);
					goto cleanup;
				}
				result.paths = tmp;
			}
			result.paths[result.count++] = path;
		} else {
			free(path);
		}
		IOObjectRelease(svc);
	}
cleanup:
	IOObjectRelease(iter);
	return result;
}

static void wendy_free_serial_list(WendySerialList list) {
	for (int i = 0; i < list.count; i++) free(list.paths[i]);
	free(list.paths);
}
*/
import "C"

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// ResolveESP32SerialPorts returns all connected serial ports whose USB VID/PID
// match the ESP32 constants, along with each device node's plug-in time.
func ResolveESP32SerialPorts() ([]SerialPortInfo, error) {
	vid, err := parseHexID(models.ESP32VendorID)
	if err != nil {
		return nil, fmt.Errorf("invalid ESP32VendorID %q: %w", models.ESP32VendorID, err)
	}
	pid, err := parseHexID(models.ESP32ProductID)
	if err != nil {
		return nil, fmt.Errorf("invalid ESP32ProductID %q: %w", models.ESP32ProductID, err)
	}

	list := C.wendy_find_usb_serial(C.int(vid), C.int(pid))
	defer C.wendy_free_serial_list(list)

	count := int(list.count)
	if count == 0 {
		return nil, nil
	}

	paths := unsafe.Slice(list.paths, count)
	result := make([]SerialPortInfo, 0, count)
	for _, cp := range paths {
		path := C.GoString(cp)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		result = append(result, SerialPortInfo{Port: path, ConnectionTime: info.ModTime()})
	}
	return result, nil
}

func parseHexID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(s, "0x"), 16, 32)
}
