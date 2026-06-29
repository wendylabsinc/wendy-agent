//go:build darwin

// Package shim is the multi-call host shim for driving the T264 initrd-flash gadget,
// folded into the wendy binary. NVIDIA's bootburn flasher shells out to `adb`,
// `lsusb` and `timeout`; on macOS those are missing or x86-only. Rather than ship a
// separate binary, wendy re-execs itself: a directory of symlinks (adb/lsusb/timeout
// -> the wendy binary) is put on PATH, and main() dispatches to Dispatch() when it is
// invoked under one of those names (see IsShimName).
package shim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/adb"
)

// adbSerial is the serial we report for the single USB device. bootburn's flasher
// must use this serial (the monkeypatch driver sets s_AdbSerialNum to it).
const adbSerial = "wendythor"

// shimNames are the tool names wendy impersonates for bootburn.
var shimNames = map[string]bool{"adb": true, "lsusb": true, "timeout": true}

// IsShimName reports whether argv[0]'s basename is one of the tools wendy stands in
// for. main() calls Dispatch() when this is true, before cobra runs.
func IsShimName(name string) bool { return shimNames[name] }

// Dispatch runs the shim for the current invocation name and exits the process.
func Dispatch() {
	switch filepath.Base(os.Args[0]) {
	case "lsusb":
		runLsusb(os.Args[1:])
	case "timeout":
		runTimeout(os.Args[1:])
	default: // "adb"
		runAdb(os.Args[1:])
	}
	os.Exit(0)
}

// ---- adb ----

func runAdb(args []string) {
	// Skip leading global options (only -s/-H/-P/-L/-t take an argument).
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
		fmt.Fprintln(os.Stderr, "wendy adb: no command")
		os.Exit(1)
	}
	cmd, rest := args[i], args[i+1:]

	switch cmd {
	case "version":
		fmt.Println("Android Debug Bridge version 1.0.41")
		fmt.Println("wendy: ADB over USB, serverless")
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
		fmt.Fprintf(os.Stderr, "wendy adb: unsupported command %q\n", cmd)
		os.Exit(1)
	}
}

func openADB() *adb.Device {
	d, err := adb.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: %v\n", err)
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
	fmt.Fprintln(os.Stderr, "wendy adb: wait-for-device timed out")
	os.Exit(1)
}

func doPush(rest []string) {
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "wendy adb: push needs <local> <remote>")
		os.Exit(1)
	}
	info, err := os.Stat(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: %v\n", err)
		os.Exit(1)
	}
	file, err := os.Open(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()
	d := openADB()
	defer d.Close()
	// Match adb: pushing a file to a directory writes <dir>/<basename>, and the
	// pushed file keeps the local file's permission bits (e.g. wr_sh.sh/nvdd are
	// 0755 and must stay executable on the device). The file is streamed, not read
	// whole, so a multi-GB rootfs push stays bounded in memory.
	remote := rest[1]
	if strings.HasSuffix(remote, "/") || d.IsDir(remote) {
		remote = strings.TrimSuffix(remote, "/") + "/" + filepath.Base(rest[0])
	}
	if err := d.Push(file, remote, int(info.Mode().Perm())); err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: push failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: 1 file pushed\n", rest[0])
}

func doShell(rest []string) {
	d := openADB()
	defer d.Close()
	out, err := d.Shell(strings.Join(rest, " "))
	fmt.Print(out)
	if ee, ok := err.(*adb.ExitError); ok {
		os.Exit(ee.Code)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: shell error: %v\n", err)
		os.Exit(1)
	}
}

// ---- lsusb ----

// runLsusb supports `lsusb -d <vid>:` (the only form bootburn uses, in
// CheckUSBServiceInit) and exits 0 iff a USB device with that vendor id is present.
func runLsusb(args []string) {
	vid := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-d" && i+1 < len(args) {
			vid = strings.SplitN(args[i+1], ":", 2)[0]
			i++
		}
	}
	if vid == "" {
		os.Exit(0)
	}
	n, err := strconv.ParseUint(vid, 16, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy lsusb: bad vendor id %q\n", vid)
		os.Exit(2)
	}
	if adb.VendorPresent(uint16(n)) {
		os.Exit(0)
	}
	os.Exit(1)
}

// ---- timeout ----

// runTimeout implements `timeout <seconds> <cmd...>`: run cmd, killing it (exit 124)
// if it does not finish within the given whole seconds. The child's exit code is
// propagated otherwise.
func runTimeout(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "wendy timeout: usage: timeout <seconds> <cmd> [args...]")
		os.Exit(125)
	}
	secs, err := strconv.Atoi(strings.TrimSuffix(args[0], "s"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy timeout: bad duration %q\n", args[0])
		os.Exit(125)
	}
	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "wendy timeout: %v\n", err)
		os.Exit(126)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case <-time.After(time.Duration(secs) * time.Second):
		_ = cmd.Process.Kill()
		os.Exit(124)
	}
}
