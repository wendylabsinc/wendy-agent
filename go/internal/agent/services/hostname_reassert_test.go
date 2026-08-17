package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// avahiFixture is the shipped wendyos-mdns.service template after the boot-time
// publisher (update-mdns-uuid.sh) has filled in the generated device name — i.e.
// exactly the state a renamed device comes back in after a reboot.
const avahiFixture = `<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">%h</name>
  <service protocol="any">
    <type>_wendyos._udp</type>
    <port>50052</port>
    <txt-record>id=3f2b1a7c</txt-record>
    <txt-record>name=brave-dolphin</txt-record>
    <txt-record>displayname=Brave Dolphin</txt-record>
    <txt-record>tls=true</txt-record>
  </service>
</service-group>
`

// reassertFixture writes the explicit-hostname and avahi service files into a
// temp dir and returns their paths. An empty hostname means "no rename in
// effect": the file is not created at all.
func reassertFixture(t *testing.T, hostname, serviceContent string) (hostnamePath, servicePath string) {
	t.Helper()
	dir := t.TempDir()
	hostnamePath = filepath.Join(dir, "hostname")
	servicePath = filepath.Join(dir, "wendyos-mdns.service")

	if hostname != "" {
		if err := os.WriteFile(hostnamePath, []byte(hostname+"\n"), 0o644); err != nil {
			t.Fatalf("writing hostname fixture: %v", err)
		}
	}
	if serviceContent != "" {
		if err := os.WriteFile(servicePath, []byte(serviceContent), 0o644); err != nil {
			t.Fatalf("writing avahi fixture: %v", err)
		}
	}
	return hostnamePath, servicePath
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// A rename must survive a reboot. The hostname itself does (it is persisted to
// /etc/wendy-agent and generate-hostname.sh prefers it), but the avahi service
// file lives on the rootfs and is rewritten from /etc/wendyos/device-name by
// wendyos-identity.service on every boot — reverting the name/displayname TXT
// records that 'wendy device list' displays. Re-asserting at startup fixes it.
func TestReassertHostnameAdvertisementRestoresRevertedRecords(t *testing.T) {
	hostnamePath, servicePath := reassertFixture(t, "kitchen-pi", avahiFixture)

	restarts := 0
	reassertHostnameAdvertisement(zap.NewNop(), hostnamePath, servicePath, func() bool {
		restarts++
		return true
	})

	got := readFile(t, servicePath)
	if !strings.Contains(got, "<txt-record>name=kitchen-pi</txt-record>") {
		t.Errorf("name TXT record not restored:\n%s", got)
	}
	if !strings.Contains(got, "<txt-record>displayname=Kitchen Pi</txt-record>") {
		t.Errorf("displayname TXT record not restored:\n%s", got)
	}
	// Records the agent owns elsewhere must survive untouched.
	if !strings.Contains(got, "<txt-record>tls=true</txt-record>") {
		t.Errorf("tls TXT record was clobbered:\n%s", got)
	}
	if !strings.Contains(got, "<port>50052</port>") {
		t.Errorf("port was clobbered:\n%s", got)
	}
	if restarts != 1 {
		t.Errorf("avahi restarts = %d, want 1", restarts)
	}
}

// The common case is a device that was never renamed. Doing nothing matters:
// this runs on every agent start, and a needless avahi-daemon restart would
// drop the device's mDNS advertisement for no reason.
func TestReassertHostnameAdvertisementNoExplicitHostname(t *testing.T) {
	_, servicePath := reassertFixture(t, "", avahiFixture)
	hostnamePath := filepath.Join(t.TempDir(), "absent")

	restarts := 0
	reassertHostnameAdvertisement(zap.NewNop(), hostnamePath, servicePath, func() bool {
		restarts++
		return true
	})

	if got := readFile(t, servicePath); got != avahiFixture {
		t.Errorf("service file modified with no rename in effect:\n%s", got)
	}
	if restarts != 0 {
		t.Errorf("avahi restarts = %d, want 0", restarts)
	}
}

// Second and later boots after a rename: the records already agree, so this is
// a no-op — again, no avahi restart.
func TestReassertHostnameAdvertisementAlreadyCurrent(t *testing.T) {
	current := strings.NewReplacer(
		"name=brave-dolphin", "name=kitchen-pi",
		"displayname=Brave Dolphin", "displayname=Kitchen Pi",
	).Replace(avahiFixture)
	hostnamePath, servicePath := reassertFixture(t, "kitchen-pi", current)

	restarts := 0
	reassertHostnameAdvertisement(zap.NewNop(), hostnamePath, servicePath, func() bool {
		restarts++
		return true
	})

	if got := readFile(t, servicePath); got != current {
		t.Errorf("service file rewritten when already current:\n%s", got)
	}
	if restarts != 0 {
		t.Errorf("avahi restarts = %d, want 0", restarts)
	}
}

// A corrupt persisted hostname must not be advertised. Leaving the boot-time
// device-name in place is the safe outcome.
func TestReassertHostnameAdvertisementInvalidHostname(t *testing.T) {
	for _, invalid := range []string{"Kitchen Pi", "-leading", "trailing-", "1digit", "under_score", ""} {
		t.Run(invalid, func(t *testing.T) {
			dir := t.TempDir()
			hostnamePath := filepath.Join(dir, "hostname")
			servicePath := filepath.Join(dir, "wendyos-mdns.service")
			if err := os.WriteFile(hostnamePath, []byte(invalid+"\n"), 0o644); err != nil {
				t.Fatalf("writing hostname fixture: %v", err)
			}
			if err := os.WriteFile(servicePath, []byte(avahiFixture), 0o644); err != nil {
				t.Fatalf("writing avahi fixture: %v", err)
			}

			restarts := 0
			reassertHostnameAdvertisement(zap.NewNop(), hostnamePath, servicePath, func() bool {
				restarts++
				return true
			})

			if got := readFile(t, servicePath); got != avahiFixture {
				t.Errorf("service file modified for invalid hostname %q:\n%s", invalid, got)
			}
			if restarts != 0 {
				t.Errorf("avahi restarts = %d, want 0", restarts)
			}
		})
	}
}

// A non-WendyOS host (the Linux desktop install) has no avahi service file.
// That is not an error worth failing startup over.
func TestReassertHostnameAdvertisementMissingServiceFile(t *testing.T) {
	dir := t.TempDir()
	hostnamePath := filepath.Join(dir, "hostname")
	if err := os.WriteFile(hostnamePath, []byte("kitchen-pi\n"), 0o644); err != nil {
		t.Fatalf("writing hostname fixture: %v", err)
	}

	restarts := 0
	reassertHostnameAdvertisement(zap.NewNop(), hostnamePath, filepath.Join(dir, "absent.service"), func() bool {
		restarts++
		return true
	})

	if restarts != 0 {
		t.Errorf("avahi restarts = %d, want 0", restarts)
	}
}

func TestReassertHostnameAdvertisementDoesNotLogSuccessWhenRestartFails(t *testing.T) {
	hostnamePath, servicePath := reassertFixture(t, "kitchen-pi", avahiFixture)
	core, logs := observer.New(zapcore.InfoLevel)

	reassertHostnameAdvertisement(zap.New(core), hostnamePath, servicePath, func() bool { return false })

	if got := logs.FilterMessage("Re-applied renamed hostname to the mDNS advertisement").Len(); got != 0 {
		t.Errorf("success logs = %d, want 0", got)
	}
}
