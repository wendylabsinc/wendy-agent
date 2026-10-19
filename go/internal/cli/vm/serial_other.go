//go:build !windows

package vm

import (
	"io"
	"os"
	"strings"
)

// RunForeground adds the VM name and the --clear fix to this error.
func openHostSerial(SerialConfig) (io.ReadWriteCloser, error) {
	return nil, errSerialHostUnsupported
}

func guardSerialProcess(*os.Process) (io.Closer, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
