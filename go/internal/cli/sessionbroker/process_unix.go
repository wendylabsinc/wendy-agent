//go:build !windows

package sessionbroker

import (
	"fmt"
	"os"
	"syscall"

	"github.com/wendylabsinc/wendy/go/internal/shared/flock"
)

func detachedProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func validateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("session broker path is not owned by the current user")
	}
	return nil
}

// pathOwnedByCurrentUser reports whether path belongs to this process's user.
// A missing path is fine — whoever creates it will own it.
func pathOwnedByCurrentUser(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && validateOwner(info) == nil
}

func acquireLock(file *os.File) (bool, error) {
	return flock.TryLock(file)
}

func releaseLock(file *os.File) {
	_ = flock.Unlock(file)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
