//go:build !unix

package jobops

import "os"

func openSpec(name string) (*os.File, error) { return os.Open(name) }
