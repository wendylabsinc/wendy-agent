package services

import "testing"

func TestCalculateDiskUsage(t *testing.T) {
	usage, ok := calculateDiskUsage(30_000_000, 29_415_000, 4_000)
	if !ok {
		t.Fatal("calculateDiskUsage returned false")
	}
	if usage.usedBytes != 2_340_000_000 {
		t.Fatalf("usedBytes = %d, want 2340000000", usage.usedBytes)
	}
	if usage.totalBytes != 120_000_000_000 {
		t.Fatalf("totalBytes = %d, want 120000000000", usage.totalBytes)
	}
}

func TestCalculateDiskUsageRejectsInvalidStats(t *testing.T) {
	tests := []struct {
		name       string
		blocks     uint64
		freeBlocks uint64
		blockSize  uint64
	}{
		{name: "zero blocks", blocks: 0, freeBlocks: 0, blockSize: 4096},
		{name: "zero block size", blocks: 1, freeBlocks: 0, blockSize: 0},
		{name: "free exceeds total", blocks: 1, freeBlocks: 2, blockSize: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := calculateDiskUsage(tt.blocks, tt.freeBlocks, tt.blockSize); ok {
				t.Fatal("calculateDiskUsage returned true")
			}
		})
	}
}
