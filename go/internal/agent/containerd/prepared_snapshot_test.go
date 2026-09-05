package containerd

import "testing"

func trackedPrepared(key, parent string, removed, released *int) *preparedSnapshot {
	return &preparedSnapshot{
		key: key, parent: parent,
		remove:  func() { (*removed)++ },
		release: func() { (*released)++ },
	}
}

func TestPreparedSnapshotConsumedExactlyOnce(t *testing.T) {
	c := &Client{}
	removed, released := 0, 0
	p := trackedPrepared("fresh", "chain", &removed, &released)
	c.storePreparedSnapshot("demo:latest", p)

	if got := c.takePreparedSnapshot("demo:latest", "chain"); got != p {
		t.Fatalf("take = %p, want %p", got, p)
	}
	if got := c.takePreparedSnapshot("demo:latest", "chain"); got != nil {
		t.Fatalf("second take = %#v, want nil", got)
	}
	p.releaseLease()
	if removed != 0 || released != 1 {
		t.Fatalf("removed=%d released=%d, want 0/1", removed, released)
	}
}

func TestPreparedSnapshotReplacementAndMismatchDiscard(t *testing.T) {
	c := &Client{}
	removed, released := 0, 0
	c.storePreparedSnapshot("demo:latest", trackedPrepared("old", "old-chain", &removed, &released))
	c.storePreparedSnapshot("demo:latest", trackedPrepared("new", "new-chain", &removed, &released))
	if removed != 1 || released != 1 {
		t.Fatalf("replacement cleanup removed=%d released=%d, want 1/1", removed, released)
	}
	if got := c.takePreparedSnapshot("demo:latest", "different-chain"); got != nil {
		t.Fatalf("mismatched take = %#v, want nil", got)
	}
	if removed != 2 || released != 2 {
		t.Fatalf("mismatch cleanup removed=%d released=%d, want 2/2", removed, released)
	}
}

func TestPreparedSnapshotCloseDiscardsAll(t *testing.T) {
	c := &Client{}
	removed, released := 0, 0
	c.storePreparedSnapshot("one:latest", trackedPrepared("one", "chain", &removed, &released))
	c.storePreparedSnapshot("two:latest", trackedPrepared("two", "chain", &removed, &released))
	c.discardAllPreparedSnapshots()
	if removed != 2 || released != 2 {
		t.Fatalf("close cleanup removed=%d released=%d, want 2/2", removed, released)
	}
}
