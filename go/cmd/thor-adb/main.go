// Command thor-adb is a self-contained ADB-over-USB diagnostic: it connects to a
// device's adbd directly over USB (no adb binary or server) to validate the
// in-tree adb package against the T264 initrd-flash gadget.
//
// Usage:
//
//	thor-adb                      run "uname -a" via the shell service
//	thor-adb shell <command...>   run a shell command
//	thor-adb push <local> <remote>   push a file
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/wendylabsinc/wendy/internal/cli/adb"
)

func main() {
	fmt.Println("Connecting to ADB gadget over USB...")
	d, err := adb.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()
	fmt.Printf("connected. device banner: %q\n\n", d.Banner)

	args := os.Args[1:]
	switch {
	case len(args) >= 3 && args[0] == "push":
		data, rerr := os.ReadFile(args[1])
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", args[1], rerr)
			os.Exit(1)
		}
		fmt.Printf("push %s -> %s (%d bytes)\n", args[1], args[2], len(data))
		if err := d.Push(data, args[2], 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "push FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(">> push OK — adbd accepted the file (sync service works)")
	default:
		command := "uname -a"
		if len(args) >= 1 {
			if args[0] == "shell" {
				command = strings.Join(args[1:], " ")
			} else {
				command = strings.Join(args, " ")
			}
		}
		fmt.Printf("$ %s\n", command)
		out, err := d.Shell(command)
		fmt.Print(out)
		if !strings.HasSuffix(out, "\n") {
			fmt.Println()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "shell error: %v\n", err)
			os.Exit(1)
		}
	}
}
