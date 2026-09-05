package containerd

import (
	"context"
	"time"
)

// Cleanup can run long after preparation and after its caller disconnects.
// Allocate the deadline when cleanup starts, never when the snapshot is staged.
func (c *Client) cleanupRuntimeOperation(operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(c.withNamespace(context.Background()), 5*time.Second)
	defer cancel()
	return operation(ctx)
}

// preparedSnapshot is a fresh active snapshot that has never backed a task.
// remove and release are idempotent at the containerd layer; the small once
// guards keep our bookkeeping deterministic under replacement/close races.
type preparedSnapshot struct {
	key    string
	parent string

	release func()
	remove  func()
}

func (p *preparedSnapshot) releaseLease() {
	if p != nil && p.release != nil {
		p.release()
		p.release = nil
	}
}

func (p *preparedSnapshot) discard() {
	if p == nil {
		return
	}
	if p.remove != nil {
		p.remove()
		p.remove = nil
	}
	p.releaseLease()
}

// storePreparedSnapshot keeps at most one staged rootfs per normalized image.
// A newer preparation supersedes an abandoned one and cleans it immediately.
func (c *Client) storePreparedSnapshot(imageName string, prepared *preparedSnapshot) {
	c.preparedSnapshotsMu.Lock()
	if c.preparedSnapshots == nil {
		c.preparedSnapshots = make(map[string]*preparedSnapshot)
	}
	old := c.preparedSnapshots[imageName]
	c.preparedSnapshots[imageName] = prepared
	c.preparedSnapshotsMu.Unlock()
	old.discard()
}

// takePreparedSnapshot transfers single-use ownership to the create path. A
// parent mismatch means the image name was repointed after preparation; discard
// the stale rootfs and let create use the normal path.
func (c *Client) takePreparedSnapshot(imageName, parent string) *preparedSnapshot {
	c.preparedSnapshotsMu.Lock()
	prepared := c.preparedSnapshots[imageName]
	delete(c.preparedSnapshots, imageName)
	c.preparedSnapshotsMu.Unlock()
	if prepared != nil && prepared.parent != parent {
		prepared.discard()
		return nil
	}
	return prepared
}

func (c *Client) discardAllPreparedSnapshots() {
	c.preparedSnapshotsMu.Lock()
	all := c.preparedSnapshots
	c.preparedSnapshots = make(map[string]*preparedSnapshot)
	c.preparedSnapshotsMu.Unlock()
	for _, prepared := range all {
		prepared.discard()
	}
}
