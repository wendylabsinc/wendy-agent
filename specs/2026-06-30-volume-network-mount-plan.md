# Volume Network Mount Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mount a WendyOS app's persistent volume on a macOS/Linux/Windows PC as a read-write network drive, working both locally and over the cloud tunnel.

**Architecture:** A new `WendyVolumeFsService` gRPC service on the agent answers stateless, path-scoped filesystem RPCs against `/var/lib/wendy/volumes/<volume>`. A host-side gateway in the `wendy` CLI wraps those RPCs in a `go-billy` filesystem and serves it as userspace NFSv3 (macOS/Linux) or WebDAV (Windows) on `127.0.0.1`, then auto-mounts it. The device runs no file server and opens no new ports.

**Tech Stack:** Go, gRPC (`google.golang.org/grpc`), Cobra CLI, `go.uber.org/zap`, `github.com/willscott/go-nfs`, `github.com/go-git/go-billy/v5`, `golang.org/x/net/webdav`.

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy`. Internal imports are `github.com/wendylabsinc/wendy/go/internal/...`; generated v2 stubs are `agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"`.
- All shell commands run from the repo root unless stated. Proto regen runs from `go/` (`cd go && make proto`).
- Build check: `go build ./...` (from repo root). Test: `go test ./go/internal/...`.
- Volumes live at `/var/lib/wendy/volumes/<name>` (`go/internal/agent/services/container_service.go:802`, `var volumesDir = "/var/lib/wendy/volumes"`).
- Auth/trust = existing mTLS / cloud-tunnel channel. No new entitlement.
- Tests use standard Go `testing` (no testify), `zap.NewNop()` loggers, and `google.golang.org/grpc/test/bufconn` for in-process gRPC.
- gRPC error mapping: `NotFound`→missing, `PermissionDenied`→perm, `InvalidArgument`→bad/escaping path, `ResourceExhausted`→ENOSPC. Use `status.Errorf(codes.X, ...)`.
- Read-write is the default; `--read-only` is an opt-out. Path scoping rejects any path escaping the volume root.

---

### Task 1: Define the `WendyVolumeFsService` proto + generate stubs

**Files:**
- Create: `Proto/wendy/agent/services/v2/volumefs_service.proto`
- Modify: `go/scripts/generate-proto.sh` (add the proto to the `V2_AGENT_PROTOS` array)
- Generated (do not hand-edit): `go/proto/gen/agentpb/v2/volumefs_service.pb.go`, `volumefs_service_grpc.pb.go`

**Interfaces:**
- Produces (consumed by every later task): service `WendyVolumeFsService` and messages below, in Go package `agentpbv2`. Key Go types: `Attr`, `DirEntry`, `FileType` (enum), `StatRequest/StatResponse`, `ReadDirRequest/ReadDirResponse`, `ReadRequest/ReadResponse`, `WriteRequest/WriteResponse`, `CreateRequest`, `MkdirRequest`, `RmdirRequest/RmdirResponse`, `UnlinkRequest/UnlinkResponse`, `RenameRequest/RenameResponse`, `SetAttrRequest`, `StatFsRequest/StatFsResponse`. Client ctor `agentpbv2.NewWendyVolumeFsServiceClient(conn)`; server registrar `agentpbv2.RegisterWendyVolumeFsServiceServer(srv, svc)`; server base `agentpbv2.UnimplementedWendyVolumeFsServiceServer`.

- [ ] **Step 1: Write the proto file**

Create `Proto/wendy/agent/services/v2/volumefs_service.proto`:

```proto
syntax = "proto3";
package wendy.agent.services.v2;
option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";

// Path-scoped filesystem access to a single persistent volume under
// /var/lib/wendy/volumes/<volume>. All paths are volume-relative; the agent
// rejects any path escaping the volume root.
service WendyVolumeFsService {
    rpc Stat(StatRequest) returns (StatResponse);
    rpc ReadDir(ReadDirRequest) returns (ReadDirResponse);
    rpc Read(ReadRequest) returns (ReadResponse);
    rpc Write(WriteRequest) returns (WriteResponse);
    rpc Create(CreateRequest) returns (StatResponse);
    rpc Mkdir(MkdirRequest) returns (StatResponse);
    rpc Rmdir(RmdirRequest) returns (RmdirResponse);
    rpc Unlink(UnlinkRequest) returns (UnlinkResponse);
    rpc Rename(RenameRequest) returns (RenameResponse);
    rpc SetAttr(SetAttrRequest) returns (StatResponse);
    rpc StatFs(StatFsRequest) returns (StatFsResponse);
}

enum FileType {
    FILE_TYPE_UNSPECIFIED = 0;
    FILE_TYPE_REGULAR = 1;
    FILE_TYPE_DIR = 2;
    FILE_TYPE_SYMLINK = 3;
}

message Attr {
    FileType type = 1;
    int64 size = 2;
    uint32 mode = 3;            // unix permission bits (lower 12 bits significant)
    int64 mtime_unix_nano = 4;
    string symlink_target = 5;  // set only when type == FILE_TYPE_SYMLINK
}

message StatRequest { string volume = 1; string path = 2; }
message StatResponse { Attr attr = 1; }

message DirEntry { string name = 1; Attr attr = 2; }
message ReadDirRequest { string volume = 1; string path = 2; }
message ReadDirResponse { repeated DirEntry entries = 1; }

message ReadRequest { string volume = 1; string path = 2; int64 offset = 3; int32 length = 4; }
message ReadResponse { bytes data = 1; bool eof = 2; }

message WriteRequest { string volume = 1; string path = 2; int64 offset = 3; bytes data = 4; }
message WriteResponse { int32 written = 1; }

message CreateRequest { string volume = 1; string path = 2; uint32 mode = 3; }
message MkdirRequest { string volume = 1; string path = 2; uint32 mode = 3; }

message RmdirRequest { string volume = 1; string path = 2; }
message RmdirResponse {}
message UnlinkRequest { string volume = 1; string path = 2; }
message UnlinkResponse {}
message RenameRequest { string volume = 1; string from = 2; string to = 3; }
message RenameResponse {}

message SetAttrRequest {
    string volume = 1;
    string path = 2;
    optional uint32 mode = 3;
    optional int64 size = 4;             // truncate target
    optional int64 mtime_unix_nano = 5;
}

message StatFsRequest { string volume = 1; }
message StatFsResponse { uint64 total_bytes = 1; uint64 free_bytes = 2; }
```

- [ ] **Step 2: Register the proto in the generator**

In `go/scripts/generate-proto.sh`, add the new path to the `V2_AGENT_PROTOS` array (next to `"wendy/agent/services/v2/container_service.proto"`):

```bash
    "wendy/agent/services/v2/volumefs_service.proto"
```

- [ ] **Step 3: Generate the stubs**

Run: `cd go && make proto`
Expected: completes without error; new files exist:
`ls go/proto/gen/agentpb/v2/volumefs_service*.go` → `volumefs_service.pb.go  volumefs_service_grpc.pb.go`

- [ ] **Step 4: Verify the module still builds**

Run: `go build ./...`
Expected: PASS (no compile errors).

- [ ] **Step 5: Commit**

```bash
git add Proto/wendy/agent/services/v2/volumefs_service.proto go/scripts/generate-proto.sh go/proto/gen/agentpb/v2/
git commit -m "feat(proto): add WendyVolumeFsService for volume filesystem access"
```

---

### Task 2: Path-scoping helper (security boundary)

**Files:**
- Create: `go/internal/agent/services/volumefs_path.go`
- Test: `go/internal/agent/services/volumefs_path_test.go`

**Interfaces:**
- Consumes: `volumesDir` (package-level var in `container_service.go`).
- Produces:
  - `func resolveVolumePath(volume, relPath string) (string, error)` — returns the absolute on-disk path under `volumesDir/<volume>`, or a `codes.InvalidArgument` status error if `volume` is unsafe or the path escapes the root. Existing-symlink components are resolved and must remain within the root.
  - `func volumeRoot(volume string) (string, error)` — validates `volume` and returns `filepath.Join(volumesDir, volume)`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/services/volumefs_path_test.go`:

```go
package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveVolumePath(t *testing.T) {
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	defer func() { volumesDir = old }()

	root := filepath.Join(tmp, "vol")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		volume  string
		path    string
		wantErr bool
		want    string
	}{
		{"root", "vol", "", false, root},
		{"slash root", "vol", "/", false, root},
		{"nested", "vol", "sub/file.txt", false, filepath.Join(root, "sub/file.txt")},
		{"dotdot escape", "vol", "../../etc/passwd", true, ""},
		{"absolute stays scoped", "vol", "/etc/passwd", false, filepath.Join(root, "etc/passwd")},
		{"empty volume", "", "x", true, ""},
		{"slash in volume", "a/b", "x", true, ""},
		{"dotdot in volume", "..", "x", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveVolumePath(c.volume, c.path)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestResolveVolumePathSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	defer func() { volumesDir = old }()

	root := filepath.Join(tmp, "vol")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the volume that points outside it.
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := resolveVolumePath("vol", "link/secret"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	_ = strings.TrimSpace // keep import if unused after edits
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestResolveVolumePath -v`
Expected: FAIL — `undefined: resolveVolumePath`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/agent/services/volumefs_path.go`:

```go
package services

import (
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// volumeRoot validates the volume name and returns its on-disk root directory.
// The name must be a single path segment (no separators, no "..").
func volumeRoot(volume string) (string, error) {
	if volume == "" || strings.ContainsAny(volume, `/\`) || volume == "." || volume == ".." {
		return "", status.Errorf(codes.InvalidArgument, "invalid volume name %q", volume)
	}
	return filepath.Join(volumesDir, volume), nil
}

// resolveVolumePath maps a volume-relative path to an absolute on-disk path
// confined to volumesDir/<volume>. Absolute inputs are treated as relative to
// the volume root. Any existing symlink component is resolved and must also
// remain within the root, preventing symlink-based escapes.
func resolveVolumePath(volume, relPath string) (string, error) {
	root, err := volumeRoot(volume)
	if err != nil {
		return "", err
	}
	// Normalise: clean as an absolute path then strip the leading slash so any
	// leading "/" or embedded ".." collapses against the volume root.
	clean := filepath.Clean("/" + filepath.ToSlash(relPath))
	full := filepath.Join(root, filepath.FromSlash(clean))

	if !withinRoot(root, full) {
		return "", status.Errorf(codes.InvalidArgument, "path %q escapes volume", relPath)
	}

	// Resolve symlinks on the longest existing prefix; the resolved location
	// must remain within the root.
	resolved, err := filepath.EvalSymlinks(full)
	if err == nil && !withinRoot(root, resolved) {
		return "", status.Errorf(codes.InvalidArgument, "path %q resolves outside volume", relPath)
	}
	return full, nil
}

func withinRoot(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/services/ -run TestResolveVolumePath -v`
Expected: PASS (symlink subtest may `SKIP` on platforms without symlink support).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/volumefs_path.go go/internal/agent/services/volumefs_path_test.go
git commit -m "feat(agent): path-scoping helper for volume filesystem access"
```

---

### Task 3: VolumeFsService — read/metadata RPCs (Stat, ReadDir, Read, StatFs) + registration

**Files:**
- Create: `go/internal/agent/services/volumefs_service.go`
- Test: `go/internal/agent/services/volumefs_service_test.go`
- Modify: `go/cmd/wendy-agent/main.go` (construct + register the service)

**Interfaces:**
- Consumes: `resolveVolumePath`, `volumeRoot` (Task 2); `agentpbv2` types (Task 1).
- Produces:
  - `type VolumeFsService struct { ... }` embedding `agentpbv2.UnimplementedWendyVolumeFsServiceServer`.
  - `func NewVolumeFsService(logger *zap.Logger) *VolumeFsService`.
  - Methods: `Stat`, `ReadDir`, `Read`, `StatFs` (write methods land in Task 4 on the same struct).
  - `func fileInfoToAttr(fi os.FileInfo, symlinkTarget string) *agentpbv2.Attr` — shared helper.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/services/volumefs_service_test.go`:

```go
package services

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func startVolumeFsServer(t *testing.T) (agentpbv2.WendyVolumeFsServiceClient, string) {
	t.Helper()
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	t.Cleanup(func() { volumesDir = old })

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyVolumeFsServiceServer(srv, NewVolumeFsService(zap.NewNop()))
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })
	return agentpbv2.NewWendyVolumeFsServiceClient(conn), tmp
}

func TestVolumeFsStatAndReadDir(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(filepath.Join(vdir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := cl.Stat(context.Background(), &agentpbv2.StatRequest{Volume: "vol", Path: "a.txt"})
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.GetAttr().GetType() != agentpbv2.FileType_FILE_TYPE_REGULAR || st.GetAttr().GetSize() != 5 {
		t.Fatalf("unexpected attr: %+v", st.GetAttr())
	}

	rd, err := cl.ReadDir(context.Background(), &agentpbv2.ReadDirRequest{Volume: "vol", Path: ""})
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(rd.GetEntries()) != 2 {
		t.Fatalf("want 2 entries, got %d", len(rd.GetEntries()))
	}
}

func TestVolumeFsRead(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "a.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := cl.Read(context.Background(), &agentpbv2.ReadRequest{Volume: "vol", Path: "a.txt", Offset: 6, Length: 100})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(resp.GetData()) != "world" || !resp.GetEof() {
		t.Fatalf("got %q eof=%v", resp.GetData(), resp.GetEof())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestVolumeFs -v`
Expected: FAIL — `undefined: NewVolumeFsService`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/agent/services/volumefs_service.go`:

```go
package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

const maxReadChunk = 1 << 20 // 1 MiB

type VolumeFsService struct {
	agentpbv2.UnimplementedWendyVolumeFsServiceServer
	logger *zap.Logger
}

func NewVolumeFsService(logger *zap.Logger) *VolumeFsService {
	return &VolumeFsService{logger: logger}
}

// osErrToStatus maps filesystem errors to gRPC status codes.
func osErrToStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, os.ErrPermission):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, os.ErrExist):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, unix.ENOSPC):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func fileInfoToAttr(fi os.FileInfo, symlinkTarget string) *agentpbv2.Attr {
	t := agentpbv2.FileType_FILE_TYPE_REGULAR
	switch {
	case fi.IsDir():
		t = agentpbv2.FileType_FILE_TYPE_DIR
	case fi.Mode()&os.ModeSymlink != 0:
		t = agentpbv2.FileType_FILE_TYPE_SYMLINK
	}
	return &agentpbv2.Attr{
		Type:          t,
		Size:          fi.Size(),
		Mode:          uint32(fi.Mode().Perm()),
		MtimeUnixNano: fi.ModTime().UnixNano(),
		SymlinkTarget: symlinkTarget,
	}
}

func (s *VolumeFsService) Stat(_ context.Context, req *agentpbv2.StatRequest) (*agentpbv2.StatResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	var target string
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ = os.Readlink(full)
	}
	return &agentpbv2.StatResponse{Attr: fileInfoToAttr(fi, target)}, nil
}

func (s *VolumeFsService) ReadDir(_ context.Context, req *agentpbv2.ReadDirRequest) (*agentpbv2.ReadDirResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	out := make([]*agentpbv2.DirEntry, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		var target string
		if fi.Mode()&os.ModeSymlink != 0 {
			target, _ = os.Readlink(filepath.Join(full, e.Name()))
		}
		out = append(out, &agentpbv2.DirEntry{Name: e.Name(), Attr: fileInfoToAttr(fi, target)})
	}
	return &agentpbv2.ReadDirResponse{Entries: out}, nil
}

func (s *VolumeFsService) Read(_ context.Context, req *agentpbv2.ReadRequest) (*agentpbv2.ReadResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	n := int(req.GetLength())
	if n <= 0 || n > maxReadChunk {
		n = maxReadChunk
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := f.ReadAt(buf, req.GetOffset())
	eof := errors.Is(err, io.EOF)
	if err != nil && !eof {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.ReadResponse{Data: buf[:read], Eof: eof}, nil
}

func (s *VolumeFsService) StatFs(_ context.Context, req *agentpbv2.StatFsRequest) (*agentpbv2.StatFsResponse, error) {
	root, err := volumeRoot(req.GetVolume())
	if err != nil {
		return nil, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.StatFsResponse{
		TotalBytes: st.Blocks * uint64(st.Bsize),
		FreeBytes:  st.Bavail * uint64(st.Bsize),
	}, nil
}
```

- [ ] **Step 4: Register the service in the agent**

In `go/cmd/wendy-agent/main.go`, add the construction near the other `services.New...` calls (around line 186-210):

```go
	volumeFsSvc := services.NewVolumeFsService(logger)
```

And inside the `registerAllServices := func(srv *grpc.Server) {` closure (around line 394-414), add:

```go
		agentpbv2.RegisterWendyVolumeFsServiceServer(srv, volumeFsSvc)
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./go/internal/agent/services/ -run TestVolumeFs -v && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/volumefs_service.go go/internal/agent/services/volumefs_service_test.go go/cmd/wendy-agent/main.go
git commit -m "feat(agent): VolumeFsService read/metadata RPCs + registration"
```

---

### Task 4: VolumeFsService — write/mutation RPCs (Write, Create, Mkdir, Rmdir, Unlink, Rename, SetAttr)

**Files:**
- Modify: `go/internal/agent/services/volumefs_service.go`
- Modify: `go/internal/agent/services/volumefs_service_test.go`

**Interfaces:**
- Consumes: everything from Task 3 (`resolveVolumePath`, `osErrToStatus`, `fileInfoToAttr`, `VolumeFsService`).
- Produces: methods `Write`, `Create`, `Mkdir`, `Rmdir`, `Unlink`, `Rename`, `SetAttr` on `*VolumeFsService`.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/agent/services/volumefs_service_test.go`:

```go
func TestVolumeFsWriteCreateRename(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := cl.Create(ctx, &agentpbv2.CreateRequest{Volume: "vol", Path: "new.txt", Mode: 0o644}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := cl.Write(ctx, &agentpbv2.WriteRequest{Volume: "vol", Path: "new.txt", Offset: 0, Data: []byte("abcdef")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := cl.SetAttr(ctx, &agentpbv2.SetAttrRequest{Volume: "vol", Path: "new.txt", Size: proto.Int64(3)}); err != nil {
		t.Fatalf("SetAttr truncate: %v", err)
	}
	if _, err := cl.Rename(ctx, &agentpbv2.RenameRequest{Volume: "vol", From: "new.txt", To: "renamed.txt"}); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vdir, "renamed.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "abc" {
		t.Fatalf("want %q got %q", "abc", got)
	}

	if _, err := cl.Mkdir(ctx, &agentpbv2.MkdirRequest{Volume: "vol", Path: "d", Mode: 0o755}); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := cl.Rmdir(ctx, &agentpbv2.RmdirRequest{Volume: "vol", Path: "d"}); err != nil {
		t.Fatalf("Rmdir: %v", err)
	}
	if _, err := cl.Unlink(ctx, &agentpbv2.UnlinkRequest{Volume: "vol", Path: "renamed.txt"}); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
}
```

Add `"google.golang.org/protobuf/proto"` to the test file's imports (for `proto.Int64`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestVolumeFsWriteCreateRename -v`
Expected: FAIL — `cl.Create undefined (...)` / method not implemented (returns `Unimplemented`).

- [ ] **Step 3: Write the implementation**

Append to `go/internal/agent/services/volumefs_service.go`:

```go
func (s *VolumeFsService) Write(_ context.Context, req *agentpbv2.WriteRequest) (*agentpbv2.WriteResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_WRONLY, 0)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	defer f.Close()
	n, err := f.WriteAt(req.GetData(), req.GetOffset())
	if err != nil {
		return nil, osErrToStatus(err)
	}
	if err := f.Sync(); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.WriteResponse{Written: int32(n)}, nil
}

func (s *VolumeFsService) Create(_ context.Context, req *agentpbv2.CreateRequest) (*agentpbv2.StatResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY, os.FileMode(req.GetMode()))
	if err != nil {
		return nil, osErrToStatus(err)
	}
	_ = f.Close()
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.StatResponse{Attr: fileInfoToAttr(fi, "")}, nil
}

func (s *VolumeFsService) Mkdir(_ context.Context, req *agentpbv2.MkdirRequest) (*agentpbv2.StatResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(full, os.FileMode(req.GetMode())); err != nil {
		return nil, osErrToStatus(err)
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.StatResponse{Attr: fileInfoToAttr(fi, "")}, nil
}

func (s *VolumeFsService) Rmdir(_ context.Context, req *agentpbv2.RmdirRequest) (*agentpbv2.RmdirResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := os.Remove(full); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.RmdirResponse{}, nil
}

func (s *VolumeFsService) Unlink(_ context.Context, req *agentpbv2.UnlinkRequest) (*agentpbv2.UnlinkResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := os.Remove(full); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.UnlinkResponse{}, nil
}

func (s *VolumeFsService) Rename(_ context.Context, req *agentpbv2.RenameRequest) (*agentpbv2.RenameResponse, error) {
	from, err := resolveVolumePath(req.GetVolume(), req.GetFrom())
	if err != nil {
		return nil, err
	}
	to, err := resolveVolumePath(req.GetVolume(), req.GetTo())
	if err != nil {
		return nil, err
	}
	if err := os.Rename(from, to); err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.RenameResponse{}, nil
}

func (s *VolumeFsService) SetAttr(_ context.Context, req *agentpbv2.SetAttrRequest) (*agentpbv2.StatResponse, error) {
	full, err := resolveVolumePath(req.GetVolume(), req.GetPath())
	if err != nil {
		return nil, err
	}
	if req.Size != nil {
		if err := os.Truncate(full, req.GetSize()); err != nil {
			return nil, osErrToStatus(err)
		}
	}
	if req.Mode != nil {
		if err := os.Chmod(full, os.FileMode(req.GetMode())); err != nil {
			return nil, osErrToStatus(err)
		}
	}
	if req.MtimeUnixNano != nil {
		mt := time.Unix(0, req.GetMtimeUnixNano())
		if err := os.Chtimes(full, mt, mt); err != nil {
			return nil, osErrToStatus(err)
		}
	}
	fi, err := os.Lstat(full)
	if err != nil {
		return nil, osErrToStatus(err)
	}
	return &agentpbv2.StatResponse{Attr: fileInfoToAttr(fi, "")}, nil
}
```

Add `"time"` to the file's import block.

- [ ] **Step 4: Run tests + build**

Run: `go test ./go/internal/agent/services/ -run TestVolumeFs -v && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/volumefs_service.go go/internal/agent/services/volumefs_service_test.go
git commit -m "feat(agent): VolumeFsService write/mutation RPCs"
```

---

### Task 5: Host gRPC FS client + AgentConnection wiring

**Files:**
- Create: `go/internal/cli/mount/fsclient.go`
- Test: `go/internal/cli/mount/fsclient_test.go`
- Modify: `go/internal/cli/grpcclient/client.go` (add `VolumeFsService` field + init)

**Interfaces:**
- Consumes: `agentpbv2.WendyVolumeFsServiceClient` (Task 1).
- Produces:
  - `type FSClient struct { ... }` with `func NewFSClient(ctx context.Context, cl agentpbv2.WendyVolumeFsServiceClient, volume string) *FSClient`.
  - Methods: `Stat(path) (*agentpbv2.Attr, error)`, `ReadDir(path) ([]*agentpbv2.DirEntry, error)`, `ReadAt(path string, off int64, n int) (data []byte, eof bool, err error)`, `WriteAt(path string, off int64, data []byte) (int, error)`, `Create(path string, mode uint32) (*agentpbv2.Attr, error)`, `Mkdir(path string, mode uint32) error`, `Unlink(path) error`, `Rmdir(path) error`, `Rename(from, to string) error`, `Truncate(path string, size int64) error`, `Chmod(path string, mode uint32) error`, `StatFs() (total, free uint64, err error)`.
  - `AgentConnection.VolumeFsService agentpbv2.WendyVolumeFsServiceClient` populated in `newAgentConnection`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/mount/fsclient_test.go`. It stands up the real `VolumeFsService` over bufconn and drives it through `FSClient`:

```go
package mount

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func newTestFSClient(t *testing.T) (*FSClient, string) {
	t.Helper()
	tmp := t.TempDir()
	services.SetVolumesDirForTest(tmp) // see Step 3 note
	t.Cleanup(services.ResetVolumesDirForTest)

	if err := os.MkdirAll(filepath.Join(tmp, "vol"), 0o755); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyVolumeFsServiceServer(srv, services.NewVolumeFsService(zap.NewNop()))
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })
	return NewFSClient(context.Background(), agentpbv2.NewWendyVolumeFsServiceClient(conn), "vol"), tmp
}

func TestFSClientRoundTrip(t *testing.T) {
	c, root := newTestFSClient(t)
	if _, err := c.Create("f.txt", 0o644); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := c.WriteAt("f.txt", 0, []byte("payload")); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	data, eof, err := c.ReadAt("f.txt", 0, 100)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(data) != "payload" || !eof {
		t.Fatalf("got %q eof=%v", data, eof)
	}
	if _, err := os.Stat(filepath.Join(root, "vol", "f.txt")); err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestFSClient -v`
Expected: FAIL — `undefined: NewFSClient` and `services.SetVolumesDirForTest`.

- [ ] **Step 3: Add the test seam for volumesDir**

`volumesDir` is an unexported package var. Add a tiny test seam in `go/internal/agent/services/volumefs_service.go`:

```go
// SetVolumesDirForTest overrides the volumes root. Test-only helper for
// cross-package tests; not for production use.
func SetVolumesDirForTest(dir string) { volumesDir = dir }

// ResetVolumesDirForTest restores the default volumes root.
func ResetVolumesDirForTest() { volumesDir = "/var/lib/wendy/volumes" }
```

- [ ] **Step 4: Write the FSClient implementation**

Create `go/internal/cli/mount/fsclient.go`:

```go
package mount

import (
	"context"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// FSClient is a thin, path-oriented wrapper over WendyVolumeFsService scoped to
// a single volume. It is the shared backend for the NFS (billy) and WebDAV
// frontends.
type FSClient struct {
	ctx    context.Context
	cl     agentpbv2.WendyVolumeFsServiceClient
	volume string
}

func NewFSClient(ctx context.Context, cl agentpbv2.WendyVolumeFsServiceClient, volume string) *FSClient {
	return &FSClient{ctx: ctx, cl: cl, volume: volume}
}

func (c *FSClient) Stat(path string) (*agentpbv2.Attr, error) {
	r, err := c.cl.Stat(c.ctx, &agentpbv2.StatRequest{Volume: c.volume, Path: path})
	if err != nil {
		return nil, err
	}
	return r.GetAttr(), nil
}

func (c *FSClient) ReadDir(path string) ([]*agentpbv2.DirEntry, error) {
	r, err := c.cl.ReadDir(c.ctx, &agentpbv2.ReadDirRequest{Volume: c.volume, Path: path})
	if err != nil {
		return nil, err
	}
	return r.GetEntries(), nil
}

func (c *FSClient) ReadAt(path string, off int64, n int) ([]byte, bool, error) {
	r, err := c.cl.Read(c.ctx, &agentpbv2.ReadRequest{Volume: c.volume, Path: path, Offset: off, Length: int32(n)})
	if err != nil {
		return nil, false, err
	}
	return r.GetData(), r.GetEof(), nil
}

func (c *FSClient) WriteAt(path string, off int64, data []byte) (int, error) {
	r, err := c.cl.Write(c.ctx, &agentpbv2.WriteRequest{Volume: c.volume, Path: path, Offset: off, Data: data})
	if err != nil {
		return 0, err
	}
	return int(r.GetWritten()), nil
}

func (c *FSClient) Create(path string, mode uint32) (*agentpbv2.Attr, error) {
	r, err := c.cl.Create(c.ctx, &agentpbv2.CreateRequest{Volume: c.volume, Path: path, Mode: mode})
	if err != nil {
		return nil, err
	}
	return r.GetAttr(), nil
}

func (c *FSClient) Mkdir(path string, mode uint32) error {
	_, err := c.cl.Mkdir(c.ctx, &agentpbv2.MkdirRequest{Volume: c.volume, Path: path, Mode: mode})
	return err
}

func (c *FSClient) Unlink(path string) error {
	_, err := c.cl.Unlink(c.ctx, &agentpbv2.UnlinkRequest{Volume: c.volume, Path: path})
	return err
}

func (c *FSClient) Rmdir(path string) error {
	_, err := c.cl.Rmdir(c.ctx, &agentpbv2.RmdirRequest{Volume: c.volume, Path: path})
	return err
}

func (c *FSClient) Rename(from, to string) error {
	_, err := c.cl.Rename(c.ctx, &agentpbv2.RenameRequest{Volume: c.volume, From: from, To: to})
	return err
}

func (c *FSClient) Truncate(path string, size int64) error {
	_, err := c.cl.SetAttr(c.ctx, &agentpbv2.SetAttrRequest{Volume: c.volume, Path: path, Size: &size})
	return err
}

func (c *FSClient) Chmod(path string, mode uint32) error {
	_, err := c.cl.SetAttr(c.ctx, &agentpbv2.SetAttrRequest{Volume: c.volume, Path: path, Mode: &mode})
	return err
}

func (c *FSClient) StatFs() (total, free uint64, err error) {
	r, err := c.cl.StatFs(c.ctx, &agentpbv2.StatFsRequest{Volume: c.volume})
	if err != nil {
		return 0, 0, err
	}
	return r.GetTotalBytes(), r.GetFreeBytes(), nil
}
```

- [ ] **Step 5: Wire the client into AgentConnection**

In `go/internal/cli/grpcclient/client.go`, add the field to the `AgentConnection` struct (next to `TimeSyncService`):

```go
	VolumeFsService     agentpbv2.WendyVolumeFsServiceClient
```

And in `newAgentConnection` (line ~235-246), add to the returned struct literal:

```go
		VolumeFsService:     agentpbv2.NewWendyVolumeFsServiceClient(conn),
```

(`agentpbv2` is already imported in this file.)

- [ ] **Step 6: Run tests + build**

Run: `go test ./go/internal/cli/mount/ -run TestFSClient -v && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mount/fsclient.go go/internal/cli/mount/fsclient_test.go go/internal/cli/grpcclient/client.go go/internal/agent/services/volumefs_service.go
git commit -m "feat(cli): VolumeFs gRPC client + AgentConnection wiring"
```

---

### Task 6: billy.Filesystem adapter (NFS backend)

**Files:**
- Create: `go/internal/cli/mount/billyfs.go`
- Test: `go/internal/cli/mount/billyfs_test.go`
- Modify: `go.mod` / `go.sum` (add `github.com/go-git/go-billy/v5`)

**Interfaces:**
- Consumes: `FSClient` (Task 5).
- Produces: `func NewBillyFS(c *FSClient) billy.Filesystem` implementing `github.com/go-git/go-billy/v5.Filesystem`, backed by `FSClient`. Internal `billyFile` implements `billy.File`.

**Note on the interface:** `billy.Filesystem` = `Basic + TempFile + Dir + Symlink + Chroot`. The compiler enforces the full method set; if a method signature differs in the pinned version, reconcile against `go doc github.com/go-git/go-billy/v5.Filesystem`. Symlink *creation* is intentionally unsupported (returns `billy.ErrNotSupported`); reads pass through.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/go-git/go-billy/v5@v5.6.0`
Expected: `go.mod` gains the require line.

- [ ] **Step 2: Write the failing test**

Create `go/internal/cli/mount/billyfs_test.go`:

```go
package mount

import (
	"io"
	"testing"
)

func TestBillyFSWriteReadRemove(t *testing.T) {
	c, _ := newTestFSClient(t)
	fs := NewBillyFS(c)

	f, err := fs.Create("hello.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("billy world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rf, err := fs.Open("hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "billy world" {
		t.Fatalf("got %q", got)
	}
	_ = rf.Close()

	if err := fs.MkdirAll("a/b", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	infos, err := fs.ReadDir("")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("expected entries")
	}
	if err := fs.Remove("hello.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestBillyFS -v`
Expected: FAIL — `undefined: NewBillyFS`.

- [ ] **Step 4: Write the implementation**

Create `go/internal/cli/mount/billyfs.go`:

```go
package mount

import (
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type billyFS struct {
	c    *FSClient
	root string // chroot prefix, "" at top
}

// NewBillyFS adapts an FSClient to a billy.Filesystem for the NFS frontend.
func NewBillyFS(c *FSClient) billy.Filesystem { return &billyFS{c: c} }

func (b *billyFS) abs(name string) string {
	return strings.TrimPrefix(path.Join("/", b.root, path.Clean("/"+name)), "/")
}

func (b *billyFS) Create(filename string) (billy.File, error) {
	if _, err := b.c.Create(b.abs(filename), 0o644); err != nil {
		return nil, err
	}
	return &billyFile{c: b.c, name: filename, abs: b.abs(filename)}, nil
}

func (b *billyFS) Open(filename string) (billy.File, error) {
	return b.OpenFile(filename, os.O_RDONLY, 0)
}

func (b *billyFS) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	abs := b.abs(filename)
	if flag&os.O_CREATE != 0 {
		if _, err := b.c.Create(abs, uint32(perm.Perm())); err != nil {
			return nil, err
		}
	} else if _, err := b.c.Stat(abs); err != nil {
		return nil, err
	}
	if flag&os.O_TRUNC != 0 {
		if err := b.c.Truncate(abs, 0); err != nil {
			return nil, err
		}
	}
	f := &billyFile{c: b.c, name: filename, abs: abs}
	if flag&os.O_APPEND != 0 {
		if at, err := b.c.Stat(abs); err == nil {
			f.off = at.GetSize()
		}
	}
	return f, nil
}

func (b *billyFS) Stat(filename string) (os.FileInfo, error) {
	at, err := b.c.Stat(b.abs(filename))
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(filename), at), nil
}

func (b *billyFS) Lstat(filename string) (os.FileInfo, error) { return b.Stat(filename) }

func (b *billyFS) Rename(oldpath, newpath string) error {
	return b.c.Rename(b.abs(oldpath), b.abs(newpath))
}

func (b *billyFS) Remove(filename string) error {
	at, err := b.c.Stat(b.abs(filename))
	if err != nil {
		return err
	}
	if at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR {
		return b.c.Rmdir(b.abs(filename))
	}
	return b.c.Unlink(b.abs(filename))
}

func (b *billyFS) Join(elem ...string) string { return path.Join(elem...) }

func (b *billyFS) ReadDir(p string) ([]os.FileInfo, error) {
	entries, err := b.c.ReadDir(b.abs(p))
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, attrToFileInfo(e.GetName(), e.GetAttr()))
	}
	return out, nil
}

func (b *billyFS) MkdirAll(filename string, perm os.FileMode) error {
	parts := strings.Split(strings.Trim(path.Clean("/"+filename), "/"), "/")
	cur := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		if _, err := b.c.Stat(b.abs(cur)); err == nil {
			continue
		}
		if err := b.c.Mkdir(b.abs(cur), uint32(perm.Perm())); err != nil {
			return err
		}
	}
	return nil
}

func (b *billyFS) TempFile(dir, prefix string) (billy.File, error) {
	name := path.Join(dir, prefix+time.Now().Format("20060102T150405.000000000"))
	return b.Create(name)
}

func (b *billyFS) Symlink(target, link string) error { return billy.ErrNotSupported }

func (b *billyFS) Readlink(link string) (string, error) {
	at, err := b.c.Stat(b.abs(link))
	if err != nil {
		return "", err
	}
	if at.GetType() != agentpbv2.FileType_FILE_TYPE_SYMLINK {
		return "", billy.ErrNotSupported
	}
	return at.GetSymlinkTarget(), nil
}

func (b *billyFS) Chroot(p string) (billy.Filesystem, error) {
	return &billyFS{c: b.c, root: path.Join(b.root, p)}, nil
}

func (b *billyFS) Root() string { return path.Join("/", b.root) }
```

- [ ] **Step 5: Add the billy.File and FileInfo helpers**

Append to `go/internal/cli/mount/billyfs.go`:

```go
type billyFile struct {
	c    *FSClient
	name string
	abs  string
	off  int64
}

func (f *billyFile) Name() string { return f.name }

func (f *billyFile) Read(p []byte) (int, error) {
	data, eof, err := f.c.ReadAt(f.abs, f.off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	f.off += int64(n)
	if n == 0 && eof {
		return 0, io.EOF
	}
	return n, nil
}

func (f *billyFile) ReadAt(p []byte, off int64) (int, error) {
	data, eof, err := f.c.ReadAt(f.abs, off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(p) && eof {
		return n, io.EOF
	}
	return n, nil
}

func (f *billyFile) Write(p []byte) (int, error) {
	n, err := f.c.WriteAt(f.abs, f.off, p)
	f.off += int64(n)
	return n, err
}

func (f *billyFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		at, err := f.c.Stat(f.abs)
		if err != nil {
			return 0, err
		}
		f.off = at.GetSize() + offset
	}
	return f.off, nil
}

func (f *billyFile) Close() error                 { return nil }
func (f *billyFile) Lock() error                  { return nil }
func (f *billyFile) Unlock() error                { return nil }
func (f *billyFile) Truncate(size int64) error    { return f.c.Truncate(f.abs, size) }

// fileInfo adapts an Attr to os.FileInfo.
type fileInfo struct {
	name string
	at   *agentpbv2.Attr
}

func attrToFileInfo(name string, at *agentpbv2.Attr) os.FileInfo { return &fileInfo{name: name, at: at} }

func (i *fileInfo) Name() string { return i.name }
func (i *fileInfo) Size() int64  { return i.at.GetSize() }
func (i *fileInfo) Mode() os.FileMode {
	m := os.FileMode(i.at.GetMode()).Perm()
	switch i.at.GetType() {
	case agentpbv2.FileType_FILE_TYPE_DIR:
		m |= os.ModeDir
	case agentpbv2.FileType_FILE_TYPE_SYMLINK:
		m |= os.ModeSymlink
	}
	return m
}
func (i *fileInfo) ModTime() time.Time { return time.Unix(0, i.at.GetMtimeUnixNano()) }
func (i *fileInfo) IsDir() bool        { return i.at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR }
func (i *fileInfo) Sys() any           { return nil }
```

- [ ] **Step 6: Run tests + build**

Run: `go test ./go/internal/cli/mount/ -run TestBillyFS -v && go build ./...`
Expected: PASS. If the build reports an unimplemented billy method, add it per `go doc github.com/go-git/go-billy/v5` and re-run.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mount/billyfs.go go/internal/cli/mount/billyfs_test.go go.mod go.sum
git commit -m "feat(cli): billy.Filesystem adapter over VolumeFs client"
```

---

### Task 7: webdav.FileSystem adapter (Windows backend)

**Files:**
- Create: `go/internal/cli/mount/webdavfs.go`
- Test: `go/internal/cli/mount/webdavfs_test.go`
- Modify: `go.mod` / `go.sum` (add `golang.org/x/net`)

**Interfaces:**
- Consumes: `FSClient` (Task 5), `fileInfo`/`attrToFileInfo` (Task 6).
- Produces: `func NewWebdavFS(c *FSClient) webdav.FileSystem` implementing `golang.org/x/net/webdav.FileSystem`. Internal `webdavFile` implements `webdav.File`.

**Note:** `webdav.FileSystem` methods take a `context.Context` and use OS-style errors. `webdav.File` = `io.Closer + io.Reader + io.Seeker + io.Writer + Readdir(count int) ([]os.FileInfo, error) + Stat() (os.FileInfo, error)`.

- [ ] **Step 1: Add the dependency**

Run: `go get golang.org/x/net@v0.46.0`
Expected: `go.mod` gains/updates `golang.org/x/net`.

- [ ] **Step 2: Write the failing test**

Create `go/internal/cli/mount/webdavfs_test.go`:

```go
package mount

import (
	"context"
	"io"
	"os"
	"testing"
)

func TestWebdavFSWriteRead(t *testing.T) {
	c, _ := newTestFSClient(t)
	fs := NewWebdavFS(c)
	ctx := context.Background()

	f, err := fs.OpenFile(ctx, "dav.txt", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.Write([]byte("dav payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	rf, err := fs.OpenFile(ctx, "dav.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	got, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(got) != "dav payload" {
		t.Fatalf("got %q", got)
	}

	if err := fs.Mkdir(ctx, "sub", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := fs.Stat(ctx, "sub"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestWebdavFS -v`
Expected: FAIL — `undefined: NewWebdavFS`.

- [ ] **Step 4: Write the implementation**

Create `go/internal/cli/mount/webdavfs.go`:

```go
package mount

import (
	"context"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type webdavFS struct{ c *FSClient }

// NewWebdavFS adapts an FSClient to a webdav.FileSystem for the Windows frontend.
func NewWebdavFS(c *FSClient) webdav.FileSystem { return &webdavFS{c: c} }

func rel(name string) string { return strings.TrimPrefix(path.Clean("/"+name), "/") }

func (w *webdavFS) Mkdir(_ context.Context, name string, perm os.FileMode) error {
	return w.c.Mkdir(rel(name), uint32(perm.Perm()))
}

func (w *webdavFS) OpenFile(_ context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	abs := rel(name)
	if flag&os.O_CREATE != 0 {
		if _, err := w.c.Create(abs, uint32(perm.Perm())); err != nil {
			return nil, err
		}
	}
	at, err := w.c.Stat(abs)
	if err != nil && flag&os.O_CREATE == 0 {
		return nil, err
	}
	if flag&os.O_TRUNC != 0 {
		if err := w.c.Truncate(abs, 0); err != nil {
			return nil, err
		}
	}
	f := &webdavFile{c: w.c, name: abs}
	if flag&os.O_APPEND != 0 && at != nil {
		f.off = at.GetSize()
	}
	return f, nil
}

func (w *webdavFS) RemoveAll(_ context.Context, name string) error {
	abs := rel(name)
	at, err := w.c.Stat(abs)
	if err != nil {
		return err
	}
	if at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR {
		return w.c.Rmdir(abs)
	}
	return w.c.Unlink(abs)
}

func (w *webdavFS) Rename(_ context.Context, oldName, newName string) error {
	return w.c.Rename(rel(oldName), rel(newName))
}

func (w *webdavFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	at, err := w.c.Stat(rel(name))
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(rel(name)), at), nil
}

type webdavFile struct {
	c    *FSClient
	name string
	off  int64
}

func (f *webdavFile) Read(p []byte) (int, error) {
	data, eof, err := f.c.ReadAt(f.name, f.off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	f.off += int64(n)
	if n == 0 && eof {
		return 0, io.EOF
	}
	return n, nil
}

func (f *webdavFile) Write(p []byte) (int, error) {
	n, err := f.c.WriteAt(f.name, f.off, p)
	f.off += int64(n)
	return n, err
}

func (f *webdavFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		at, err := f.c.Stat(f.name)
		if err != nil {
			return 0, err
		}
		f.off = at.GetSize() + offset
	}
	return f.off, nil
}

func (f *webdavFile) Readdir(count int) ([]os.FileInfo, error) {
	entries, err := f.c.ReadDir(f.name)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, attrToFileInfo(e.GetName(), e.GetAttr()))
	}
	if count > 0 && len(out) > count {
		out = out[:count]
	}
	return out, nil
}

func (f *webdavFile) Stat() (os.FileInfo, error) {
	at, err := f.c.Stat(f.name)
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(f.name), at), nil
}

func (f *webdavFile) Close() error { return nil }
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./go/internal/cli/mount/ -run TestWebdavFS -v && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/mount/webdavfs.go go/internal/cli/mount/webdavfs_test.go go.mod go.sum
git commit -m "feat(cli): webdav.FileSystem adapter over VolumeFs client"
```

---

### Task 8: NFS server + mount orchestration (macOS/Linux)

**Files:**
- Create: `go/internal/cli/mount/server_nfs.go`
- Create: `go/internal/cli/mount/mount_unix.go` (build tag `//go:build darwin || linux`)
- Test: `go/internal/cli/mount/server_nfs_test.go`
- Modify: `go.mod` / `go.sum` (add `github.com/willscott/go-nfs`)

**Interfaces:**
- Consumes: `NewBillyFS` (Task 6).
- Produces:
  - `func ServeNFS(ctx context.Context, fs billy.Filesystem) (addr string, stop func() error, err error)` — starts a userspace NFSv3 server on `127.0.0.1:0`, returns the chosen `host:port`.
  - `func mountNFS(ctx context.Context, addr, mountpoint string, readOnly bool) error` and `func unmountPath(mountpoint string) error` (in `mount_unix.go`).

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/willscott/go-nfs@latest`
Expected: `go.mod` gains `github.com/willscott/go-nfs` (and its `go-nfs-client`/`buse` deps).

- [ ] **Step 2: Write the failing test**

Create `go/internal/cli/mount/server_nfs_test.go` (verifies the server starts and hands back a loopback address; does not perform a real OS mount):

```go
package mount

import (
	"context"
	"strings"
	"testing"
)

func TestServeNFSStartsOnLoopback(t *testing.T) {
	c, _ := newTestFSClient(t)
	addr, stop, err := ServeNFS(context.Background(), NewBillyFS(c))
	if err != nil {
		t.Fatalf("ServeNFS: %v", err)
	}
	defer stop()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected loopback addr, got %q", addr)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestServeNFS -v`
Expected: FAIL — `undefined: ServeNFS`.

- [ ] **Step 4: Write the NFS server**

Create `go/internal/cli/mount/server_nfs.go`:

```go
package mount

import (
	"context"
	"net"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

// ServeNFS starts a userspace NFSv3 server bound to loopback, backed by fs.
// It returns the listening address and a stop function.
func ServeNFS(ctx context.Context, fs billy.Filesystem) (string, func() error, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	handler := nfshelper.NewNullAuthHandler(fs)
	cacheHandler := nfshelper.NewCachingHandler(handler, 1024)

	go func() { _ = nfs.Serve(lis, cacheHandler) }()

	stop := func() error { return lis.Close() }
	go func() {
		<-ctx.Done()
		_ = lis.Close()
	}()
	return lis.Addr().String(), stop, nil
}
```

- [ ] **Step 5: Write the unix mount helpers**

Create `go/internal/cli/mount/mount_unix.go`:

```go
//go:build darwin || linux

package mount

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
)

// mountNFS mounts the loopback NFS server at addr onto mountpoint.
func mountNFS(ctx context.Context, addr, mountpoint string, readOnly bool) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// nfsv3 over tcp, no reserved-port requirement, explicit server port.
		opts := fmt.Sprintf("vers=3,tcp,port=%d,mountport=%d,nolocks,noresvport", port, port)
		if readOnly {
			opts += ",rdonly"
		}
		cmd = exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", opts,
			fmt.Sprintf("%s:/", host), mountpoint)
	case "linux":
		opts := fmt.Sprintf("vers=3,tcp,port=%d,mountport=%d,nolock", port, port)
		if readOnly {
			opts += ",ro"
		}
		cmd = exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", opts,
			fmt.Sprintf("%s:/", host), mountpoint)
	default:
		return fmt.Errorf("nfs mount not supported on %s", runtime.GOOS)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount failed: %v: %s", err, out)
	}
	return nil
}

func unmountPath(mountpoint string) error {
	cmd := exec.Command("umount", mountpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount failed: %v: %s", err, out)
	}
	return nil
}
```

- [ ] **Step 6: Run tests + build**

Run: `go test ./go/internal/cli/mount/ -run TestServeNFS -v && go build ./...`
Expected: PASS. If `go-nfs` helper names differ in the resolved version, reconcile against `go doc github.com/willscott/go-nfs/helpers` (the helper set is `NewNullAuthHandler` + `NewCachingHandler`).

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mount/server_nfs.go go/internal/cli/mount/mount_unix.go go/internal/cli/mount/server_nfs_test.go go.mod go.sum
git commit -m "feat(cli): userspace NFS server + unix mount helpers"
```

---

### Task 9: WebDAV server + drive mapping (Windows)

**Files:**
- Create: `go/internal/cli/mount/server_webdav.go`
- Create: `go/internal/cli/mount/mount_windows.go` (build tag `//go:build windows`)
- Create: `go/internal/cli/mount/mount_other.go` (build tag `//go:build !windows`) — stub `mapWebdavDrive`/`unmapWebdavDrive` returning a clear "unsupported" error, so non-Windows builds link.
- Test: `go/internal/cli/mount/server_webdav_test.go`

**Interfaces:**
- Consumes: `NewWebdavFS` (Task 7).
- Produces:
  - `func ServeWebdav(ctx context.Context, fs webdav.FileSystem) (addr string, stop func() error, err error)` — HTTP WebDAV handler on `127.0.0.1:0`.
  - `func mapWebdavDrive(ctx context.Context, addr, drive string) error` / `func unmapWebdavDrive(drive string) error` (Windows: `net use`).

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/mount/server_webdav_test.go`:

```go
package mount

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestServeWebdavStartsOnLoopback(t *testing.T) {
	c, _ := newTestFSClient(t)
	addr, stop, err := ServeWebdav(context.Background(), NewWebdavFS(c))
	if err != nil {
		t.Fatalf("ServeWebdav: %v", err)
	}
	defer stop()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected loopback addr, got %q", addr)
	}
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestServeWebdav -v`
Expected: FAIL — `undefined: ServeWebdav`.

- [ ] **Step 3: Write the WebDAV server**

Create `go/internal/cli/mount/server_webdav.go`:

```go
package mount

import (
	"context"
	"net"
	"net/http"

	"golang.org/x/net/webdav"
)

// ServeWebdav starts a WebDAV HTTP server bound to loopback, backed by fs.
func ServeWebdav(ctx context.Context, fs webdav.FileSystem) (string, func() error, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	handler := &webdav.Handler{
		FileSystem: fs,
		LockSystem: webdav.NewMemLS(),
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(lis) }()

	stop := func() error { return srv.Close() }
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return lis.Addr().String(), stop, nil
}
```

- [ ] **Step 4: Write the Windows + stub mount helpers**

Create `go/internal/cli/mount/mount_windows.go`:

```go
//go:build windows

package mount

import (
	"context"
	"fmt"
	"os/exec"
)

// mapWebdavDrive maps the loopback WebDAV server to a drive letter via the
// Windows WebClient redirector.
func mapWebdavDrive(ctx context.Context, addr, drive string) error {
	url := fmt.Sprintf(`\\%s@%s\DavWWWRoot`, hostOf(addr), portOf(addr)) // see note below
	cmd := exec.CommandContext(ctx, "net", "use", drive, "http://"+addr+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("net use failed: %v: %s", err, out)
	}
	_ = url
	return nil
}

func unmapWebdavDrive(drive string) error {
	cmd := exec.Command("net", "use", drive, "/delete", "/y")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("net use /delete failed: %v: %s", err, out)
	}
	return nil
}
```

(`hostOf`/`portOf` are not needed if you map via the HTTP URL form; drop the unused `url` line — kept here only to flag that some Windows versions require the `\\host@port\DavWWWRoot` UNC form instead of the `http://` form. Prefer `net use <drive> http://127.0.0.1:<port>/` first.)

Create `go/internal/cli/mount/mount_other.go`:

```go
//go:build !windows

package mount

import (
	"context"
	"fmt"
)

func mapWebdavDrive(_ context.Context, _, _ string) error {
	return fmt.Errorf("webdav drive mapping is only supported on Windows")
}

func unmapWebdavDrive(_ string) error {
	return fmt.Errorf("webdav drive mapping is only supported on Windows")
}
```

Also add the non-Windows stubs for the NFS helpers so the Windows build links: create `go/internal/cli/mount/mount_nonunix.go` with build tag `//go:build !darwin && !linux` providing `mountNFS`/`unmountPath` that return `fmt.Errorf("nfs mount is only supported on macOS and Linux")`.

- [ ] **Step 5: Run tests + build (host + cross-compile both targets)**

Run: `go test ./go/internal/cli/mount/ -run TestServeWebdav -v`
Run: `GOOS=windows GOARCH=amd64 go build ./... && GOOS=linux GOARCH=arm64 go build ./...`
Expected: tests PASS; both cross-builds succeed (proves the build-tag split links on every OS).

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/mount/server_webdav.go go/internal/cli/mount/mount_windows.go go/internal/cli/mount/mount_other.go go/internal/cli/mount/mount_nonunix.go go/internal/cli/mount/server_webdav_test.go go.mod go.sum
git commit -m "feat(cli): WebDAV server + Windows drive mapping + cross-OS stubs"
```

---

### Task 10: `wendy device mount` / `unmount` command

**Files:**
- Create: `go/internal/cli/commands/device_mount.go`
- Modify: `go/internal/cli/commands/device.go` (register under `manage` group)
- Create: `go/internal/cli/mount/orchestrate.go` (OS dispatch + default mountpoint)
- Test: `go/internal/cli/mount/orchestrate_test.go`

**Interfaces:**
- Consumes: `ServeNFS`/`mountNFS`/`unmountPath` (Task 8), `ServeWebdav`/`mapWebdavDrive` (Task 9), `NewFSClient`/`NewBillyFS`/`NewWebdavFS` (Tasks 5-7); `resolveTarget`, `target.Agent.VolumeFsService`, `target.Agent.ContainerService.ListVolumes`.
- Produces:
  - `func DefaultMountpoint(deviceName, volume string) (string, error)` — `~/Wendy/<deviceName>/<volume>` (created), used on darwin/linux.
  - `func Run(ctx context.Context, opts Options) error` where `Options{ FS *FSClient, Protocol string, Mountpoint string, ReadOnly bool, DeviceName, Volume string, Stdout io.Writer }` — picks protocol by OS, serves, mounts, blocks until ctx is cancelled, then unmounts and stops the server.
  - `newMountCmd()` / `newUnmountCmd()` cobra commands.

- [ ] **Step 1: Write the failing test (default mountpoint logic)**

Create `go/internal/cli/mount/orchestrate_test.go`:

```go
package mount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultMountpoint(t *testing.T) {
	home, _ := os.UserHomeDir()
	mp, err := DefaultMountpoint("mydevice", "data")
	if err != nil {
		t.Fatalf("DefaultMountpoint: %v", err)
	}
	want := filepath.Join(home, "Wendy", "mydevice", "data")
	if mp != want {
		t.Fatalf("got %q want %q", mp, want)
	}
	if !strings.Contains(mp, "Wendy") {
		t.Fatal("expected Wendy in path")
	}
	if _, err := os.Stat(mp); err != nil {
		t.Fatalf("mountpoint not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/mount/ -run TestDefaultMountpoint -v`
Expected: FAIL — `undefined: DefaultMountpoint`.

- [ ] **Step 3: Write the orchestrator**

Create `go/internal/cli/mount/orchestrate.go`:

```go
package mount

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultMountpoint returns ~/Wendy/<device>/<volume>, creating it if absent.
func DefaultMountpoint(deviceName, volume string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	mp := filepath.Join(home, "Wendy", deviceName, volume)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	return mp, nil
}

type Options struct {
	FS         *FSClient
	Protocol   string // "nfs", "webdav", or "" for OS default
	Mountpoint string // path (unix) or drive letter like "W:" (windows)
	ReadOnly   bool
	DeviceName string
	Volume     string
	Stdout     io.Writer
}

func chooseProtocol(p string) string {
	if p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return "webdav"
	}
	return "nfs"
}

// Run serves the volume and mounts it, blocking until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	proto := chooseProtocol(opts.Protocol)
	switch proto {
	case "nfs":
		addr, stop, err := ServeNFS(ctx, NewBillyFS(opts.FS))
		if err != nil {
			return fmt.Errorf("starting NFS server: %w", err)
		}
		defer stop()
		if err := mountNFS(ctx, addr, opts.Mountpoint, opts.ReadOnly); err != nil {
			return fmt.Errorf("mounting: %w", err)
		}
		fmt.Fprintf(opts.Stdout, "Mounted %q at %s (Ctrl-C to unmount)\n", opts.Volume, opts.Mountpoint)
		<-ctx.Done()
		return unmountPath(opts.Mountpoint)
	case "webdav":
		addr, stop, err := ServeWebdav(ctx, NewWebdavFS(opts.FS))
		if err != nil {
			return fmt.Errorf("starting WebDAV server: %w", err)
		}
		defer stop()
		if err := mapWebdavDrive(ctx, addr, opts.Mountpoint); err != nil {
			return fmt.Errorf("mapping drive: %w", err)
		}
		fmt.Fprintf(opts.Stdout, "Mounted %q at %s (Ctrl-C to unmount)\n", opts.Volume, opts.Mountpoint)
		<-ctx.Done()
		return unmapWebdavDrive(opts.Mountpoint)
	default:
		return fmt.Errorf("unknown protocol %q (want nfs or webdav)", proto)
	}
}
```

- [ ] **Step 4: Run the orchestrator test**

Run: `go test ./go/internal/cli/mount/ -run TestDefaultMountpoint -v`
Expected: PASS.

- [ ] **Step 5: Write the cobra command**

Create `go/internal/cli/commands/device_mount.go`:

```go
package commands

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/mount"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func newMountCmd() *cobra.Command {
	var (
		protocol   string
		readOnly   bool
		mountpoint string
	)
	cmd := &cobra.Command{
		Use:   "mount <volume> [mountpoint]",
		Short: "Mount a persistent volume as a local drive",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			volume := args[0]
			if len(args) == 2 {
				mountpoint = args[1]
			}

			parentCtx := cmd.Context()
			target, err := resolveTarget(parentCtx)
			if err != nil {
				return err
			}
			defer target.Close()
			if target.Agent == nil {
				return fmt.Errorf("mounting requires a WendyOS device")
			}

			// Validate the volume exists and warn if a running app uses it.
			vols, err := target.Agent.ContainerService.ListVolumes(parentCtx, &agentpb.ListVolumesRequest{})
			if err != nil {
				return fmt.Errorf("listing volumes: %w", err)
			}
			found := false
			for _, v := range vols.GetVolumes() {
				if v.GetName() == volume {
					found = true
					if len(v.GetUsedBy()) > 0 {
						fmt.Fprintf(cmd.OutOrStdout(),
							"warning: volume %q is in use by %v; writes from your PC and the app can corrupt data\n",
							volume, v.GetUsedBy())
					}
				}
			}
			if !found {
				return fmt.Errorf("volume %q not found", volume)
			}

			deviceName := target.Agent.Host
			if mountpoint == "" && protocol != "webdav" {
				mountpoint, err = mount.DefaultMountpoint(deviceName, volume)
				if err != nil {
					return err
				}
			}
			if mountpoint == "" { // webdav default drive
				mountpoint = "W:"
			}

			// Cancel on SIGINT/SIGTERM for a clean unmount.
			ctx, stop := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			fsc := mount.NewFSClient(ctx, target.Agent.VolumeFsService, volume)
			return mount.Run(ctx, mount.Options{
				FS:         fsc,
				Protocol:   protocol,
				Mountpoint: mountpoint,
				ReadOnly:   readOnly,
				DeviceName: deviceName,
				Volume:     volume,
				Stdout:     cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "", "mount protocol: nfs or webdav (default: per-OS)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "mount read-only")
	return cmd
}

func newUnmountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unmount <mountpoint|drive>",
		Short: "Unmount a volume mounted by 'wendy device mount'",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mount.Unmount(args[0])
		},
	}
}
```

Add to `go/internal/cli/mount/orchestrate.go` a small dispatcher used by `unmount`:

```go
// Unmount tears down a mount by path (unix) or drive letter (windows).
func Unmount(target string) error {
	if runtime.GOOS == "windows" {
		return unmapWebdavDrive(target)
	}
	return unmountPath(target)
}
```

- [ ] **Step 6: Register the commands**

In `go/internal/cli/commands/device.go`, inside `newDeviceCmd()`, add to the existing `addToGroup("manage", ...)` block (next to `newVolumesCmd()`):

```go
		newMountCmd(),
		newUnmountCmd(),
```

- [ ] **Step 7: Build + run the package tests + verify the command is wired**

Run: `go build ./... && go test ./go/internal/cli/mount/ -v`
Run: `go run ./go/cmd/wendy device mount --help`
Expected: build clean, tests PASS, help shows the `mount` usage and `--protocol`/`--read-only` flags.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/device_mount.go go/internal/cli/commands/device.go go/internal/cli/mount/orchestrate.go go/internal/cli/mount/orchestrate_test.go
git commit -m "feat(cli): wendy device mount/unmount commands"
```

---

### Task 11: End-to-end adapter↔service integration test + docs

**Files:**
- Create: `go/internal/cli/mount/e2e_test.go`
- Create: `examples/claude-on-device/` doc note OR `go/internal/cli/mount/README.md` describing manual OS-mount verification.

**Interfaces:**
- Consumes: all prior tasks.

**Rationale:** A real OS-level `mount`/`net use` is environment-specific (needs an NFS client and, on CI, often privileges), so it is verified manually (documented). The automated E2E proves the *full software path*: cobra → FSClient → gRPC → VolumeFsService → disk, through the billy adapter, exactly as the NFS server would drive it.

- [ ] **Step 1: Write the E2E test**

Create `go/internal/cli/mount/e2e_test.go`:

```go
package mount

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestE2EBillyThroughGRPCToDisk drives the billy adapter (what the NFS server
// uses) end to end: write via the adapter, assert the bytes land in the volume
// directory on the "device", then read back through the adapter.
func TestE2EBillyThroughGRPCToDisk(t *testing.T) {
	c, root := newTestFSClient(t)
	fs := NewBillyFS(c)

	f, err := fs.Create("dir/nested.txt") // exercises auto-parent? no — create parent first
	if err == nil {
		_ = f.Close()
	}
	if err := fs.MkdirAll("dir", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wf, err := fs.Create("dir/nested.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := wf.Write([]byte("end to end")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = wf.Close()

	// Assert on the device side.
	onDisk := filepath.Join(root, "vol", "dir", "nested.txt")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read on disk: %v", err)
	}
	if string(got) != "end to end" {
		t.Fatalf("on-disk content %q", got)
	}

	// Read back through the adapter.
	rf, err := fs.Open("dir/nested.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	back, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(back) != "end to end" {
		t.Fatalf("read-back %q", back)
	}
}
```

- [ ] **Step 2: Run the E2E test**

Run: `go test ./go/internal/cli/mount/ -run TestE2E -v`
Expected: PASS.

- [ ] **Step 3: Write the manual-verification doc**

Create `go/internal/cli/mount/README.md`:

```markdown
# wendy device mount

Mounts a WendyOS persistent volume as a local network drive.

- macOS/Linux: userspace NFSv3 on 127.0.0.1, auto-mounted with `mount -t nfs`.
- Windows: WebDAV on 127.0.0.1, mapped with `net use`.

The device runs no file server; all I/O flows over the existing agent gRPC
channel (mTLS on LAN, or the cloud tunnel), via `WendyVolumeFsService`.

## Manual verification (real OS mount)

1. `wendy device mount <volume>`  (read-write by default; add `--read-only` to opt out)
2. Open the printed mountpoint (`~/Wendy/<device>/<volume>` on macOS/Linux, `W:` on Windows).
3. Copy a file in; confirm it appears on the device under
   `/var/lib/wendy/volumes/<volume>`.
4. Ctrl-C; confirm the mount is gone (`mount | grep <volume>` empty).

## Notes
- macOS may prompt to install command-line NFS on first use; it is built in.
- Windows requires the WebClient service running for WebDAV drive mapping.
- Mounting a volume in use by a running app is allowed but warns: concurrent
  writes can corrupt app data.
```

- [ ] **Step 4: Full suite + build**

Run: `go test ./go/internal/cli/mount/ ./go/internal/agent/services/ -v && go build ./...`
Expected: all PASS, build clean.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/mount/e2e_test.go go/internal/cli/mount/README.md
git commit -m "test(cli): volume mount E2E adapter test + usage docs"
```

---

## Self-Review

**Spec coverage:**
- Device-side `WendyVolumeFsService` with Stat/ReadDir/Read/Write/Create/Mkdir/Rmdir/Unlink/Rename/SetAttr/StatFs → Tasks 1, 3, 4. ✅
- Path scoping / escape rejection → Task 2. ✅
- Reuse existing transport, no new entitlement → Task 5 wires into `AgentConnection` which already carries mTLS/tunnel; command uses `resolveTarget`. ✅
- Host gateway, NFS (mac/linux) + WebDAV (windows), one backend two frontends → Tasks 5 (FSClient), 6 (billy), 7 (webdav), 8 (NFS server/mount), 9 (WebDAV server/map). ✅
- CLI `wendy device mount`/`unmount`, `--protocol`, `--read-only`, default mountpoint, used_by warning, foreground + Ctrl-C unmount → Task 10. ✅
- Read-write default, read-only opt-out → Task 10 flag + Task 8 readOnly plumbing. ✅
- Error mapping to gRPC codes / POSIX → Task 3 `osErrToStatus`. ✅
- StatFs for df/size bar → Tasks 1, 3. ✅
- Testing: agent unit (Tasks 2-4), host adapter unit (Tasks 5-7), E2E (Task 11). ✅
- Non-goals (detach, write-back cache, symlink creation, SMB, arbitrary paths) → not implemented; symlink creation explicitly returns `ErrNotSupported` (Task 6). ✅

**Placeholder scan:** No TBD/TODO. Every code step shows full code. Two flagged reconciliation points (billy/go-nfs exact method names per pinned version) are compiler-enforced, not placeholders.

**Type consistency:** `FSClient` method names are used identically across Tasks 5-10. `attrToFileInfo`/`fileInfo` defined in Task 6, reused in Task 7. `FileType_FILE_TYPE_*` enum constants consistent. `Options` struct fields match their use in Task 10's command. `mountNFS`/`unmountPath`/`mapWebdavDrive`/`unmapWebdavDrive` signatures consistent across build-tagged files and stubs.

**Known reconciliation risks (call out during execution):**
- `go-nfs` helper API (`NewNullAuthHandler`, `NewCachingHandler`) and `billy.Filesystem` method set are version-sensitive — pin versions, let the compiler enforce, reconcile via `go doc`.
- macOS `mount_nfs` option string for a userspace server on a non-standard port may need `-o resvport`/`nfsvers=3` tweaks on the actual OS version; verify during manual E2E (Task 11).
