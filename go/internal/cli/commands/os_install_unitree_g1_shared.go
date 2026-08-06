package commands

const unitreeG1MinDriveBytes = int64(1_000_000_000_000)

func unitreeG1DriveMeetsMinimumCapacity(sizeBytes int64) bool {
	return sizeBytes >= unitreeG1MinDriveBytes
}
