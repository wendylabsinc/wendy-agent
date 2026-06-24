//go:build !linux

package services

import "runtime"

func detectDistro() (string, string) { return "", "" }
func detectOS() string               { return runtime.GOOS }
