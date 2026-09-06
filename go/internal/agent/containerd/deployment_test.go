package containerd

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

type revisionContainerStore struct {
	entries           map[string]containers.Container
	failJournalUpdate bool
	failJournalDelete bool
}

func (s *revisionContainerStore) Get(_ context.Context, id string) (containers.Container, error) {
	v, ok := s.entries[id]
	if !ok {
		return v, errdefs.ErrNotFound
	}
	v.Labels = maps.Clone(v.Labels)
	return v, nil
}
func (s *revisionContainerStore) List(_ context.Context, filters ...string) ([]containers.Container, error) {
	var out []containers.Container
	for _, v := range s.entries {
		if len(filters) > 0 && v.Labels[labelRevisionContainer] == "" {
			continue
		}
		v.Labels = maps.Clone(v.Labels)
		out = append(out, v)
	}
	return out, nil
}
func (s *revisionContainerStore) Create(_ context.Context, v containers.Container) (containers.Container, error) {
	if _, ok := s.entries[v.ID]; ok {
		return v, errdefs.ErrAlreadyExists
	}
	v.Labels = maps.Clone(v.Labels)
	s.entries[v.ID] = v
	return v, nil
}
func (s *revisionContainerStore) Update(_ context.Context, v containers.Container, _ ...string) (containers.Container, error) {
	if s.failJournalUpdate && v.Labels[labelRevisionContainer] != "" {
		return v, errors.New("journal storage failure")
	}
	if _, ok := s.entries[v.ID]; !ok {
		return v, errdefs.ErrNotFound
	}
	v.Labels = maps.Clone(v.Labels)
	s.entries[v.ID] = v
	return v, nil
}
func (s *revisionContainerStore) Delete(_ context.Context, id string) error {
	if s.failJournalDelete && s.entries[id].Labels[labelRevisionContainer] != "" {
		return errors.New("journal deletion failure")
	}
	if _, ok := s.entries[id]; !ok {
		return errdefs.ErrNotFound
	}
	delete(s.entries, id)
	return nil
}

func TestDeleteDeploymentRevisionsRemovesRollbackRoots(t *testing.T) {
	for _, phase := range []string{"retained", "activating", "prepared"} {
		t.Run(phase, func(t *testing.T) {
			c, store, imgs, sn := revisionTestClient(t)
			tx := revisionTestTransaction(t, c, store, phase)
			previousAlias := "docker.io/wendy-revision/previous:candidate"
			candidateAlias := "docker.io/wendy-revision/candidate:candidate"
			tx.metadata.Original.Image = previousAlias
			tx.metadata.CandidateImage = candidateAlias
			tx.journal.Image = previousAlias
			encoded, err := encodeRevisionMetadata(tx.metadata)
			if err != nil {
				t.Fatal(err)
			}
			tx.journal.Extensions[revisionExtension] = encoded
			store.entries[tx.journal.ID] = tx.journal
			if err := c.DeleteDeploymentRevisions(context.Background(), []string{"app"}); err != nil {
				t.Fatal(err)
			}
			if len(store.entries) != 0 {
				t.Fatalf("retained metadata survived explicit deletion: %v", store.entries)
			}
			if !reflect.DeepEqual(sn.removed, []string{tx.journal.SnapshotKey}) {
				t.Fatalf("removed snapshots = %v", sn.removed)
			}
			slices.Sort(imgs.deleted)
			if !reflect.DeepEqual(imgs.deleted, []string{candidateAlias, previousAlias}) {
				t.Fatalf("removed immutable aliases = %v", imgs.deleted)
			}
			if err := c.RecoverDeployments(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.entries) != 0 {
				t.Fatal("agent restart resurrected an explicitly deleted app")
			}
		})
	}
}

func TestDeleteDeploymentRevisionsPreservesOtherSnapshotAndImageOwners(t *testing.T) {
	c, store, imgs, sn := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "retained")
	alias := "docker.io/wendy-revision/shared:candidate"
	tx.journal.Image = alias
	store.entries[tx.journal.ID] = tx.journal
	store.entries["other-live"] = containers.Container{ID: "other-live", Image: alias,
		Snapshotter: tx.journal.Snapshotter, SnapshotKey: tx.journal.SnapshotKey}
	other := tx.journal
	other.ID = "other-journal"
	other.Labels = map[string]string{labelRevisionContainer: "app-sibling", labelRevisionPhase: "retained"}
	store.entries[other.ID] = other
	if err := c.DeleteDeploymentRevisions(context.Background(), []string{"app"}); err != nil {
		t.Fatal(err)
	}
	if len(sn.removed) != 0 || len(imgs.deleted) != 0 {
		t.Fatalf("deleted resources still in use: snapshots=%v images=%v", sn.removed, imgs.deleted)
	}
	if len(store.entries) != 2 {
		t.Fatalf("deleted another container or journal: %v", store.entries)
	}
}

func TestRecoverDeploymentDeletionTombstoneNeverRestoresApp(t *testing.T) {
	c, store, _, sn := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	store.failJournalDelete = true
	if err := c.DeleteDeploymentRevisions(context.Background(), []string{"app"}); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if store.entries[tx.journal.ID].Labels[labelRevisionPhase] != "deleted" {
		t.Fatal("cleanup failure did not persist deletion intent")
	}
	store.failJournalDelete = false
	if err := c.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.entries) != 0 || !reflect.DeepEqual(sn.removed, []string{tx.journal.SnapshotKey}) {
		t.Fatalf("recovery failed to finish deletion: metadata=%v removed=%v", store.entries, sn.removed)
	}
}

func TestRuntimeCleanupGetsFreshBoundedContext(t *testing.T) {
	c, _, _, _ := revisionTestClient(t)
	for range 2 {
		var usedCtx context.Context
		if err := c.cleanupRuntimeOperation(func(ctx context.Context) error {
			usedCtx = ctx
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 4*time.Second || time.Until(deadline) > 5*time.Second {
				t.Fatalf("cleanup deadline is not freshly bounded: %v, %v", deadline, ok)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(usedCtx.Err(), context.Canceled) {
			t.Fatal("cleanup did not release its context")
		}
	}
}

func TestDeletedJournalRecoveryPreservesNewerDeploymentForSameApp(t *testing.T) {
	c, store, _, sn := revisionTestClient(t)
	old := revisionTestTransaction(t, c, store, "deleted").journal
	old.ID = "older-deleted-journal"
	store.entries[old.ID] = old
	tx := revisionTestTransaction(t, c, store, "activating")
	tx.metadata.Original.SnapshotKey = "newer-original-rootfs"
	tx.metadata.Original.Labels[labelKeyAppVersion] = "newer-version"
	tx.journal.SnapshotKey = tx.metadata.Original.SnapshotKey
	encoded, err := encodeRevisionMetadata(tx.metadata)
	if err != nil {
		t.Fatal(err)
	}
	tx.journal.Extensions[revisionExtension] = encoded
	store.entries[tx.journal.ID] = tx.journal
	if err := c.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	live := store.entries["app"]
	if live.SnapshotKey != "newer-original-rootfs" || live.Labels[labelKeyAppVersion] != "newer-version" {
		t.Fatalf("older deletion lost the newer deployment recovery state: %+v", live)
	}
	if !reflect.DeepEqual(sn.removed, []string{old.SnapshotKey}) {
		t.Fatalf("deleted snapshots = %v; expected only older deleted rootfs", sn.removed)
	}
}

type revisionImageStore struct {
	images.Store
	getCalls int
	deleted  []string
}

func (s *revisionImageStore) Get(context.Context, string) (images.Image, error) {
	s.getCalls++
	return images.Image{}, errors.New("mutable image must not be consulted")
}
func (s *revisionImageStore) Delete(_ context.Context, name string, _ ...images.DeleteOpt) error {
	s.deleted = append(s.deleted, name)
	return nil
}

type revisionSnapshots struct {
	snapshots.Snapshotter
	removed []string
}

func (s *revisionSnapshots) Remove(_ context.Context, key string) error {
	s.removed = append(s.removed, key)
	return nil
}

func revisionTestClient(t *testing.T) (*Client, *revisionContainerStore, *revisionImageStore, *revisionSnapshots) {
	t.Helper()
	store := &revisionContainerStore{entries: map[string]containers.Container{}}
	imgs := &revisionImageStore{}
	sn := &revisionSnapshots{}
	client, err := containerd.New("", containerd.WithServices(containerd.WithContainerStore(store), containerd.WithImageStore(imgs), containerd.WithSnapshotters(map[string]snapshots.Snapshotter{"overlayfs": sn})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &Client{client: client, namespace: "test", logger: zap.NewNop()}, store, imgs, sn
}
func revisionOriginal() containers.Container {
	return containers.Container{ID: "app", Labels: map[string]string{labelKeyAppID: "app", labelKeyAppVersion: "old", labelKeyRestartPolicy: "no"}, Image: "app:latest",
		Runtime:     containers.RuntimeInfo{Name: "io.containerd.runc.v2", Options: &anypb.Any{TypeUrl: "runtime/options", Value: []byte{1, 2}}},
		Spec:        &anypb.Any{TypeUrl: "runtime/spec", Value: []byte(`{"process":{"args":["old"],"env":["OLD=1"]}}`)},
		Snapshotter: "overlayfs", SnapshotKey: "old-writable-rootfs", Extensions: map[string]typeurl.Any{"custom": &anypb.Any{TypeUrl: "custom/type", Value: []byte{3, 4}}}}
}
func revisionTestTransaction(t *testing.T, c *Client, store *revisionContainerStore, phase string) *deploymentTransaction {
	t.Helper()
	original := revisionOriginal()
	m := retainedMetadata{Original: retainContainer(original), Revision: "candidate", PreviousRevision: "previous", CandidateImage: "wendy-revision/candidate:candidate"}
	ext, err := encodeRevisionMetadata(m)
	if err != nil {
		t.Fatal(err)
	}
	journal := containers.Container{ID: "wendy-revision-candidate", Labels: map[string]string{labelRevisionContainer: "app", labelRevisionPhase: phase},
		SnapshotKey: original.SnapshotKey, Snapshotter: original.Snapshotter, Spec: original.Spec, Runtime: original.Runtime, Extensions: map[string]typeurl.Any{revisionExtension: ext}}
	store.entries[journal.ID] = journal
	return &deploymentTransaction{c: c, journal: journal, metadata: m, resume: func() {}, activated: phase == "activating"}
}

func TestRetainedContainerRoundTripPreservesRuntimeSnapshotAndSpec(t *testing.T) {
	original := revisionOriginal()
	m := retainedMetadata{Original: retainContainer(original), Revision: "r", CandidateImage: "immutable"}
	encoded, err := encodeRevisionMetadata(m)
	if err != nil {
		t.Fatal(err)
	}
	journal := containers.Container{ID: "hidden", Labels: map[string]string{labelRevisionContainer: original.ID}, Extensions: map[string]typeurl.Any{revisionExtension: encoded}}
	got, err := decodeRevisionMetadata(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Original.container(), original) {
		t.Fatalf("retained revision changed:\n%#v\n%#v", got.Original.container(), original)
	}
	original.Labels[labelKeyAppVersion] = "mutated"
	if got.Original.Labels[labelKeyAppVersion] != "old" {
		t.Fatal("retained labels alias mutable live metadata")
	}
}

func TestRollbackRestoresExactSnapshotWithoutReadingMutableImage(t *testing.T) {
	c, store, imgs, sn := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	// Simulate failed candidate creation after the old metadata was removed.
	if _, err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.entries["app"]
	want := revisionOriginal()
	want.Labels[labelDeploymentRevision] = "previous"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored metadata differs:\n%#v\n%#v", got, want)
	}
	if err := tx.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if imgs.getCalls != 0 {
		t.Fatal("rollback resolved the mutable image tag")
	}
	if len(sn.removed) != 0 {
		t.Fatalf("rollback/close deleted retained rootfs: %v", sn.removed)
	}
	if _, ok := store.entries["app"]; !ok {
		t.Fatal("close deleted the restored container")
	}
	if _, ok := store.entries[tx.journal.ID]; ok {
		t.Fatal("restored journal was not cleaned up")
	}
}

func TestCommitRetainsPreviousSnapshotAndCloseDoesNotDeleteIt(t *testing.T) {
	c, store, _, sn := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	store.entries["app"] = containers.Container{ID: "app", SnapshotKey: "candidate-rootfs", Snapshotter: "overlayfs", Labels: map[string]string{labelDeploymentRevision: "candidate", labelDeploymentPending: "candidate"}}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, ok := store.entries[tx.journal.ID]
	if !ok || journal.SnapshotKey != "old-writable-rootfs" || journal.Labels[labelRevisionPhase] != "retained" {
		t.Fatalf("previous snapshot not retained: %#v", journal)
	}
	if journal.Labels[labelKeyAppVersion] != "" || journal.Labels[labelKeyAppID] != "" {
		t.Fatal("archive is visible as a runnable Wendy app")
	}
	if len(sn.removed) != 0 {
		t.Fatalf("commit deleted rollback snapshot: %v", sn.removed)
	}
	if deploymentIsPending(store.entries["app"].Labels) {
		t.Fatal("committed container remains pending")
	}
}

func TestCommitCleanupFailureDoesNotTurnSuccessIntoRollbackFailure(t *testing.T) {
	c, store, _, _ := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	store.entries["app"] = containers.Container{ID: "app", Labels: map[string]string{labelDeploymentRevision: "candidate", labelDeploymentPending: "candidate"}}
	store.failJournalUpdate = true
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("durably committed healthy revision reported failure: %v", err)
	}
	if !tx.committed || deploymentIsPending(store.entries["app"].Labels) {
		t.Fatal("commit point not persisted")
	}
}

func TestRecoverDeploymentAfterCutoverRestoresPriorRootfs(t *testing.T) {
	c, store, imgs, sn := revisionTestClient(t)
	revisionTestTransaction(t, c, store, "activating")
	if err := c.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.entries["app"].SnapshotKey; got != "old-writable-rootfs" {
		t.Fatalf("snapshot=%q", got)
	}
	if imgs.getCalls != 0 || len(sn.removed) != 0 {
		t.Fatal("recovery used mutable image or deleted retained snapshot")
	}
}

func TestRecoverRecognizesDurableCommitBeforeJournalFinalization(t *testing.T) {
	c, store, _, _ := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	store.entries["app"] = containers.Container{ID: "app", SnapshotKey: "candidate-rootfs", Labels: map[string]string{labelDeploymentRevision: "candidate"}}
	if err := c.RecoverDeployments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.entries["app"].SnapshotKey != "candidate-rootfs" {
		t.Fatal("recovery rolled back committed candidate")
	}
	if store.entries[tx.journal.ID].Labels[labelRevisionPhase] != "retained" {
		t.Fatal("recovery did not finalize journal")
	}
}

func TestCloseUnactivatedPreparationNeverRemovesExistingSnapshot(t *testing.T) {
	c, store, _, sn := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "prepared")
	store.entries["app"] = revisionOriginal()
	if err := tx.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sn.removed) != 0 || store.entries["app"].SnapshotKey != "old-writable-rootfs" {
		t.Fatal("abandoned prepare disturbed old rootfs")
	}
}

func TestRevisionPruneReleasesSupersededImageButKeepsPrevious(t *testing.T) {
	c, store, imgs, _ := revisionTestClient(t)
	tx := revisionTestTransaction(t, c, store, "activating")
	oldAlias := "docker.io/wendy-revision/old:candidate"
	previousAlias := "docker.io/wendy-revision/previous:candidate"
	tx.metadata.Original.Image = previousAlias
	tx.journal.Image = previousAlias
	encoded, err := encodeRevisionMetadata(tx.metadata)
	if err != nil {
		t.Fatal(err)
	}
	tx.journal.Extensions[revisionExtension] = encoded
	store.entries[tx.journal.ID] = tx.journal
	oldMetadata := retainedMetadata{Revision: "previous", CandidateImage: previousAlias}
	oldEncoded, err := encodeRevisionMetadata(oldMetadata)
	if err != nil {
		t.Fatal(err)
	}
	store.entries["older-journal"] = containers.Container{ID: "older-journal", Image: oldAlias,
		Labels: map[string]string{labelRevisionContainer: "app", labelRevisionPhase: "retained"}, Extensions: map[string]typeurl.Any{revisionExtension: oldEncoded}}
	if err := tx.pruneOlderRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(imgs.deleted, []string{oldAlias}) {
		t.Fatalf("deleted images=%v; want only superseded alias", imgs.deleted)
	}
}

func TestRetainedRevisionIsRealContainerdSnapshotGCRoot(t *testing.T) {
	root := t.TempDir()
	db, err := bolt.Open(filepath.Join(root, "metadata.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	content, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		t.Fatal(err)
	}
	nativeSN, err := native.NewSnapshotter(filepath.Join(root, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nativeSN.Close() })
	mdb := metadata.NewDB(db, content, map[string]snapshots.Snapshotter{"native": nativeSN})
	ctx := namespaces.WithNamespace(context.Background(), "revision-test")
	if err := mdb.Init(ctx); err != nil {
		t.Fatal(err)
	}
	store := metadata.NewContainerStore(mdb)
	sn := mdb.Snapshotter("native")
	if _, err := sn.Prepare(ctx, "old-rootfs", ""); err != nil {
		t.Fatal(err)
	}
	original := revisionOriginal()
	original.Snapshotter, original.SnapshotKey = "native", "old-rootfs"
	if _, err := store.Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	archive := original
	archive.ID = "hidden-revision"
	archive.Image = ""
	archive.Labels = map[string]string{labelRevisionContainer: "app", labelRevisionPhase: "retained"}
	if _, err := store.Create(ctx, archive); err != nil {
		t.Fatal(err)
	}
	// Activation removes only old metadata; the hidden revision takes over
	// ownership of the exact active snapshot, even after actual containerd GC.
	if err := store.Delete(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mdb.GarbageCollect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sn.Stat(ctx, "old-rootfs"); err != nil {
		t.Fatalf("retained rootfs was garbage collected: %v", err)
	}
	if err := store.Delete(ctx, archive.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mdb.GarbageCollect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sn.Stat(ctx, "old-rootfs"); !errdefs.IsNotFound(err) {
		t.Fatalf("unreferenced rootfs should become collectible, got %v", err)
	}
}
