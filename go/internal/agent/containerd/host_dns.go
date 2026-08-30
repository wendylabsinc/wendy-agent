package containerd

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"go.uber.org/zap"
)

const (
	// systemdResolvedStubConfig is created by systemd-resolved while its local
	// stub resolver is available. Checking the file as well as the listener
	// avoids a loopback dial on hosts that do not install systemd-resolved.
	systemdResolvedStubConfig  = "/run/systemd/resolve/stub-resolv.conf"
	systemdResolvedStubAddress = "127.0.0.53:53"

	// hostResolvConfDir holds the static resolver file bind-mounted into
	// host-network containers. It lives under /run because it is derived state;
	// recreateHostResolvConfForStart restores it before NewTask after a reboot.
	hostResolvConfDir  = "/run/wendy/host-dns"
	hostResolvConfName = "resolv.conf"

	// The resolver address is stable while systemd-resolved dynamically tracks
	// DHCP, VPN, and other per-link upstreams. Search domains are copied from
	// stub-resolv.conf when the managed file is created or recreated; upstream
	// nameservers are deliberately never copied.
	hostResolvConfHeader = `# Managed by wendy-agent for host-network containers.
nameserver 127.0.0.53
options edns0 trust-ad
`
)

// resolvedStubAvailable verifies both that systemd-resolved has created its
// runtime configuration and that its TCP DNS listener is accepting
// connections. The listener supports both TCP and UDP; TCP gives us an actual
// accept/refuse signal whereas connecting a UDP socket proves nothing.
// Dependencies are injected so the decision is deterministic in unit tests.
func resolvedStubAvailable(
	stat func(string) (os.FileInfo, error),
	dial func() (io.Closer, error),
) bool {
	if _, err := stat(systemdResolvedStubConfig); err != nil {
		return false
	}
	conn, err := dial()
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func systemdResolvedStubAvailable() bool {
	return resolvedStubAvailable(os.Stat, func() (io.Closer, error) {
		return net.DialTimeout("tcp", systemdResolvedStubAddress, 250*time.Millisecond)
	})
}

// resolvSearchDomains extracts the effective search/domain directive from a
// resolv.conf. The two directives are mutually exclusive and the last one
// wins, matching resolv.conf(5). Comments and malformed directives are
// ignored. Nameserver lines are never returned: host-network containers must
// always query the stable systemd-resolved stub instead of pinning an upstream.
func resolvSearchDomains(r io.Reader) ([]string, error) {
	var domains []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if comment := strings.IndexAny(line, "#;"); comment >= 0 {
			line = line[:comment]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "search":
			domains = append([]string(nil), fields[1:]...)
		case "domain":
			domains = []string{fields[1]}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading resolver search domains: %w", err)
	}
	return domains, nil
}

func resolvSearchDomainsFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	domains, err := resolvSearchDomains(f)
	if err != nil {
		return nil
	}
	return domains
}

func systemdResolvedSearchDomains() []string {
	return resolvSearchDomainsFile(systemdResolvedStubConfig)
}

func renderHostResolvConf(searchDomains []string) string {
	if len(searchDomains) == 0 {
		return hostResolvConfHeader + "search .\n"
	}
	return hostResolvConfHeader + "search " + strings.Join(searchDomains, " ") + "\n"
}

// writeHostResolvConfIn atomically writes the static systemd-resolved client
// configuration beneath baseDir and returns its path. Atomic replacement is
// safe for already-running containers: each container keeps the complete file
// inode it started with, while newly started containers receive the latest
// known host search domains.
func writeHostResolvConfIn(baseDir string, searchDomains []string) (string, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("creating host DNS directory: %w", err)
	}
	path := filepath.Join(baseDir, hostResolvConfName)
	tmp, err := os.CreateTemp(baseDir, ".resolv-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temporary host resolv.conf: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(renderHostResolvConf(searchDomains)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("writing host resolv.conf: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", fmt.Errorf("setting host resolv.conf permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing host resolv.conf: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("installing host resolv.conf: %w", err)
	}
	return path, nil
}

func writeHostResolvConf() (string, error) {
	return writeHostResolvConfIn(hostResolvConfDir, systemdResolvedSearchDomains())
}

// hostResolvMountSource reports whether mounts contains exactly the
// wendy-managed host resolver mount. Exact matching prevents an unrelated
// resolv.conf under a similarly named directory from triggering host writes.
func hostResolvMountSource(mounts []specs.Mount, baseDir string) (string, bool) {
	want := filepath.Join(baseDir, hostResolvConfName)
	for _, mount := range mounts {
		if mount.Destination == "/etc/resolv.conf" && mount.Source == want {
			return want, true
		}
	}
	return "", false
}

func recreateHostResolvConfIn(baseDir string, mounts []specs.Mount, searchDomains []string) (bool, error) {
	if _, ok := hostResolvMountSource(mounts, baseDir); !ok {
		return false, nil
	}
	_, err := writeHostResolvConfIn(baseDir, searchDomains)
	return true, err
}

// recreateHostResolvConfForStart restores the /run-backed bind source after a
// reboot, before container.NewTask asks the runtime to mount it.
func (c *Client) recreateHostResolvConfForStart(mounts []specs.Mount) {
	if ok, err := recreateHostResolvConfIn(hostResolvConfDir, mounts, systemdResolvedSearchDomains()); ok && err != nil {
		c.logger.Warn("host DNS: could not recreate resolv.conf before container start", zap.Error(err))
	}
}
