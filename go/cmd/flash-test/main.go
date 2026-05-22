//go:build darwin || linux

// flash-test runs a bare-metal tegraflash from a local bundle path (tar or directory).
// Usage: flash-test <bundle-path> [--emmc] [--xml <name>]
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/internal/cli/tegraflash"
)

func main() {
	emmc := flag.Bool("emmc", false, "flash full eMMC (not just QSPI)")
	xmlName := flag.String("xml", "", "partition XML basename override")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: flash-test <bundle-path> [--emmc] [--xml <name>]")
		os.Exit(1)
	}

	err := tegraflash.Flash(tegraflash.FlashOptions{
		BundlePath: flag.Arg(0),
		XMLName:    *xmlName,
		FullEMMC:   *emmc,
		SkipLarger: tegraflash.DefaultSkipLarger,
		Out:        os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flash: %v\n", err)
		os.Exit(1)
	}
}
