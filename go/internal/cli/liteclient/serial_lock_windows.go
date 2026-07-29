//go:build windows

package liteclient

// On Windows, CreateFile opens serial ports without sharing, so the OS
// already enforces exclusive access and no advisory lock is needed.
type serialLock struct{}

func acquireSerialLock(device string) (*serialLock, error) {
	return &serialLock{}, nil
}

func (l *serialLock) release() {}
