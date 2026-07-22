//go:build windows

package winusb

// ADB over the WinUSB transport: the Windows counterpart of the gousb-based adb
// package. Both wrap the shared ADB wire protocol in package adbproto; only the
// bulk transfer differs (WinUsb_ReadPipe/WritePipe here vs gousb there). adbd on
// the flashing initrd runs insecure (no AUTH).

import (
	"io"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/adbproto"
)

// ADB is an ADB session over a WinUSB device.
type ADB struct {
	dev  *USBDevice
	conn *adbproto.Conn
}

// NewADB performs the CNXN handshake over an already-open WinUSB device.
func NewADB(dev *USBDevice) (*ADB, error) {
	c := adbproto.NewConn(dev)
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return &ADB{dev: dev, conn: c}, nil
}

// Shell runs a command and returns combined stdout+stderr; a non-zero exit yields
// an *adbproto.ExitError.
func (a *ADB) Shell(command string) (string, error) { return a.conn.Shell(command) }

// Push streams r to remotePath via the sync SEND service with the given unix mode.
func (a *ADB) Push(r io.Reader, remotePath string, mode int) error {
	return a.conn.Push(r, remotePath, mode)
}
