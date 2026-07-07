//go:build linux

package diskspace

import "golang.org/x/sys/unix"

// FreePercent returns the percentage of the filesystem containing path that is
// free, and true on success. It returns (0, false) on any statfs error so the
// caller can treat the measurement as unavailable and skip any disk-pressure
// action. Free space is computed from Bfree/Blocks (all blocks, matching the
// agent's own disk-usage reporting in agent/services) rather than Bavail, so the
// percentage lines up with what `wendy device doctor` shows.
func FreePercent(path string) (float64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, false
	}
	if st.Blocks == 0 {
		return 0, false
	}
	return float64(st.Bfree) / float64(st.Blocks) * 100, true
}
