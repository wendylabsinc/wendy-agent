//go:build linux

package containerd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/errdefs"
	"go.uber.org/zap"
)

type fakeReuseSnapshotter struct {
	root       string
	infos      map[string]snapshots.Info
	paths      map[string]string
	commitInfo snapshots.Info
}

func newFakeReuseSnapshotter(root string) *fakeReuseSnapshotter {
	return &fakeReuseSnapshotter{
		root:  root,
		infos: make(map[string]snapshots.Info),
		paths: make(map[string]string),
	}
}

func (f *fakeReuseSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, _ ...string) error {
	for _, info := range f.infos {
		if err := fn(ctx, info); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeReuseSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	info, ok := f.infos[key]
	if !ok {
		return snapshots.Info{}, errdefs.ErrNotFound
	}
	return info, nil
}

func (f *fakeReuseSnapshotter) View(_ context.Context, key, parent string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	source, ok := f.paths[parent]
	if !ok {
		return nil, errdefs.ErrNotFound
	}
	f.infos[key] = snapshots.Info{Name: key, Kind: snapshots.KindView, Parent: parent}
	return []mount.Mount{{Type: "bind", Source: source}}, nil
}

func (f *fakeReuseSnapshotter) Prepare(_ context.Context, key, parent string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	path := filepath.Join(f.root, key)
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, err
	}
	f.paths[key] = path
	f.infos[key] = snapshots.Info{Name: key, Kind: snapshots.KindActive, Parent: parent}
	return []mount.Mount{{Type: "bind", Source: path}}, nil
}

func (f *fakeReuseSnapshotter) Commit(_ context.Context, name, key string, opts ...snapshots.Opt) error {
	info := snapshots.Info{Name: name, Kind: snapshots.KindCommitted}
	for _, opt := range opts {
		if err := opt(&info); err != nil {
			return err
		}
	}
	f.commitInfo = info
	f.infos[name] = info
	f.paths[name] = f.paths[key]
	delete(f.infos, key)
	return nil
}

func (f *fakeReuseSnapshotter) Remove(_ context.Context, key string) error {
	delete(f.infos, key)
	return nil
}

func (f *fakeReuseSnapshotter) Update(_ context.Context, info snapshots.Info, _ ...string) (snapshots.Info, error) {
	f.infos[info.Name] = info
	return info, nil
}

func TestCloneSnapshotUpperHardLinksFilesAndCopiesDirectories(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(src, "models"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	weight := filepath.Join(src, "models", "yolo.pt")
	if err := os.WriteFile(weight, []byte("large immutable checkpoint"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("yolo.pt", filepath.Join(src, "models", "current.pt")); err != nil {
		t.Fatal(err)
	}

	if err := cloneSnapshotUpper(dst, src); err != nil {
		t.Fatalf("cloneSnapshotUpper: %v", err)
	}

	srcWeight, err := os.Lstat(weight)
	if err != nil {
		t.Fatal(err)
	}
	dstWeight, err := os.Lstat(filepath.Join(dst, "models", "yolo.pt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcWeight, dstWeight) {
		t.Fatal("regular file was copied instead of hard-linked")
	}

	srcLink, err := os.Lstat(filepath.Join(src, "models", "current.pt"))
	if err != nil {
		t.Fatal(err)
	}
	dstLink, err := os.Lstat(filepath.Join(dst, "models", "current.pt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcLink, dstLink) {
		t.Fatal("symlink inode was not hard-linked")
	}

	dirInfo, err := os.Stat(filepath.Join(dst, "models"))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o750 {
		t.Fatalf("directory mode = %o, want 750", got)
	}
}

func TestTryReuseLayerSnapshotClonesAndRebases(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	weight := filepath.Join(source, "weights.pt")
	if err := os.WriteFile(weight, []byte("checkpoint"), 0o644); err != nil {
		t.Fatal(err)
	}

	diffID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sn := newFakeReuseSnapshotter(root)
	sn.infos["old-chain"] = snapshots.Info{
		Name: "old-chain", Kind: snapshots.KindCommitted,
		Labels: map[string]string{labelKeyWendySnapshot: "true", labelKeyWendyDiffID: diffID},
	}
	sn.paths["old-chain"] = source

	c := &Client{logger: zap.NewNop(), snapshotter: "overlayfs"}
	if !c.tryReuseLayerSnapshot(context.Background(), context.Background(), sn, "image", 2, diffID, "new-parent", "new-chain") {
		t.Fatal("expected reusable layer snapshot")
	}
	if sn.commitInfo.Parent != "new-parent" {
		t.Fatalf("committed parent = %q, want new-parent", sn.commitInfo.Parent)
	}
	if sn.commitInfo.Labels[labelKeyWendyDiffID] != diffID {
		t.Fatalf("committed diff ID = %q", sn.commitInfo.Labels[labelKeyWendyDiffID])
	}
	newWeight, err := os.Lstat(filepath.Join(sn.paths["new-chain"], "weights.pt"))
	if err != nil {
		t.Fatal(err)
	}
	oldWeight, err := os.Lstat(weight)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(oldWeight, newWeight) {
		t.Fatal("reused layer did not hard-link the immutable weight")
	}
}
