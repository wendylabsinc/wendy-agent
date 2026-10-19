//go:build !unix

package trustedkeys

import "os"

func checkOwner(os.FileInfo) error { return nil }
