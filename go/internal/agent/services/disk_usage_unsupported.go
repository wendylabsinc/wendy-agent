//go:build !(linux || darwin || freebsd)

package services

func rootDiskUsage() (diskUsage, bool) {
	return diskUsage{}, false
}

func listDiskPartitions() []partitionUsage {
	return nil
}
