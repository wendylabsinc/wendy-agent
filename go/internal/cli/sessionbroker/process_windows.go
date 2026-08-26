//go:build windows

package sessionbroker

import (
	"os"
	"syscall"
)

func detachedProcessAttributes() *syscall.SysProcAttr { return nil }
func validateOwner(os.FileInfo) error                 { return ErrUnavailable }
func acquireLock(*os.File) (bool, error)              { return false, ErrUnavailable }
func releaseLock(*os.File)                            {}
func processAlive(int) bool                           { return false }
