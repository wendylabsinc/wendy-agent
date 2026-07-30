package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/sigverify"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// newTestDriverService wires a DriverService against a temp /data store, a fixed
// kernel, an in-memory artifact fetcher, and /bin/true as the apply script so the
// verify/place/apply path runs without a device.
func newTestDriverService(t *testing.T, payload []byte) *DriverService {
	t.Helper()
	tmp := t.TempDir()
	return &DriverService{
		logger:          zap.NewNop(),
		verifier:        sigverify.DefaultVerifier,
		enabledDir:      filepath.Join(tmp, "enabled"),
		modulesDir:      filepath.Join(tmp, "modules-load.d"),
		bakedModulesDir: filepath.Join(tmp, "baked-modules-load.d"),
		applyScript:     "/bin/true",
		unameR:          func() string { return "6.6.0-test" },
		httpGet: func(_ context.Context, _ string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestValidateDriverName(t *testing.T) {
	valid := []string{"wendyos-hello", "hailo_pci", "coral.apex", "a", "Driver-1"}
	for _, n := range valid {
		if err := validateDriverName(n); err != nil {
			t.Errorf("validateDriverName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", ".", "..", "a/b", "a\\b", "../evil", "has space", "sneaky.raw/../x"}
	for _, n := range invalid {
		if err := validateDriverName(n); err == nil {
			t.Errorf("validateDriverName(%q) = nil, want error", n)
		}
	}
}

func TestInstallFromURL_HappyPath(t *testing.T) {
	payload := []byte("fake squashfs .raw bytes")
	svc := newTestDriverService(t, payload)

	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/wendyos-hello.raw",
		ModulesLoad:   []string{"wendyos_hello", "helper"},
	})
	if err != nil {
		t.Fatalf("InstallFromURL: %v", err)
	}

	// The .raw is placed under the verified name (== extension-release name).
	got, err := os.ReadFile(filepath.Join(svc.enabledDir, "wendyos-hello.raw"))
	if err != nil {
		t.Fatalf("reading placed .raw: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("placed .raw = %q, want %q", got, payload)
	}
	// modules-load.d config lists the modules, one per line.
	conf, err := os.ReadFile(filepath.Join(svc.modulesDir, "wendyos-hello.conf"))
	if err != nil {
		t.Fatalf("reading modules conf: %v", err)
	}
	if want := "wendyos_hello\nhelper\n"; string(conf) != want {
		t.Errorf("modules conf = %q, want %q", conf, want)
	}
}

func TestInstallFromURL_SHA256Mismatch(t *testing.T) {
	payload := []byte("real bytes")
	svc := newTestDriverService(t, payload)

	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex([]byte("different bytes")),
		ArtifactURL:   "https://example/x.raw",
	})
	if err == nil {
		t.Fatal("InstallFromURL: got nil error, want sha256 mismatch")
	}
	// Nothing must be placed on a failed verification.
	if _, statErr := os.Stat(filepath.Join(svc.enabledDir, "wendyos-hello.raw")); !os.IsNotExist(statErr) {
		t.Errorf(".raw was placed despite sha256 mismatch")
	}
}

func TestInstallFromURL_KernelMismatch(t *testing.T) {
	payload := []byte("bytes")
	svc := newTestDriverService(t, payload)

	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "9.9.9-other",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/x.raw",
	})
	if err == nil {
		t.Fatal("InstallFromURL: got nil error, want kernel mismatch")
	}
}

func TestInstallFromURL_BadName(t *testing.T) {
	svc := newTestDriverService(t, []byte("bytes"))
	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "../escape",
		KernelVersion: "6.6.0-test",
		ArtifactURL:   "https://example/x.raw",
	})
	if err == nil {
		t.Fatal("InstallFromURL: got nil error, want invalid-name error")
	}
}

func TestListDrivers_InstalledSet(t *testing.T) {
	svc := newTestDriverService(t, nil)
	if err := os.MkdirAll(svc.enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(svc.modulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(svc.enabledDir, n+".raw"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(svc.modulesDir, "alpha.conf"), []byte("# comment\nalpha_mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.ListDrivers(context.Background(), &agentpbv2.ListDriversRequest{})
	if err != nil {
		t.Fatalf("ListDrivers: %v", err)
	}
	if resp.GetKernelVersion() != "6.6.0-test" {
		t.Errorf("kernel = %q, want 6.6.0-test", resp.GetKernelVersion())
	}
	got := map[string][]string{}
	for _, d := range resp.GetInstalled() {
		got[d.GetName()] = d.GetModulesLoad()
	}
	if _, ok := got["alpha"]; !ok {
		t.Errorf("alpha not in installed set: %v", got)
	}
	if _, ok := got["beta"]; !ok {
		t.Errorf("beta not in installed set: %v", got)
	}
	if mods := got["alpha"]; len(mods) != 1 || mods[0] != "alpha_mod" {
		t.Errorf("alpha modules = %v, want [alpha_mod] (comments/blank lines skipped)", mods)
	}
}

func TestListDrivers_BakedInModulesFallback(t *testing.T) {
	// Self-describing add-on: the .raw is installed but its module list is baked
	// in (surfaced at the merged path), with no /data override. ListDrivers must
	// still report the baked-in modules.
	svc := newTestDriverService(t, nil)
	for _, d := range []string{svc.enabledDir, svc.bakedModulesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(svc.enabledDir, "wendyos-hello.raw"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.bakedModulesDir, "wendyos-hello.conf"), []byte("wendyos_hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.ListDrivers(context.Background(), &agentpbv2.ListDriversRequest{})
	if err != nil {
		t.Fatalf("ListDrivers: %v", err)
	}
	var got []string
	for _, d := range resp.GetInstalled() {
		if d.GetName() == "wendyos-hello" {
			got = d.GetModulesLoad()
		}
	}
	if len(got) != 1 || got[0] != "wendyos_hello" {
		t.Errorf("baked-in modules = %v, want [wendyos_hello]", got)
	}
}

func TestListDrivers_DataOverrideWinsOverBaked(t *testing.T) {
	// When both exist, the /data override takes precedence over the baked-in conf
	// (mirrors wendyos-sysext-apply).
	svc := newTestDriverService(t, nil)
	for _, d := range []string{svc.enabledDir, svc.modulesDir, svc.bakedModulesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(svc.enabledDir, "d.raw"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.modulesDir, "d.conf"), []byte("override_mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.bakedModulesDir, "d.conf"), []byte("baked_mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.ListDrivers(context.Background(), &agentpbv2.ListDriversRequest{})
	if err != nil {
		t.Fatalf("ListDrivers: %v", err)
	}
	var got []string
	for _, d := range resp.GetInstalled() {
		if d.GetName() == "d" {
			got = d.GetModulesLoad()
		}
	}
	if len(got) != 1 || got[0] != "override_mod" {
		t.Errorf("modules = %v, want [override_mod] (/data override wins)", got)
	}
}

func TestValidateArtifactURL(t *testing.T) {
	// http stays allowed for on-prem and usb0 registries, but only once the host
	// is opted in (see TestValidateArtifactURL_ExtraHostsOptIn).
	t.Setenv(driverExtraHostsEnv, "registry.example,10.42.0.1")
	ok := []string{"https://registry.example/wendyos-hello.raw", "http://10.42.0.1:8000/x.raw"}
	for _, u := range ok {
		if err := validateArtifactURL(u); err != nil {
			t.Errorf("validateArtifactURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{"file:///etc/passwd", "ftp://h/x", "gopher://h/x", "/no/scheme", "https://"}
	for _, u := range bad {
		if err := validateArtifactURL(u); err == nil {
			t.Errorf("validateArtifactURL(%q) = nil, want error", u)
		}
	}
}

func TestAllModulesLoaded(t *testing.T) {
	loaded := map[string]bool{"wendyos_hello": true, "foo": true}
	// modprobe treats '-' and '_' interchangeably; /proc/modules uses '_'.
	if !allModulesLoaded([]string{"wendyos-hello"}, loaded) {
		t.Error("allModulesLoaded should normalize '-' to '_'")
	}
	if allModulesLoaded([]string{"wendyos_hello", "missing"}, loaded) {
		t.Error("allModulesLoaded should be false when any module is not loaded")
	}
	if allModulesLoaded(nil, loaded) {
		t.Error("allModulesLoaded(nil) should be false (nothing declared to load)")
	}
}

func TestInstallFromURL_ApplyFailureRollsBack(t *testing.T) {
	payload := []byte("bytes")
	svc := newTestDriverService(t, payload)
	svc.applyScript = "/bin/false" // apply exits non-zero

	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/x.raw",
		ModulesLoad:   []string{"wendyos_hello"},
	})
	if err == nil {
		t.Fatal("InstallFromURL: got nil, want apply failure")
	}
	// Nothing must remain installed or declared after a failed apply.
	if _, statErr := os.Stat(filepath.Join(svc.enabledDir, "wendyos-hello.raw")); !os.IsNotExist(statErr) {
		t.Errorf(".raw was left behind after apply failure")
	}
	if _, statErr := os.Stat(filepath.Join(svc.modulesDir, "wendyos-hello.conf")); !os.IsNotExist(statErr) {
		t.Errorf("modules-load conf was left behind after apply failure")
	}
}

// A failed reinstall must restore the working version, not delete it: the
// upgrade renames the new .raw over the old one before apply runs, so a naive
// rollback would destroy a driver that was fine before the operation started.
func TestInstallFromURL_ApplyFailureRestoresPreviousInstall(t *testing.T) {
	oldPayload := []byte("old-working-driver")
	svc := newTestDriverService(t, oldPayload)

	if err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(oldPayload),
		ArtifactURL:   "https://example/old.raw",
		ModulesLoad:   []string{"wendyos_hello"},
	}); err != nil {
		t.Fatalf("seeding the working install: %v", err)
	}

	// Upgrade attempt whose apply fails (e.g. an unrelated add-on's modprobe
	// returns non-zero, which makes the apply script exit 1).
	newPayload := []byte("new-broken-driver")
	svc.httpGet = func(_ context.Context, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(newPayload)), nil
	}
	svc.applyScript = "/bin/false"
	if err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(newPayload),
		ArtifactURL:   "https://example/new.raw",
		ModulesLoad:   []string{"wendyos_hello"},
	}); err == nil {
		t.Fatal("InstallFromURL: got nil, want apply failure")
	}

	got, err := os.ReadFile(filepath.Join(svc.enabledDir, "wendyos-hello.raw"))
	if err != nil {
		t.Fatalf("previous install was destroyed by the failed upgrade: %v", err)
	}
	if !bytes.Equal(got, oldPayload) {
		t.Errorf("restored .raw = %q, want the previous working payload %q", got, oldPayload)
	}
	if _, err := os.Stat(filepath.Join(svc.modulesDir, "wendyos-hello.conf")); err != nil {
		t.Errorf("previous modules-load conf was not restored: %v", err)
	}
	// The backup must not linger where the apply script could see it.
	if entries, _ := os.ReadDir(svc.enabledDir); len(entries) != 1 {
		t.Errorf("enabled dir = %d entries, want exactly the restored .raw", len(entries))
	}
}

// A remove whose apply fails must put the driver back: the add-on is still
// merged into /usr at that point, so dropping it from /data would strand a
// merged-but-unlisted driver that the CLI can no longer remove.
func TestRemoveDriver_ApplyFailureRestoresInstall(t *testing.T) {
	payload := []byte("installed-driver")
	svc := newTestDriverService(t, payload)
	if err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/x.raw",
		ModulesLoad:   []string{"wendyos_hello"},
	}); err != nil {
		t.Fatalf("seeding the install: %v", err)
	}

	svc.applyScript = "/bin/false"
	stream := &fakeRemoveStream{}
	if err := svc.RemoveDriver(&agentpbv2.RemoveDriverRequest{Name: "wendyos-hello"}, stream); err != nil {
		t.Fatalf("RemoveDriver returned a transport error: %v", err)
	}
	if !stream.failed {
		t.Error("RemoveDriver: expected a Failed response when apply fails")
	}

	got, err := os.ReadFile(filepath.Join(svc.enabledDir, "wendyos-hello.raw"))
	if err != nil {
		t.Fatalf("driver was dropped from the store despite the failed unmerge: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("restored .raw = %q, want %q", got, payload)
	}
	if _, err := os.Stat(filepath.Join(svc.modulesDir, "wendyos-hello.conf")); err != nil {
		t.Errorf("modules-load conf was not restored: %v", err)
	}
}

// A successful install must not leave rollback backups behind.
func TestInstallFromURL_SuccessLeavesNoBackups(t *testing.T) {
	payload := []byte("driver")
	svc := newTestDriverService(t, payload)
	for i := 0; i < 2; i++ { // install then reinstall over it
		if err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
			Name:          "wendyos-hello",
			KernelVersion: "6.6.0-test",
			SHA256:        sha256Hex(payload),
			ArtifactURL:   "https://example/x.raw",
			ModulesLoad:   []string{"wendyos_hello"},
		}); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	entries, _ := os.ReadDir(svc.enabledDir)
	if len(entries) != 1 || entries[0].Name() != "wendyos-hello.raw" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("enabled dir = %v, want only wendyos-hello.raw", names)
	}
}

func TestValidateArtifactURL_Rejects(t *testing.T) {
	rejected := map[string]string{
		"cloud metadata":   "http://169.254.169.254/latest/meta-data/",
		"internal service": "http://192.168.2.10:8000/x.raw",
		"loopback":         "http://127.0.0.1:8000/x.raw",
		"arbitrary host":   "https://evil.example/x.raw",
		"embedded creds":   "https://user:pass@storage.googleapis.com/x.raw",
		"file scheme":      "file:///etc/shadow",
		"no host":          "https://",
	}
	for what, u := range rejected {
		if err := validateArtifactURL(u); err == nil {
			t.Errorf("validateArtifactURL(%s = %q) = nil, want an error", what, u)
		}
	}
	// The registry the CLI resolves add-ons from must keep working.
	if err := validateArtifactURL("https://storage.googleapis.com/wendyos-images-public/x.raw"); err != nil {
		t.Errorf("registry URL rejected: %v", err)
	}
}

// Bench and on-prem registries are reachable only when explicitly opted in.
func TestValidateArtifactURL_ExtraHostsOptIn(t *testing.T) {
	const benchURL = "http://169.254.198.132:8000/wendyos-hello.raw"
	if err := validateArtifactURL(benchURL); err == nil {
		t.Fatal("a bench host was accepted without the opt-in")
	}
	t.Setenv(driverExtraHostsEnv, "169.254.198.132, registry.internal")
	if err := validateArtifactURL(benchURL); err != nil {
		t.Errorf("opted-in bench host rejected: %v", err)
	}
	if err := validateArtifactURL("https://registry.internal/x.raw"); err != nil {
		t.Errorf("opted-in registry rejected: %v", err)
	}
	if err := validateArtifactURL("https://other.example/x.raw"); err == nil {
		t.Error("the opt-in must not allow unrelated hosts")
	}
}

// The dial guard is what stops a validated name from being rebound to an
// internal address after the fact.
func TestCheckDialAddress(t *testing.T) {
	internal := []string{"10.0.0.5:443", "192.168.1.7:80", "127.0.0.1:8080", "169.254.169.254:80"}
	for _, addr := range internal {
		if err := checkDialAddress("storage.googleapis.com", addr); err == nil {
			t.Errorf("checkDialAddress(%q) = nil, want a refusal", addr)
		}
	}
	if err := checkDialAddress("storage.googleapis.com", "142.250.180.16:443"); err != nil {
		t.Errorf("public address refused: %v", err)
	}
	// An opted-in host is exempt: reaching a LAN address is why it was named.
	t.Setenv(driverExtraHostsEnv, "registry.internal")
	if err := checkDialAddress("registry.internal", "192.168.1.7:80"); err != nil {
		t.Errorf("opted-in host refused its LAN address: %v", err)
	}
}

// fakeRemoveStream captures what RemoveDriver streams back, so a test can tell
// a Failed response apart from a Completed one.
type fakeRemoveStream struct {
	grpc.ServerStreamingServer[agentpbv2.DriverApplyResponse]
	failed    bool
	completed bool
}

func (f *fakeRemoveStream) Send(resp *agentpbv2.DriverApplyResponse) error {
	switch resp.GetResponseType().(type) {
	case *agentpbv2.DriverApplyResponse_Failed_:
		f.failed = true
	case *agentpbv2.DriverApplyResponse_Completed_:
		f.completed = true
	}
	return nil
}

func (f *fakeRemoveStream) Context() context.Context { return context.Background() }

// The config-partition seed is unauthenticated storage, so it must refuse to
// install while no signing key is embedded, even though the mTLS RPC may not.
func TestSeedInstall_FailsClosedWithoutSigningKey(t *testing.T) {
	payload := []byte("unsigned-driver")
	svc := newTestDriverService(t, payload)
	svc.requireSignature = true // what NewSeedDriverService sets
	if svc.verifier.Enabled() {
		t.Skip("a signing key is embedded; the fail-closed path is not exercised")
	}

	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/x.raw",
	})
	if err == nil {
		t.Fatal("seeded install of an unverifiable driver succeeded; want it refused")
	}
	if !strings.Contains(err.Error(), "cannot be authenticated") {
		t.Errorf("error = %v, want an authenticity refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(svc.enabledDir, "wendyos-hello.raw")); !os.IsNotExist(statErr) {
		t.Error("the refused driver was placed on disk anyway")
	}
}

// The same unsigned add-on is accepted over the mTLS RPC pre-GA (that caller is
// already root-equivalent), so the fail-closed switch must stay opt-in.
func TestRPCInstall_AllowsUnsignedWhileNoKeyEmbedded(t *testing.T) {
	payload := []byte("unsigned-driver")
	svc := newTestDriverService(t, payload)
	if svc.verifier.Enabled() {
		t.Skip("a signing key is embedded; the pre-GA fail-open path is gone")
	}
	if err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:          "wendyos-hello",
		KernelVersion: "6.6.0-test",
		SHA256:        sha256Hex(payload),
		ArtifactURL:   "https://example/x.raw",
	}); err != nil {
		t.Fatalf("RPC-path install: %v", err)
	}
}

// A remote install that declares no kernel version cannot be waved through:
// nothing pins the .ko to this kernel at resolve time.
func TestInstallFromURL_RequiresKernelVersion(t *testing.T) {
	payload := []byte("driver")
	svc := newTestDriverService(t, payload)
	err := svc.InstallFromURL(context.Background(), DriverInstallSpec{
		Name:        "wendyos-hello",
		SHA256:      sha256Hex(payload),
		ArtifactURL: "https://example/x.raw",
	})
	if err == nil {
		t.Fatal("remote install without a kernel version succeeded; want it refused")
	}
	if !strings.Contains(err.Error(), "kernel version") {
		t.Errorf("error = %v, want a kernel-version refusal", err)
	}
}

func TestCheckKernel(t *testing.T) {
	svc := newTestDriverService(t, nil)
	cases := []struct {
		what    string
		kver    string
		remote  bool
		wantErr bool
	}{
		{"remote must declare one", "", true, true},
		{"local file may omit it", "", false, false},
		{"matching version passes", "6.6.0-test", true, false},
		{"mismatch refused", "5.10.0-other", true, true},
		{"mismatch refused locally too", "5.10.0-other", false, true},
	}
	for _, tc := range cases {
		if err := svc.checkKernel("d", tc.kver, tc.remote); (err != nil) != tc.wantErr {
			t.Errorf("%s: checkKernel(%q, remote=%v) = %v, wantErr=%v", tc.what, tc.kver, tc.remote, err, tc.wantErr)
		}
	}
}
