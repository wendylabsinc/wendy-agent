// Command wendy-adb is a drop-in `adb` replacement that speaks ADB over USB
// directly (via internal/cli/tegraflash/adb) — no Google adb binary and no adb
// server/daemon. It implements just the subset NVIDIA's bootburn flashing scripts
// invoke, so they can drive the device through our USB transport unmodified.
//
// Build it AS `adb` so PATH lookups for "adb" resolve here, e.g.:
//
//	go build -o adb ./cmd/wendy-adb
//
// then put that directory first on PATH (thor-flash --adb-dir does this for the
// Python flasher).
//
// Supported: version, start-server/kill-server (no-ops — we are serverless),
// devices [-l], wait-for-device, push <local> <remote>, shell <cmd...>. The global
// "-s <serial>" option is accepted and ignored (there is one device on the bus).
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/adb"
)

// adbSerial is the serial wendy-adb reports for the single USB device. bootburn's
// flasher must use this serial (the monkeypatch driver sets s_AdbSerialNum to it).
const adbSerial = "wendythor"

func main() {
	args := os.Args[1:]

	// Skip leading global options (only -s takes an argument among those bootburn uses).
	i := 0
	for i < len(args) {
		switch a := args[i]; {
		case a == "-s" || a == "-H" || a == "-P" || a == "-L" || a == "-t":
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			goto found
		}
	}
found:
	if i >= len(args) {
		fmt.Fprintln(os.Stderr, "wendy-adb: no command")
		os.Exit(1)
	}
	cmd, rest := args[i], args[i+1:]

	switch cmd {
	case "version":
		fmt.Println("Android Debug Bridge version 1.0.41")
		fmt.Println("wendy-adb: ADB over USB, serverless")
	case "start-server", "kill-server":
		// no-op: there is no server
	case "devices":
		fmt.Println("List of devices attached")
		fmt.Printf("%s\tdevice\n", adbSerial)
		fmt.Println()
	case "wait-for-device":
		waitForDevice()
	case "push":
		doPush(rest)
	case "shell":
		doShell(rest)
	default:
		fmt.Fprintf(os.Stderr, "wendy-adb: unsupported command %q\n", cmd)
		os.Exit(1)
	}
}

func open() *adb.Device {
	d, err := adb.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy-adb: %v\n", err)
		os.Exit(1)
	}
	return d
}

func waitForDevice() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if d, err := adb.Open(); err == nil {
			d.Close()
			return
		}
		time.Sleep(time.Second)
	}
	fmt.Fprintln(os.Stderr, "wendy-adb: wait-for-device timed out")
	os.Exit(1)
}

func doPush(rest []string) {
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "wendy-adb: push needs <local> <remote>")
		os.Exit(1)
	}
	data, err := os.ReadFile(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy-adb: %v\n", err)
		os.Exit(1)
	}
	d := open()
	defer d.Close()
	if err := d.Push(data, rest[1], 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "wendy-adb: push failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: 1 file pushed\n", rest[0])
}

func doShell(rest []string) {
	d := open()
	defer d.Close()
	out, err := d.Shell(strings.Join(rest, " "))
	fmt.Print(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy-adb: shell error: %v\n", err)
		os.Exit(1)
	}
}
