//go:build linux

package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/containernetworking/plugins/pkg/testutils"
	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/agent/cni/hostlocal"
)

func TestDeleteQuarantinesLeaseUntilInternetGrantRevoked(t *testing.T) {
	// Use a real host-local store and fail firewall inspection at the command
	// boundary. No host networking or privilege is needed for this regression.
	bin := t.TempDir()
	firewall := filepath.Join(bin, "iptables")
	if err := os.WriteFile(firewall, []byte("#!/bin/sh\nexit 4\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, netns := range []string{"", filepath.Join(t.TempDir(), "missing-netns")} {
		t.Run(strings.ReplaceAll(netns, "/", "_"), func(t *testing.T) {
			if err := os.WriteFile(firewall, []byte("#!/bin/sh\nexit 4\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg, err := json.Marshal(map[string]any{
				"cniVersion": "0.4.0", "name": "quarantine-test", "type": "bridge", "bridge": "app0",
				"ipam": map[string]any{"type": "host-local", "subnet": "10.42.1.0/28", "dataDir": t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			args := &skel.CmdArgs{ContainerID: "previous-app", IfName: "eth0", Netns: netns, StdinData: cfg}
			if _, _, err := hostlocal.Allocate(args); err != nil {
				t.Fatal(err)
			}
			if err := CmdDel(args); err == nil {
				t.Fatal("firewall failure was hidden")
			}
			if err := hostlocal.CmdCheck(args); err != nil {
				t.Fatalf("unsafe address released despite stale Internet grant: %v", err)
			}
			// An absent chain means every grant is gone. Retried DEL can now release
			// the quarantine even when the original namespace has disappeared.
			if err := os.WriteFile(firewall, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := CmdDel(args); err != nil {
				t.Fatal(err)
			}
			if err := hostlocal.CmdCheck(args); err == nil {
				t.Fatal("recovered cleanup retained the address")
			}
		})
	}
}

func TestAddQuarantinesLeaseWhenInternetRollbackFails(t *testing.T) {
	if os.Getenv("WENDY_MESH_NAMESPACE_TEST") != "1" {
		t.Skip("disposable privileged container only")
	}
	netns, err := testutils.NewNS()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { netns.Close(); testutils.UnmountNS(netns) })
	bin := t.TempDir()
	firewall := filepath.Join(bin, "iptables")
	// Chain initialization/inspection succeeds until the first attempted grant;
	// both that write and subsequent rollback inspection fail.
	t.Setenv("WENDY_CNI_FAILURE_MARKER", filepath.Join(bin, "failed"))
	script := `#!/bin/sh
case " $* " in
*' -A WENDY-BRIDGE-INTERNET '*) touch "$WENDY_CNI_FAILURE_MARKER"; exit 4;;
*' -S WENDY-BRIDGE-INTERNET '*) if test -e "$WENDY_CNI_FAILURE_MARKER"; then exit 4; fi;;
esac
exit 0
`
	if err := os.WriteFile(firewall, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ip"), []byte("#!/bin/sh\necho '[]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg, err := json.Marshal(map[string]any{
		"cniVersion": "0.4.0", "name": "add-quarantine-test", "type": "bridge", "bridge": "qbridge0", "isDefaultGateway": true,
		"ipam": map[string]any{"type": "host-local", "subnet": "10.42.2.0/28", "dataDir": t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := &skel.CmdArgs{ContainerID: "failed-add-app", IfName: "eth0", Netns: netns.Path(), StdinData: cfg}
	t.Cleanup(func() {
		if link, err := netlink.LinkByName("qbridge0"); err == nil {
			_ = netlink.LinkDel(link)
		}
	})
	if err := CmdAdd(args); err == nil {
		t.Fatal("grant installation failure was hidden")
	}
	if err := hostlocal.CmdCheck(args); err != nil {
		t.Fatalf("failed ADD released a potentially granted address: %v", err)
	}
	if err := CmdDel(args); err == nil {
		t.Fatal("DEL hid failed firewall cleanup")
	}
	if err := hostlocal.CmdCheck(args); err != nil {
		t.Fatalf("DEL released quarantined lease: %v", err)
	}
	if err := netns.Do(func(_ ns.NetNS) error {
		if _, err := netlink.LinkByName("eth0"); err == nil {
			return fmt.Errorf("failed grant revocation prevented veth cleanup")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firewall, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CmdDel(args); err != nil {
		t.Fatal(err)
	}
	if err := hostlocal.CmdCheck(args); err == nil {
		t.Fatal("recovered DEL retained the address")
	}
}
