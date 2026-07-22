//go:build windows

package t234

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ioctlDiskSetDiskAttributes = 0x0007c0f4 // IOCTL_DISK_SET_DISK_ATTRIBUTES
	ioctlDiskUpdateProperties  = 0x00070140 // IOCTL_DISK_UPDATE_PROPERTIES

	diskAttributeOffline  = 0x0000000000000001 // DISK_ATTRIBUTE_OFFLINE
	diskAttributeReadOnly = 0x0000000000000002 // DISK_ATTRIBUTE_READ_ONLY
)

// setDiskAttributes mirrors SET_DISK_ATTRIBUTES (winioctl.h).
type setDiskAttributes struct {
	Version        uint32
	Persist        byte
	_              [3]byte
	Attributes     uint64
	AttributesMask uint64
	_              [4]uint32
}

// prepareRawTarget makes a physical drive safe for a full raw rewrite. Windows
// denies writes into any sector owned by a mounted volume, and it eagerly
// mounts a partition it recognizes the instant the gadget's eMMC appears — a
// stale ESP from a prior flash triggers the "you need to format this disk"
// prompt and, once mounted, makes the raw ESP write fail with
// ERROR_ACCESS_DENIED. Locking volumes from the parent process races that
// mount, so the writer takes the whole disk offline here — in the same process
// and immediately before writing. An offline disk force-dismounts its volumes
// and mounts none for the life of this handle, which closes the race. The
// physical-drive handle itself stays writable while offline. A regular file
// (tests) has no drive number and is left untouched.
func prepareRawTarget(dev *os.File) error {
	diskNum, ok := physicalDriveNumber(dev.Name())
	if !ok {
		return nil
	}
	attrs := setDiskAttributes{
		Version:        uint32(unsafe.Sizeof(setDiskAttributes{})),
		Attributes:     diskAttributeOffline,
		AttributesMask: diskAttributeOffline | diskAttributeReadOnly,
	}
	h := windows.Handle(dev.Fd())
	var bytesReturned uint32
	if err := windows.DeviceIoControl(h, ioctlDiskSetDiskAttributes,
		(*byte)(unsafe.Pointer(&attrs)), uint32(unsafe.Sizeof(attrs)),
		nil, 0, &bytesReturned, nil); err != nil {
		return fmt.Errorf("taking PhysicalDrive%d offline for raw write (is a disk tool or format prompt holding it?): %w", diskNum, err)
	}
	// Make the offline state take effect before the first write.
	_ = windows.DeviceIoControl(h, ioctlDiskUpdateProperties, nil, 0, nil, 0, &bytesReturned, nil)
	return nil
}
