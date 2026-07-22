//go:build windows

package winusb

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// InstallDriver installs a WinUSB driver binding for the Jetson USB device IDs
// (recovery 0955:7023/7026 and the initrd-flash gadget 0955:7100), so wendy can
// open them over WinUSB. Steps, all via inbox DLLs:
//
//  1. generate a self-signed code-signing cert (key in a machine container);
//  2. write the INF and build+sign its catalog with that cert;
//  3. trust the cert (LocalMachine Root + TrustedPublisher);
//  4. stage the package into the driver store (SetupCopyOEMInf);
//  5. bind it to any present matching device (UpdateDriverForPlugAndPlayDevices).
//
// Staging in step 4 means the gadget (0955:7100) binds automatically when it
// enumerates mid-flash, so the user sees a single UAC prompt total.
//
// Requires elevation (machine key container + Root store + driver store).
func InstallDriver(out io.Writer) error {
	// Working dir for the package files; the .cat must sit beside the .inf.
	dir, err := os.MkdirTemp("", "wendy-jetson-driver-")
	if err != nil {
		return fmt.Errorf("creating package dir: %w", err)
	}
	defer os.RemoveAll(dir)
	infPath := filepath.Join(dir, infFileName)
	catPath := filepath.Join(dir, catalogFileName)

	fmt.Fprintln(out, "1/5 Writing driver INF…")
	if err := os.WriteFile(infPath, []byte(generateINF()), 0o644); err != nil {
		return fmt.Errorf("writing INF: %w", err)
	}

	fmt.Fprintln(out, "2/5 Generating self-signed signing certificate…")
	cert, err := createSigningCert(true)
	if err != nil {
		return err
	}
	defer cert.Free()

	fmt.Fprintln(out, "3/5 Building and signing driver catalog…")
	if err := buildAndSignCatalog(catPath, infPath, jetsonHardwareIDs(), cert); err != nil {
		return err
	}

	fmt.Fprintln(out, "4/5 Trusting certificate + staging driver package…")
	if err := cert.installToStores(); err != nil {
		return err
	}
	if err := stageDriverPackage(infPath); err != nil {
		return err
	}

	fmt.Fprintln(out, "5/5 Binding driver to present devices…")
	bound := 0
	for _, hwid := range jetsonHardwareIDs() {
		ok, err := bindDriverToPresentDevices(infPath, hwid)
		if err != nil {
			fmt.Fprintf(out, "    %s: %v\n", hwid, err)
			continue
		}
		if ok {
			bound++
			fmt.Fprintf(out, "    %s: bound\n", hwid)
		}
	}
	fmt.Fprintf(out, "Done. Package staged; %d present device(s) bound now, others bind on connect.\n", bound)
	return nil
}

// stageDriverPackage copies the INF (and its referenced catalog) into the Windows
// driver store via SetupCopyOEMInf. Fails if the catalog isn't validly signed by
// a trusted publisher — which is why installToStores runs first.
func stageDriverPackage(infPath string) error {
	winf, err := windows.UTF16PtrFromString(infPath)
	if err != nil {
		return err
	}
	const spostPath = 1 // SPOST_PATH
	r, _, e := procSetupCopyOEMInfW.Call(
		uintptr(unsafe.Pointer(winf)),
		0,         // OEMSourceMediaLocation
		spostPath, // OEMSourceMediaType
		0,         // CopyStyle
		0,         // DestinationInfFileName
		0,         // DestinationInfFileNameSize
		0,         // RequiredSize
		0,         // DestinationInfFileNameComponent
	)
	if r == 0 {
		return fmt.Errorf("SetupCopyOEMInf: %w", e)
	}
	return nil
}

// bindDriverToPresentDevices installs infPath onto any currently-present device
// matching hwid. Returns (false, nil) when no such device is present (not an
// error — the package is staged and will bind on connect). Reports whether a
// reboot was requested (ignored; WinUSB never needs one).
func bindDriverToPresentDevices(infPath, hwid string) (bool, error) {
	winf, err := windows.UTF16PtrFromString(infPath)
	if err != nil {
		return false, err
	}
	whwid, err := windows.UTF16PtrFromString(hwid)
	if err != nil {
		return false, err
	}
	var rebootRequired uint32
	r, _, e := procUpdateDriverForPlugAndPlayDevicesW.Call(
		0, // hwndParent
		uintptr(unsafe.Pointer(whwid)),
		uintptr(unsafe.Pointer(winf)),
		uintptr(installFlagForce),
		uintptr(unsafe.Pointer(&rebootRequired)),
	)
	if r == 0 {
		// ERROR_NO_SUCH_DEVINST / ERROR_NO_MORE_ITEMS: no present device with this
		// ID — expected for the gadget PID and any absent board. Not an error.
		if errno, ok := e.(windows.Errno); ok {
			switch uintptr(errno) {
			case 0xE000020B, // ERROR_NO_SUCH_DEVINST (SPAPI)
				uintptr(windows.ERROR_NO_MORE_ITEMS),
				uintptr(windows.ERROR_FILE_NOT_FOUND):
				return false, nil
			}
		}
		return false, fmt.Errorf("UpdateDriverForPlugAndPlayDevices: %w", e)
	}
	return true, nil
}
