package services

import "math"

type diskUsage struct {
	usedBytes  int64
	totalBytes int64
}

func calculateDiskUsage(blocks, freeBlocks, blockSize uint64) (diskUsage, bool) {
	if blockSize == 0 || blocks == 0 || freeBlocks > blocks {
		return diskUsage{}, false
	}

	usedBlocks := blocks - freeBlocks
	if blocks > math.MaxInt64/blockSize || usedBlocks > math.MaxInt64/blockSize {
		return diskUsage{}, false
	}

	return diskUsage{
		usedBytes:  int64(usedBlocks * blockSize),
		totalBytes: int64(blocks * blockSize),
	}, true
}
