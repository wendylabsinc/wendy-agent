//go:build linux

package nanprovider

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const partialCreateHelper = "/usr/sbin/wendyos-nan"

// The real Provider calls this subprocess through its ordinary helper path.
// It creates a real kernel interface, then reports failure just as a helper
// interrupted after interface_add can do. Only a disposable container installs
// this test binary at the helper path; no radio hardware is required.
func init() {
	if os.Args[0] != partialCreateHelper || os.Getenv("WENDY_NAN_PARTIAL_CREATE_HELPER") != "1" {
		return
	}
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	if len(os.Args) < 2 {
		fail(fmt.Errorf("missing helper command"))
	}
	switch os.Args[1] {
	case "status":
		fmt.Println("nan_started=0")
	case "start", "schedule-default":
	case "ndi-create":
		if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: ndiName}}); err != nil {
			fail(err)
		}
		if os.Getenv("WENDY_NAN_PARTIAL_CREATE_WAIT") == "1" {
			fmt.Fprintln(os.Stderr, "partial-create-fault")
			time.Sleep(time.Hour) // CommandContext must cancel this child.
		}
		fail(fmt.Errorf("partial-create-fault"))
	case "ndi-remove":
		link, err := netlink.LinkByName(ndiName)
		if _, absent := err.(netlink.LinkNotFoundError); !absent {
			if err != nil {
				fail(err)
			}
			if err = netlink.LinkDel(link); err != nil {
				fail(err)
			}
		}
	case "stop":
		if _, err := netlink.LinkByName(ndiName); err == nil {
			fail(fmt.Errorf("remove NDI before stopping NAN"))
		}
	default:
		fail(fmt.Errorf("unexpected helper command %q", os.Args[1]))
	}
	os.Exit(0)
}

type unusedPartialCreateNode struct{}

func (unusedPartialCreateNode) AttachWithCost(context.Context, meshid.Device, *quic.Conn, uint16) error {
	return fmt.Errorf("unexpected peer attachment")
}

func TestProviderRemovesNDIAfterPartialCreateFailure(t *testing.T) {
	for _, cancelAfterCreate := range []bool{false, true} {
		name := "helper reports failure"
		if cancelAfterCreate {
			name = "parent cancels after interface creation"
		}
		t.Run(name, func(t *testing.T) { checkPartialCreateCleanup(t, cancelAfterCreate) })
	}
}

func checkPartialCreateCleanup(t *testing.T, cancelAfterCreate bool) {
	t.Helper()
	if os.Getenv("WENDY_LOCALMESH_ISOLATED_TEST") != "1" {
		t.Skip("disposable privileged container only")
	}
	protected := []string{partialCreateHelper, nanControlPath, sessionOwnerPath, "/run/wendy-nan-provider.lock", "/run/systemd/network/05-wendy-nan-provider.network", "/run/NetworkManager/conf.d/05-wendy-nan-provider.conf"}
	for _, path := range protected {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Skipf("preserve pre-existing host state: %s", path)
		}
	}
	if _, err := netlink.LinkByName(ndiName); err == nil {
		t.Skip("preserve pre-existing NDI")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(partialCreateHelper), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(exe, partialCreateHelper); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(partialCreateHelper) })
	t.Setenv("WENDY_NAN_PARTIAL_CREATE_HELPER", "1")
	if err = os.MkdirAll(filepath.Dir(nanControlPath), 0700); err != nil {
		t.Fatal(err)
	}
	control, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: nanControlPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close(); _ = os.Remove(nanControlPath) })
	id := underlayDevice(t, 0x1234)
	t.Cleanup(func() {
		if link, err := netlink.LinkByName(ndiName); err == nil && link.Type() == "dummy" {
			_ = netlink.LinkDel(link)
		}
		_ = clearSessionOwner(id)
		for _, path := range protected[3:] {
			_ = os.Remove(path)
		}
	})
	provider := Provider{Credentials: &localmesh.Credentials{Device: id}, Node: unusedPartialCreateNode{}}
	if cancelAfterCreate {
		t.Setenv("WENDY_NAN_PARTIAL_CREATE_WAIT", "1")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- provider.Run(ctx) }()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, lookupErr := netlink.LinkByName(ndiName); lookupErr == nil {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("provider exited before creating its interface: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				<-done
				t.Fatal("helper did not reach partial interface creation")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		err = <-done
	} else {
		err = provider.Run(context.Background())
	}
	if err == nil || !cancelAfterCreate && !strings.Contains(err.Error(), "partial-create-fault") {
		t.Fatalf("did not reach the real partial-create fault: %v", err)
	}
	if _, err = netlink.LinkByName(ndiName); err == nil {
		t.Error("failed helper left its unaddressed NDI behind, preventing the next provider from starting")
	}
	if _, err = os.Lstat(sessionOwnerPath); !os.IsNotExist(err) {
		t.Errorf("NAN cleanup could not finish after partial interface creation: %v", err)
	}
}
