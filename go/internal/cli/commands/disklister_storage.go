package commands

// StorageType identifies the underlying storage protocol of a drive.
type StorageType int

const (
	StorageUnknown StorageType = iota
	StorageNVMe
	StorageUSB
)

// writersForStorage returns how many concurrent writer goroutines the seekable
// bmap writer should use for a given storage type. NVMe/SSD has real write-queue
// depth and benefits from parallel WriteAt; SD/USB media (and unknown devices,
// which are usually card readers) are serial and get slower under scattered
// concurrent writes, so they write strictly sequentially.
func writersForStorage(t StorageType) int {
	if t == StorageNVMe {
		return 4
	}
	return 1
}
