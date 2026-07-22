//go:build windows

package t234

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/winusb"
)

// cfgmgr32 devnode-restart plumbing not wrapped by x/sys/windows.
var (
	modcfgmgr32                  = windows.NewLazySystemDLL("CFGMGR32.dll")
	procCMQueryAndRemoveSubTreeW = modcfgmgr32.NewProc("CM_Query_And_Remove_SubTreeW")
	procCMSetupDevNode           = modcfgmgr32.NewProc("CM_Setup_DevNode")
)

const (
	cmRemoveUINotOK     = 1 // CM_REMOVE_UI_NOT_OK — never pop a veto dialog
	cmRemoveNoRestart   = 2 // CM_REMOVE_NO_RESTART — stay removed until Setup_DevNode
	cmSetupDevNodeReady = 0 // CM_SETUP_DNINST_READY — re-enable the devnode
	crSuccess           = 0 // CR_SUCCESS
)

// restartDevnode surprise-removes a devnode subtree and restores it a second
// later; the hub then re-initializes the device with a port reset — the
// Windows equivalent of a sysfs unbind/bind cycle. Requires Administrator.
func restartDevnode(devInst windows.DEVINST, instanceID string) error {
	if r, _, _ := procCMQueryAndRemoveSubTreeW.Call(
		uintptr(devInst),
		uintptr(0), // pVetoType — NULL forces removal even where a query would veto
		uintptr(0), // pszVetoName
		uintptr(0), // ulNameLength
		uintptr(cmRemoveUINotOK|cmRemoveNoRestart),
	); r != crSuccess {
		return fmt.Errorf("CM_Query_And_Remove_SubTree(%s): CR code %d", instanceID, r)
	}
	time.Sleep(time.Second)
	if r, _, _ := procCMSetupDevNode.Call(uintptr(devInst), uintptr(cmSetupDevNodeReady)); r != crSuccess {
		return fmt.Errorf("CM_Setup_DevNode(%s): CR code %d", instanceID, r)
	}
	return nil
}

// ReleaseUSB forces a USB-level disconnect of the flashing gadget so the
// device sees the host let go (its initrd polls the UDC state and proceeds
// only when it leaves "configured"). It surprise-removes the gadget's devnode
// subtree and restores it a second later — the hub then re-initializes the
// device with a port reset, the Windows equivalent of the sysfs unbind/bind
// cycle on Linux. Requires Administrator. serial, when set, must match the
// gadget's USB serial number; port, when set, its physical location path.
func ReleaseUSB(serial, port string) error {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return fmt.Errorf("SetupDiGetClassDevsEx(USB): %w", err)
	}
	defer set.Close()

	for i := 0; ; i++ {
		info, err := set.EnumDeviceInfo(i)
		if err != nil {
			break // ERROR_NO_MORE_ITEMS
		}
		instanceID, err := set.DeviceInstanceID(info)
		if err != nil {
			continue
		}
		vid, pid, ok := winusb.ParseVIDPID(instanceID)
		if !ok || vid != GadgetVendorID || pid != GadgetProductID {
			continue
		}
		// Only whole-device nodes: a composite gadget's usbccgp function
		// devnodes (MI_xx) also carry the gadget VID/PID, but their instance
		// trailer is a synthesized ID and their location path a #USBMI(n)
		// variant — the serial and port passed here (see gadgetPortMap) name
		// the root, and removing the root removes the whole subtree.
		if isCompositeFunction(instanceID) {
			continue
		}
		if serial != "" && !strings.EqualFold(winusb.InstanceSerial(instanceID), serial) {
			continue
		}
		if port != "" {
			v, err := set.DeviceRegistryProperty(info, windows.SPDRP_LOCATION_PATHS)
			if err != nil || winusb.FirstString(v) != port {
				continue
			}
		}

		return restartDevnode(info.DevInst, instanceID)
	}
	return fmt.Errorf("flashing gadget (%04x:%04x) not found on USB", GadgetVendorID, GadgetProductID)
}
