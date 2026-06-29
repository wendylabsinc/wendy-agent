//go:build !darwin

package rcm

import "fmt"

// ListRecoveryDevices is unsupported off macOS.
func ListRecoveryDevices() ([]RecoveryDevice, error) {
	return nil, fmt.Errorf("Jetson USB recovery flashing is only supported on macOS")
}

// WaitForDeviceAt is unsupported off macOS.
func WaitForDeviceAt(string) (*Device, error) {
	return nil, fmt.Errorf("Jetson USB recovery flashing is only supported on macOS")
}
