//go:build !darwin && !linux

package rcm

import "fmt"

// ListRecoveryDevices is unsupported off macOS/Linux.
func ListRecoveryDevices() ([]RecoveryDevice, error) {
	return nil, fmt.Errorf("Jetson USB recovery flashing is only supported on macOS and Linux")
}

// WaitForDeviceAt is unsupported off macOS/Linux.
func WaitForDeviceAt(string) (*Device, error) {
	return nil, fmt.Errorf("Jetson USB recovery flashing is only supported on macOS and Linux")
}
