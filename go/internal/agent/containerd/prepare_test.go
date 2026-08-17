package containerd

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForChunksUnblocksWhenChunkIsStaged(t *testing.T) {
	dir := t.TempDir()
	index, err := NewChunkIndex(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{chunkIndex: index, staging: newStaging(filepath.Join(dir, "staging"))}
	data := []byte("arrives while preparation is waiting")
	hash := sha256.Sum256(data)

	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- c.waitForChunks(context.Background(), [][32]byte{hash})
		}()
	}

	if err := c.StageChunk(context.Background(), hash, data); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("waitForChunks: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("waitForChunks did not broadcast the staged chunk")
		}
	}
}

func TestWaitForChunksHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	index, err := NewChunkIndex(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{chunkIndex: index, staging: newStaging(filepath.Join(dir, "staging"))}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = c.waitForChunks(ctx, [][32]byte{{1}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForChunks error = %v, want context.Canceled", err)
	}
}

func TestPrepareChunkHashesRejectsMalformedHash(t *testing.T) {
	if _, err := prepareChunkHashes([][]byte{{1, 2, 3}}); err == nil {
		t.Fatal("expected malformed chunk hash error")
	}
}
