//go:build linux

package localmesh

import (
	"crypto"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wendylabsinc/WendyOS/go/babel"
)

type routePublicationSigner struct {
	crypto.Signer
	beforeSign func()
}

func (s *routePublicationSigner) Sign(random io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	s.beforeSign()
	return s.Signer.Sign(random, digest, opts)
}

// Construct only the publication dependencies. Calling Persist does not need a
// kernel or a live link, and refresh can read the real Routing engine's snapshot.
func routePublicationNode(t *testing.T) *Node {
	t.Helper()
	credentials, _ := testCredentials(t)
	now := time.Now()
	cache, err := OpenIdentityCache("", DefaultCacheLimits(), credentials.Verify, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Put(credentials.Certificate.Certificate, now); err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(credentials.Device.Tenant(), 10, cache)
	if err != nil {
		t.Fatal(err)
	}
	store, state, err := OpenStateStore(filepath.Join(t.TempDir(), "state.json"), credentials.Device, 1)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := NewRouting(babel.Config{RouterID: 1}, nil, &routingRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{
		Credentials: credentials, cache: cache, directory: directory, store: store,
		state: state, routing: routing, peers: make(map[babel.LinkID]*nodePeer), started: now,
		advertisement: Manifest{Version: ManifestVersionRoute, Device: credentials.Device.UUID(), AgentPort: 50052},
	}
	if err := directory.SetPersistence(nil, func(receipts []DirectoryReceipt) error {
		next := n.state
		next.Receipts = receipts
		if err := n.store.Save(next); err != nil {
			return err
		}
		n.state = next
		return nil
	}, now); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNodeRoutePublicationPersistsBeforeSigning(t *testing.T) {
	n := routePublicationNode(t)
	checkpoint := n.routing.Checkpoint()
	wantRevision := uint64(1)
	signatures := 0
	n.Credentials.Signer = &routePublicationSigner{Signer: n.Credentials.Signer, beforeSign: func() {
		signatures++
		// Read the real durable file at the point the private key is used.
		persisted, _, err := readPersistentState(n.store.path)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Babel == nil || persisted.Babel.Seqno != checkpoint.Seqno || persisted.Revision != wantRevision {
			t.Fatalf("signed before checkpoint/revision persistence: %+v", persisted)
		}
	}}
	if err := n.Persist(checkpoint); err != nil {
		t.Fatal(err)
	}
	if signatures != 1 {
		t.Fatalf("initial checkpoint signed %d announcements", signatures)
	}
	manifest := n.advertisement
	if manifest.Version != ManifestVersionRoute || manifest.Route == nil || manifest.Route.Seqno != checkpoint.Seqno || manifest.Route.RouterID != checkpoint.RouterID || manifest.Route.Prefix != netip.PrefixFrom(n.Credentials.Device.MeshAddress(), 128) {
		t.Fatalf("initial checkpoint produced incorrect route proof: %+v", manifest)
	}
	if manifest.Revision != 1 || manifest.Expires <= manifest.Issued || manifest.Expires-manifest.Issued > MaxLease.Milliseconds() {
		t.Fatalf("invalid publication lease: %+v", manifest)
	}
	// Another Directory must independently accept the emitted signature.
	verifier, err := NewDirectory(n.Credentials.Device.Tenant(), 10, n.cache)
	if err != nil {
		t.Fatal(err)
	}
	records := n.directory.Records(time.Now())
	if len(records) != 1 {
		t.Fatalf("expected one published proof, got %d", len(records))
	}
	if changed, err := verifier.Accept(records[0], time.Now()); err != nil || !changed {
		t.Fatalf("published signature failed verification: changed=%v err=%v", changed, err)
	}

	// Persisting unrelated Babel state must not consume an origin revision or
	// shorten the currently advertised lease by publishing another manifest.
	checkpoint.LastLink++
	if err := n.Persist(checkpoint); err != nil {
		t.Fatal(err)
	}
	if signatures != 1 || n.state.Revision != 1 || !reflect.DeepEqual(n.advertisement, manifest) {
		t.Fatal("unchanged sequence churned the signed announcement")
	}
	checkpoint.Seqno++
	wantRevision++
	if err := n.Persist(checkpoint); err != nil {
		t.Fatal(err)
	}
	if signatures != 2 || n.advertisement.Revision != wantRevision || n.advertisement.Route.Seqno != checkpoint.Seqno {
		t.Fatalf("new checkpoint sequence was not published: %+v", n.advertisement)
	}
}

func TestNodeRoutePublicationFailureDoesNotExposeNewProof(t *testing.T) {
	for _, phase := range []string{"checkpoint", "origin revision"} {
		t.Run(phase, func(t *testing.T) {
			n := routePublicationNode(t)
			checkpoint := n.routing.Checkpoint()
			if err := n.Persist(checkpoint); err != nil {
				t.Fatal(err)
			}
			before := n.advertisement
			records := n.directory.Records(time.Now())
			originalPath := n.store.path
			blocker := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(blocker, []byte("block writes"), 0600); err != nil {
				t.Fatal(err)
			}
			brokenPath := filepath.Join(blocker, "state.json")
			n.Credentials.Signer = &routePublicationSigner{Signer: n.Credentials.Signer, beforeSign: func() {
				t.Fatal("private key used despite failed persistence")
			}}
			if phase == "checkpoint" {
				n.store.path = brokenPath
			} else {
				// publish checks cached identity only after Persist saves the new
				// checkpoint. Fail the subsequent origin-revision write.
				verify := n.cache.verify
				n.cache.verify = func(chain [][]byte, now time.Time) (Identity, error) {
					n.store.path = brokenPath
					return verify(chain, now)
				}
			}
			checkpoint.Seqno++
			if err := n.Persist(checkpoint); err == nil {
				t.Fatal("persistence failure ignored")
			}
			if !reflect.DeepEqual(n.advertisement, before) || !reflect.DeepEqual(n.directory.Records(time.Now()), records) {
				t.Fatal("failed persistence exposed a new proof")
			}
			persisted, _, err := readPersistentState(originalPath)
			if err != nil {
				t.Fatal(err)
			}
			wantSequence := before.Route.Seqno
			if phase == "origin revision" {
				wantSequence = checkpoint.Seqno
			}
			if persisted.Babel.Seqno != wantSequence || persisted.Revision != before.Revision {
				t.Fatalf("unexpected durable state after failed %s write: %+v", phase, persisted)
			}
		})
	}
}

func TestNodeRoutePublicationSnapshotOwnsRouteBinding(t *testing.T) {
	n := routePublicationNode(t)
	if err := n.Persist(n.routing.Checkpoint()); err != nil {
		t.Fatal(err)
	}
	snapshot := n.Snapshot()
	if len(snapshot.Devices) != 1 || snapshot.Devices[0].Route == nil {
		t.Fatal("published proof missing from node snapshot")
	}
	want := *snapshot.Devices[0].Route
	if snapshot.Self != n.Credentials.Device || snapshot.CapturedAt.IsZero() || !snapshot.RouteAuthorizations[want] {
		t.Fatal("snapshot lacks its enrolled identity, capture time, or admitted route origin")
	}
	delete(snapshot.RouteAuthorizations, want)
	if !n.Snapshot().RouteAuthorizations[want] {
		t.Fatal("caller mutated node route authorization through snapshot")
	}
	snapshot.Devices[0].Route.Seqno++
	snapshot.Devices[0].Route.RouterID++
	if got := *n.Snapshot().Devices[0].Route; got != want {
		t.Fatalf("caller mutated node's route proof through snapshot: %+v", got)
	}
	if got := *n.directory.Snapshot(time.Now())[0].Route; got != want {
		t.Fatalf("caller mutated Directory's route proof through snapshot: %+v", got)
	}
}
