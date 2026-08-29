package clitimesync

import (
	"os"
	"testing"
)

// FetchProofPacket writes the proof it obtains under $HOME, so every test in this
// package runs against a throwaway home. Without this a test that stubs the
// Roughtime query overwrites the developer's own cached proof with a fake one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wendy-timesync-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)        //nolint:errcheck
	os.Setenv("USERPROFILE", dir) //nolint:errcheck — config.ConfigDir on Windows
	code := m.Run()
	os.RemoveAll(dir) //nolint:errcheck
	os.Exit(code)
}
