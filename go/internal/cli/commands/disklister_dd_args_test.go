package commands

import (
	"slices"
	"testing"
)

func TestDarwinDDArgsUseFullblockForStreamedInput(t *testing.T) {
	args, err := darwinDDArgs("/dev/rdisk61", "64m")
	if err != nil {
		t.Fatalf("darwinDDArgs() error = %v", err)
	}

	if !slices.Contains(args, "iflag=fullblock") {
		t.Fatalf("darwinDDArgs() = %v; want iflag=fullblock", args)
	}
	if slices.Contains(args, "conv=sync") {
		t.Fatalf("darwinDDArgs() = %v; conv=sync pads every short pipe read", args)
	}
}

func TestDarwinDDArgsValidatesPrivilegedDDArguments(t *testing.T) {
	for _, rawPath := range []string{
		"",
		"/dev/disk61",
		"/dev/rdisk61/foo",
		"/dev/rdisk61 seek=0",
		"/tmp/wendyos.img",
		"../../etc/sudoers",
	} {
		t.Run("rawPath "+rawPath, func(t *testing.T) {
			if _, err := darwinDDArgs(rawPath, "64m"); err == nil {
				t.Fatalf("darwinDDArgs(%q, 64m) error = nil; want validation error", rawPath)
			}
		})
	}

	for _, bs := range []string{"", "4m", "64m status=none", "1;rm -rf /"} {
		t.Run("bs "+bs, func(t *testing.T) {
			if _, err := darwinDDArgs("/dev/rdisk61", bs); err == nil {
				t.Fatalf("darwinDDArgs(/dev/rdisk61, %q) error = nil; want validation error", bs)
			}
		})
	}
}
