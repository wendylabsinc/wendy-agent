//go:build linux

package localmesh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// HostPolicy owns resolver roaming only. Internet tunnels, leases and forwarding
// policy belong to meshsharing; Babel owns device ULA routing.
type HostPolicy struct {
	dnsAddress, dnsInterface, previousResolver string
}

func policyCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return out, nil
}

// DNS is associated with the real TUN toward the gateway, not the dummy address
// interface: resolved binds its outbound queries to this link.
func (p *HostPolicy) SetDNS(ctx context.Context, iface, address string) error {
	if address == p.dnsAddress && iface == p.dnsInterface {
		return nil
	}
	if p.dnsInterface != "" {
		_, err := policyCommand(ctx, "resolvectl", "revert", p.dnsInterface)
		if err != nil {
			if _, lookup := net.InterfaceByName(p.dnsInterface); lookup == nil {
				return err
			}
		}
		p.dnsInterface = ""
		p.dnsAddress = ""
	}
	if address == "" {
		return p.restoreResolver()
	}
	if _, err := policyCommand(ctx, "resolvectl", "dns", iface, address); err != nil {
		return err
	}
	if _, err := policyCommand(ctx, "resolvectl", "domain", iface, "~."); err != nil {
		_, _ = policyCommand(ctx, "resolvectl", "revert", iface)
		return err
	}
	if err := p.useResolverStub(); err != nil {
		_, _ = policyCommand(ctx, "resolvectl", "revert", iface)
		return err
	}
	p.dnsInterface = iface
	p.dnsAddress = address
	return nil
}

func (p *HostPolicy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return p.SetDNS(ctx, "", "")
}

const resolverStub = "/run/systemd/resolve/stub-resolv.conf"

func replaceResolverLink(target string) error {
	file, err := os.CreateTemp("/etc", ".wendy-local-mesh-resolver-")
	if err != nil {
		return err
	}
	name := file.Name()
	_ = file.Close()
	defer os.Remove(name)
	if err = os.Remove(name); err != nil {
		return err
	}
	if err = os.Symlink(target, name); err != nil {
		return err
	}
	return os.Rename(name, "/etc/resolv.conf")
}

func (p *HostPolicy) useResolverStub() error {
	if _, err := os.Stat(resolverStub); err != nil {
		return err
	}
	current, err := os.Readlink("/etc/resolv.conf")
	if err != nil {
		return fmt.Errorf("mesh DNS requires a managed resolv.conf symlink: %w", err)
	}
	resolved := current
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join("/etc", resolved)
	}
	if filepath.Clean(resolved) == resolverStub {
		return nil
	}
	if p.previousResolver != "" {
		return errors.New("resolver changed externally during local-mesh roaming")
	}
	if err = replaceResolverLink(resolverStub); err != nil {
		return err
	}
	p.previousResolver = current
	return nil
}

func (p *HostPolicy) restoreResolver() error {
	if p.previousResolver == "" {
		return nil
	}
	current, err := os.Readlink("/etc/resolv.conf")
	if err != nil {
		return err
	}
	if current != resolverStub {
		return errors.New("resolver ownership changed; refusing overwrite")
	}
	if err = replaceResolverLink(p.previousResolver); err != nil {
		return err
	}
	p.previousResolver = ""
	return nil
}
