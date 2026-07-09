package commands

import "errors"

// ReasonError tags an error with a bounded, PII-free analytics reason code.
// The Reason is a low-cardinality constant (e.g. "install_download") that
// errorClass reports as-is, so failures that would otherwise collapse into the
// "other" bucket carry a specific reason. The wrapped error is preserved for
// user-facing messages and errors.Is/As matching.
type ReasonError struct {
	Reason string
	Err    error
}

func (e *ReasonError) Error() string { return e.Err.Error() }

func (e *ReasonError) Unwrap() error { return e.Err }

// WithReason tags err with a bounded analytics reason and returns it. It returns
// nil when err is nil. If err already carries a ReasonError anywhere in its
// chain, the existing (more specific) reason wins and err is returned unchanged —
// WithReason never overwrites a reason set closer to the failure site.
func WithReason(reason string, err error) error {
	if err == nil {
		return nil
	}
	var existing *ReasonError
	if errors.As(err, &existing) {
		return err
	}
	return &ReasonError{Reason: reason, Err: err}
}

// Install failure reason codes. All are prefixed "install_" so analytics can
// group them, and each maps to a distinct stage of the OS/firmware install flow.
const (
	reasonInstallElevation     = "install_elevation"       // sudo/UAC elevation failed
	reasonInstallDriveList     = "install_drive_list"      // enumerating drives failed
	reasonInstallDriveNotFound = "install_drive_not_found" // target drive not present
	reasonInstallManifest      = "install_manifest"        // device/version not in manifest / fetch failed
	reasonInstallDownload      = "install_download"        // image/bmap/zst download failed
	reasonInstallImageOpen     = "install_image_open"      // opening/streaming/decompressing image failed
	reasonInstallDiskWrite     = "install_disk_write"      // writing the image to the drive failed
	reasonInstallProvisioning  = "install_provisioning"    // config-partition provisioning unsupported/failed
)
