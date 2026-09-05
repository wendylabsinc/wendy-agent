package containerd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestResolvedStubAvailable(t *testing.T) {
	statOK := func(string) (os.FileInfo, error) { return nil, nil }
	dialOK := func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }

	if !resolvedStubAvailable(statOK, dialOK) {
		t.Fatal("existing stub config plus accepting listener should be available")
	}

	dialled := false
	if resolvedStubAvailable(
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		func() (io.Closer, error) {
			dialled = true
			return nil, nil
		},
	) {
		t.Fatal("missing stub config must be unavailable")
	}
	if dialled {
		t.Fatal("missing stub config should short-circuit before dialing loopback")
	}

	if resolvedStubAvailable(statOK, func() (io.Closer, error) {
		return nil, errors.New("connection refused")
	}) {
		t.Fatal("non-listening stub must be unavailable")
	}
}

func TestResolvSearchDomains(t *testing.T) {
	tests := []struct {
		name string
		conf string
		want []string
	}{
		{
			name: "search domains",
			conf: "nameserver 192.0.2.53\nsearch localdomain corp.example\n",
			want: []string{"localdomain", "corp.example"},
		},
		{
			name: "domain directive",
			conf: "domain lab.example\n",
			want: []string{"lab.example"},
		},
		{
			name: "last directive wins",
			conf: "search old.example\ndomain middle.example\nSEARCH new.example vpn.example\n",
			want: []string{"new.example", "vpn.example"},
		},
		{
			name: "comments and malformed directives",
			conf: "# search ignored.example\nsearch\n; domain ignored.example\nsearch home.example # learned from DHCP\n",
			want: []string{"home.example"},
		},
		{
			name: "no search domains",
			conf: "nameserver 127.0.0.53\noptions edns0 trust-ad\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvSearchDomains(strings.NewReader(tt.conf))
			if err != nil {
				t.Fatalf("resolvSearchDomains: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("domains = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvSearchDomainsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stub-resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 127.0.0.53\nsearch lan.example vpn.example\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := resolvSearchDomainsFile(path); strings.Join(got, "|") != "lan.example|vpn.example" {
		t.Fatalf("domains = %q, want [lan.example vpn.example]", got)
	}
	if got := resolvSearchDomainsFile(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing resolver domains = %q, want nil", got)
	}
}

func TestRenderHostResolvConf(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		search  string
	}{
		{name: "known domains", domains: []string{"localdomain", "corp.example"}, search: "search localdomain corp.example\n"},
		{name: "no domains", search: "search .\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderHostResolvConf(tt.domains)
			if !strings.HasSuffix(got, tt.search) {
				t.Fatalf("resolv.conf = %q, want suffix %q", got, tt.search)
			}
			if count := strings.Count(got, "nameserver "); count != 1 {
				t.Fatalf("resolv.conf has %d nameservers, want exactly the managed stub", count)
			}
			if !strings.Contains(got, "nameserver 127.0.0.53\n") {
				t.Fatalf("resolv.conf does not point to the systemd-resolved stub: %q", got)
			}
		})
	}
}

func TestWriteHostResolvConfIn(t *testing.T) {
	dir := t.TempDir()
	domains := []string{"localdomain", "corp.example"}
	path, err := writeHostResolvConfIn(dir, domains)
	if err != nil {
		t.Fatalf("writeHostResolvConfIn: %v", err)
	}
	if want := filepath.Join(dir, hostResolvConfName); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := renderHostResolvConf(domains); string(data) != want {
		t.Fatalf("resolv.conf = %q, want %q", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != hostResolvConfName {
		t.Fatalf("unexpected files after atomic write: %v", entries)
	}
}

func TestRecreateHostResolvConfIn(t *testing.T) {
	t.Run("recreates the exact managed mount source", func(t *testing.T) {
		dir := t.TempDir()
		path, err := writeHostResolvConfIn(dir, nil)
		if err != nil {
			t.Fatalf("writeHostResolvConfIn: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		mounts := []specs.Mount{{
			Destination: "/etc/resolv.conf",
			Source:      path,
			Type:        "bind",
		}}
		domains := []string{"reboot.example"}
		ok, err := recreateHostResolvConfIn(dir, mounts, domains)
		if err != nil {
			t.Fatalf("recreateHostResolvConfIn: %v", err)
		}
		if !ok {
			t.Fatal("managed mount was not recognized")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != renderHostResolvConf(domains) {
			t.Fatalf("recreated resolv.conf = %q, err %v", data, err)
		}
	})

	t.Run("ignores unrelated resolv mounts", func(t *testing.T) {
		dir := t.TempDir()
		mounts := []specs.Mount{{
			Destination: "/etc/resolv.conf",
			Source:      filepath.Join(dir+"-other", hostResolvConfName),
			Type:        "bind",
		}}
		ok, err := recreateHostResolvConfIn(dir, mounts, []string{"ignored.example"})
		if err != nil {
			t.Fatalf("recreateHostResolvConfIn: %v", err)
		}
		if ok {
			t.Fatal("unrelated resolver mount was treated as managed")
		}
		if _, err := os.Stat(filepath.Join(dir, hostResolvConfName)); !os.IsNotExist(err) {
			t.Fatalf("managed path should not be written, stat err = %v", err)
		}
	})
}
