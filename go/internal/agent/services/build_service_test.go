package services

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// tarWith builds a one-entry tar with the given entry name, used to probe the
// extractor's path handling.
func tarWith(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1}); err != nil {
		t.Fatalf("writing header: %v", err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatalf("writing body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	return buf.Bytes()
}

func TestContextDir_StableAcrossCalls(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})

	first, err := svc.contextDir("myapp")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	second, err := svc.contextDir("myapp")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	if first != second {
		t.Fatalf("context dir must be stable per app (%q vs %q): BuildKit keys its local-source cache on this path, so a fresh temp dir re-transfers the whole context every build", first, second)
	}
}

// Sanitising drops characters, so distinct app ids can reduce to the same name.
// Two unrelated apps sharing one context directory would clear and re-extract
// over each other.
func TestContextDir_DistinctAppIDsDoNotCollide(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})

	dotted, err := svc.contextDir("sh.wendy.app")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	// Sanitises to the identical string "shwendyapp".
	squashed, err := svc.contextDir("shwendyapp")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	if dotted == squashed {
		t.Fatalf("app ids that sanitise alike must still get separate context directories, both got %q", dotted)
	}
}

func TestContextDir_RejectsTraversalInAppID(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})

	// After sanitising, "../.." has no legal characters left, so it must be
	// rejected rather than resolving to the state dir's parent.
	if _, err := svc.contextDir("../.."); err == nil {
		t.Fatal("an app id that sanitises to nothing must be rejected")
	}
}

func TestExtractContextTar_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(tarWith(t, "../escape.txt")), dir); err == nil {
		t.Fatal("a tar entry escaping the context root must be rejected, not written")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.txt")); err == nil {
		t.Fatal("the escaping entry was actually written outside the context root")
	}
}

func TestExtractContextTar_RejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(tarWith(t, "/etc/passwd")), dir); err == nil {
		t.Fatal("an absolute tar entry must be rejected")
	}
}

func TestExtractContextTar_WritesOrdinaryEntries(t *testing.T) {
	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(tarWith(t, "sub/app.py")), dir); err != nil {
		t.Fatalf("extractContextTar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "app.py")); err != nil {
		t.Fatalf("ordinary nested entry was not written: %v", err)
	}
}

func TestBuildctlArgs_SortedAndPushing(t *testing.T) {
	args, err := buildctlArgs("/ctx", "Dockerfile", "linux/arm64",
		"127.0.0.1:41000/myapp:latest", map[string]string{"FOO": "bar", "ABC": "1"})
	if err != nil {
		t.Fatalf("buildctlArgs: %v", err)
	}
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/ctx",
		"--local", "dockerfile=/ctx",
		"--opt", "filename=Dockerfile",
		"--opt", "platform=linux/arm64",
		"--opt", "build-arg:ABC=1",
		"--opt", "build-arg:FOO=bar",
		"--output", "type=image,name=127.0.0.1:41000/myapp:latest,push=true",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("buildctlArgs mismatch:\n got: %v\nwant: %v", args, want)
	}
}

// The agent re-validates rather than trusting the CLI's validation.
func TestBuildctlArgs_RejectsFlagInjectionBuildArg(t *testing.T) {
	if _, err := buildctlArgs("/ctx", "Dockerfile", "linux/arm64", "127.0.0.1:41000/a:latest",
		map[string]string{"FOO": "-rm-rf"}); err == nil {
		t.Fatal("the agent must re-validate build args, not trust the client's validation")
	}
}

// The platform is client-supplied like everything else, and reaches the
// streamed build log, where control characters can forge lines.
func TestBuildctlArgs_RejectsMalformedPlatform(t *testing.T) {
	for _, platform := range []string{"", "linux", "linux/arm64\nFAKE LOG LINE", "linux/arm64/v8/extra", "LINUX/ARM64"} {
		if _, err := buildctlArgs("/ctx", "Dockerfile", platform, "127.0.0.1:41000/a:latest", nil); err == nil {
			t.Errorf("platform %q must be rejected", platform)
		}
	}
	for _, platform := range []string{"linux/arm64", "linux/amd64", "linux/arm/v7"} {
		if _, err := buildctlArgs("/ctx", "Dockerfile", platform, "127.0.0.1:41000/a:latest", nil); err != nil {
			t.Errorf("platform %q must be accepted, got: %v", platform, err)
		}
	}
}

// The name checks constrain where an entry lands, not where a link points, and
// whether BuildKit follows one out of the context is not ours to decide.
func TestExtractContextTar_RejectsSymlinkEntries(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777,
	}); err != nil {
		t.Fatalf("writing symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(buf.Bytes()), dir); err == nil {
		t.Fatal("a symlink entry must be rejected, not silently written as an empty file")
	}
}

func TestBuildctlArgs_RejectsDockerfileEscapingContext(t *testing.T) {
	if _, err := buildctlArgs("/ctx", "../../etc/shadow", "linux/arm64",
		"127.0.0.1:41000/a:latest", nil); err == nil {
		t.Fatal("a dockerfile name escaping the context must be rejected")
	}
}

func TestBuildctlArgs_RejectsAbsoluteDockerfile(t *testing.T) {
	if _, err := buildctlArgs("/ctx", "/etc/shadow", "linux/arm64",
		"127.0.0.1:41000/a:latest", nil); err == nil {
		t.Fatal("an absolute dockerfile path must be rejected")
	}
}

func TestRedactBuildctlArgs_MasksValuesKeepsKeys(t *testing.T) {
	out := redactBuildctlArgs([]string{"--opt", "build-arg:TOKEN=secret", "--output", "type=image,name=x"})
	if slices.Contains(out, "build-arg:TOKEN=secret") {
		t.Fatal("build-arg value was not redacted; these reach the agent log")
	}
	if !slices.Contains(out, "build-arg:TOKEN=<redacted>") {
		t.Fatalf("the key must survive for debugging, got %v", out)
	}
	if !slices.Contains(out, "type=image,name=x") {
		t.Fatalf("non-build-arg tokens must be preserved, got %v", out)
	}
}

func enabledService(t *testing.T) *BuildService {
	t.Helper()
	return NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
		// A dialer that never gets used by these tests, but must be present:
		// BuildImage refuses up front when it has no way to deliver, which
		// would otherwise mask the errors under test.
		Peers: stubPeerDialer{err: errors.New("not dialed in this test")},
	})
}

// A build host with no way to reach peers must say so rather than build first
// and discover it at push time.
func TestBuildImage_RejectsWhenNoPeerDialer(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})
	err := svc.BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppId:      "app",
		Platform:   "linux/arm64",
		PushTarget: &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "app:latest"},
		Definition: &agentpbv2.BuildSpec_DockerfileBuild{
			DockerfileBuild: &agentpbv2.DockerfileBuild{Dockerfile: "Dockerfile"},
		},
	}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition when the host cannot dial peers", err)
	}
}

func TestBuildImage_RejectsSpecWithoutDefinition(t *testing.T) {
	err := enabledService(t).BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppId:      "app",
		Platform:   "linux/arm64",
		PushTarget: &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "app:latest"},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument for a spec with no build definition", err)
	}
}

func TestBuildImage_RejectsMissingSpec(t *testing.T) {
	err := enabledService(t).BuildImage(&stubBuildStream{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument when the first message carries no spec", err)
	}
}

// The destination is checked before the context is even reassembled, so a bad
// one costs nothing.
func TestBuildImage_RejectsBadPushDestinationBeforeBuilding(t *testing.T) {
	err := enabledService(t).BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppId:      "app",
		Platform:   "linux/arm64",
		PushTarget: &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "evil.example.com/exfil:latest"},
		Definition: &agentpbv2.BuildSpec_DockerfileBuild{
			DockerfileBuild: &agentpbv2.DockerfileBuild{Dockerfile: "Dockerfile"},
		},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument — the destination must be validated before any build runs", err)
	}
}

// ctxWithIdentity builds a gRPC context carrying an mTLS peer whose leaf has
// the given wendy identity URN, mirroring how the agent sees a real caller.
func ctxWithIdentity(t *testing.T, urn string) context.Context {
	t.Helper()
	uri, err := url.Parse(urn)
	if err != nil {
		t.Fatalf("parsing urn: %v", err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{uri}}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}},
		},
	})
}

func TestSetBuildHostEnabled_UserCertMayEnable(t *testing.T) {
	dir := t.TempDir()
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: dir, StateDir: t.TempDir()})

	resp, err := svc.SetBuildHostEnabled(ctxWithIdentity(t, "urn:wendy:org:2:user:alice"),
		&agentpbv2.SetBuildHostEnabledRequest{Enabled: true})
	if err != nil {
		t.Fatalf("SetBuildHostEnabled: %v", err)
	}
	if !resp.GetEnabled() {
		t.Fatal("response must echo the state now in effect")
	}
	if !svc.builderEnabled() {
		t.Fatal("the builder role must actually be on afterwards")
	}
}

func TestSetBuildHostEnabled_TakesEffectWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: dir, StateDir: t.TempDir()})
	ctx := ctxWithIdentity(t, "urn:wendy:org:2:user:alice")

	if _, err := svc.SetBuildHostEnabled(ctx, &agentpbv2.SetBuildHostEnabledRequest{Enabled: true}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	caps, err := svc.GetBuildCapabilities(ctx, &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if !caps.GetBuilderEnabled() {
		t.Fatal("capabilities must reflect the new state on the very next call, with no agent restart")
	}

	if _, err := svc.SetBuildHostEnabled(ctx, &agentpbv2.SetBuildHostEnabledRequest{Enabled: false}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if svc.builderEnabled() {
		t.Fatal("disable must take effect immediately too")
	}
}

// The gate would be decorative if a device cert could flip it: anything able to
// call BuildImage could simply call this first.
func TestSetBuildHostEnabled_RefusesDeviceCert(t *testing.T) {
	dir := t.TempDir()
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: dir, StateDir: t.TempDir()})

	_, err := svc.SetBuildHostEnabled(ctxWithIdentity(t, "urn:wendy:org:2:asset:345"),
		&agentpbv2.SetBuildHostEnabledRequest{Enabled: true})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got %v, want PermissionDenied for an asset certificate", err)
	}
	if svc.builderEnabled() {
		t.Fatal("a refused call must not have enabled anything")
	}
}

func TestSetBuildHostEnabled_RefusesNoIdentity(t *testing.T) {
	dir := t.TempDir()
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: dir, StateDir: t.TempDir()})

	_, err := svc.SetBuildHostEnabled(context.Background(),
		&agentpbv2.SetBuildHostEnabledRequest{Enabled: true})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got %v, want PermissionDenied without mTLS", err)
	}
}

func TestSetBuildHostEnabled_DisableIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: dir, StateDir: t.TempDir()})
	ctx := ctxWithIdentity(t, "urn:wendy:org:2:user:alice")

	// Disabling something already disabled must not error.
	if _, err := svc.SetBuildHostEnabled(ctx, &agentpbv2.SetBuildHostEnabledRequest{Enabled: false}); err != nil {
		t.Fatalf("disabling an already-disabled host must be a no-op, got: %v", err)
	}
}

func TestBuildImage_RejectsEmptyContext(t *testing.T) {
	err := enabledService(t).BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppId:      "app",
		Platform:   "linux/arm64",
		PushTarget: &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "app:latest"},
		Definition: &agentpbv2.BuildSpec_DockerfileBuild{
			DockerfileBuild: &agentpbv2.DockerfileBuild{Dockerfile: "Dockerfile"},
		},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument for a spec with no build context", err)
	}
}

// oversizedChunkSource pretends to hold a context larger than the ceiling,
// without allocating one: it streams zeros forever, which is exactly the shape
// of the hazard — the agent must stop reading rather than read until it dies.
type oversizedChunkSource struct{ read *int64 }

func (o oversizedChunkSource) OpenChunkStream(context.Context, [][32]byte) io.Reader {
	return &countingReader{n: o.read}
}

type countingReader struct{ n *int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	*c.n += int64(len(p))
	return len(p), nil
}

// testMaxContextBytes is small on purpose. Proving the read is bounded means
// reading right up to the ceiling, so testing at the real 2 GiB would allocate
// 2 GiB — fine on a workstation, an out-of-memory kill on a CI runner.
const testMaxContextBytes = 1 << 20

// A build context is reassembled into memory in one piece on a device that may
// have only a few GiB of it, so an unbounded read is an OOM the client chooses.
func TestReassembleContext_BoundsMemoryAgainstUndeclaredSize(t *testing.T) {
	var read int64
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		StateDir:        t.TempDir(),
		MaxContextBytes: testMaxContextBytes,
		Chunks:          oversizedChunkSource{read: &read},
	})

	// TotalSize 0 means "undeclared", so the declared-size check cannot be what
	// stops this — the read itself has to be bounded.
	_, err := svc.reassembleContext(context.Background(), &agentpbv2.ChunkManifest{
		ChunkHashes: [][]byte{make([]byte, 32)},
		TotalSize:   0,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument for a context past the ceiling", err)
	}
	// Slack for io.ReadAll's growth: it reads in chunks and may overshoot the
	// limit by one buffer before the LimitReader reports EOF.
	if read > testMaxContextBytes+int64(1<<20) {
		t.Fatalf("read %d bytes for a %d-byte ceiling: the read is not bounded", read, int64(testMaxContextBytes))
	}
}

// The default must be the real ceiling, not whatever a test injected — the
// option exists for tests, and a zero value must not disable the bound.
func TestNewBuildService_DefaultsToRealContextCeiling(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: t.TempDir()})
	if svc.maxContextBytes != maxContextBytes {
		t.Fatalf("maxContextBytes = %d, want the %d-byte default", svc.maxContextBytes, int64(maxContextBytes))
	}
}

// A declared oversize is refused before any I/O, so the pathological case is
// cheap rather than merely survivable.
func TestReassembleContext_RejectsDeclaredOversizeWithoutReading(t *testing.T) {
	var read int64
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		StateDir:        t.TempDir(),
		MaxContextBytes: testMaxContextBytes,
		Chunks:          oversizedChunkSource{read: &read},
	})

	_, err := svc.reassembleContext(context.Background(), &agentpbv2.ChunkManifest{
		ChunkHashes: [][]byte{make([]byte, 32)},
		TotalSize:   testMaxContextBytes + 1,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument for a declared oversize", err)
	}
	if read != 0 {
		t.Fatalf("read %d bytes; a declared oversize must cost no I/O at all", read)
	}
}

// Two builds sharing a context directory must not overlap: BuildImage clears
// and re-extracts that directory while buildctl reads it for the whole build,
// so an interleaving hands one developer's sources to another's image.
func TestLockContextDir_SerialisesSameDirectory(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})

	unlock := svc.lockContextDir("/ctx/app")
	acquired := make(chan struct{})
	go func() {
		defer close(acquired)
		svc.lockContextDir("/ctx/app")()
	}()

	select {
	case <-acquired:
		t.Fatal("a second build acquired the same context directory while the first still held it")
	case <-time.After(50 * time.Millisecond):
	}

	unlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the lock was never released to the waiting build")
	}
}

// Different apps must still build concurrently; the lock is per context
// directory, not a global build queue.
func TestLockContextDir_DoesNotSerialiseDifferentDirectories(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
	})

	defer svc.lockContextDir("/ctx/one")()
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.lockContextDir("/ctx/two")()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an unrelated app's build was blocked; the lock must be per context directory")
	}
}

// The registry hop must be pinned to the asset the image was built for. A
// config that only validates the chain would let any org-issued certificate
// terminate it, and the mesh LAN path picks its peer from an unauthenticated
// mDNS record — so the target's asset id has to reach the TLS config.
func TestBuildImage_PinsPushCredentialsToTheTargetAsset(t *testing.T) {
	var gotAssetID int32 = -1
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath: enabledConfigDir(t),
		StateDir:   t.TempDir(),
		Peers:      stubPeerDialer{err: errors.New("not dialed in this test")},
		// A real tar: extraction runs before the push credentials are loaded, so
		// a bogus context would fail earlier and the assertion would never run.
		Chunks: staticChunkSource{data: tarWith(t, "Dockerfile")},
		PushTLS: func(targetAssetID int32) (*tls.Config, error) {
			gotAssetID = targetAssetID
			// Failing here stops the build before it shells out to buildctl; the
			// assertion under test is the argument, not what follows.
			return nil, errors.New("stop here")
		},
	})

	_ = svc.BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppId:      "app",
		Platform:   "linux/arm64",
		PushTarget: &agentpbv2.PushTarget{AssetId: 214, RegistryPort: 5000, Repository: "app:latest"},
		Context:    &agentpbv2.ChunkManifest{ChunkHashes: [][]byte{make([]byte, 32)}},
		Definition: &agentpbv2.BuildSpec_DockerfileBuild{
			DockerfileBuild: &agentpbv2.DockerfileBuild{Dockerfile: "Dockerfile"},
		},
	}})

	if gotAssetID != 214 {
		t.Fatalf("push credentials were requested for asset %d, want the push target's 214: an unpinned config lets any org-issued cert receive the image", gotAssetID)
	}
}

// staticChunkSource returns fixed bytes as the reassembled context.
type staticChunkSource struct{ data []byte }

func (s staticChunkSource) OpenChunkStream(context.Context, [][32]byte) io.Reader {
	return bytes.NewReader(s.data)
}

// stubBuildStream is a fake BuildImage server stream for unit tests.
type stubBuildStream struct {
	agentpbv2.WendyBuildService_BuildImageServer
	spec  *agentpbv2.BuildSpec
	sent  []*agentpbv2.BuildImageProgress
	recvd bool
}

func (s *stubBuildStream) Context() context.Context { return context.Background() }

func (s *stubBuildStream) Recv() (*agentpbv2.BuildImageRequest, error) {
	if s.recvd {
		return nil, io.EOF
	}
	s.recvd = true
	return &agentpbv2.BuildImageRequest{Spec: s.spec}, nil
}

func (s *stubBuildStream) Send(p *agentpbv2.BuildImageProgress) error {
	s.sent = append(s.sent, p)
	return nil
}

// enabledConfigDir returns a config dir with the builder opt-in marker present.
func enabledConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, buildHostEnabledFile), nil, 0o600); err != nil {
		t.Fatalf("writing opt-in marker: %v", err)
	}
	return dir
}

func TestGetBuildCapabilities_DisabledByDefault(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: t.TempDir()})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuilderEnabled() {
		t.Fatal("a device must not report itself as a builder unless explicitly opted in: being reachable is not volunteering")
	}
}

func TestGetBuildCapabilities_EnabledByMarkerFile(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: enabledConfigDir(t)})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if !resp.GetBuilderEnabled() {
		t.Fatal("the opt-in marker file must enable the builder role")
	}
}

func TestGetBuildCapabilities_ReportsNoBuildkitWhenSocketAbsent(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		BuildkitAddress: "unix:///nonexistent/buildkitd.sock",
	})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuildkitAvailable() {
		t.Fatal("buildkit must not be reported available when its socket does not exist")
	}
	if len(resp.GetNativePlatforms()) != 0 {
		t.Fatal("a host with no buildkit must advertise no buildable platforms")
	}
}

func TestGetBuildCapabilities_ReportsPlatformsWhenBuildkitPresent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "buildkitd.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("creating fake socket: %v", err)
	}
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		ConfigPath:      enabledConfigDir(t),
		BuildkitAddress: "unix://" + sock,
	})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if !resp.GetBuildkitAvailable() {
		t.Fatal("buildkit must be reported available when its socket exists")
	}
	if len(resp.GetNativePlatforms()) == 0 {
		t.Fatal("a buildkit host must advertise at least its own platform")
	}
	if resp.GetOs() == "" || resp.GetCpuArchitecture() == "" {
		t.Fatal("os and architecture must be reported so the CLI can check the target platform")
	}
}

func TestBuildImage_RejectedWhenNotEnabled(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{ConfigPath: t.TempDir()})

	err := svc.BuildImage(&stubBuildStream{
		spec: &agentpbv2.BuildSpec{AppId: "app", Platform: "linux/arm64"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition when the device is not a builder", err)
	}
}
