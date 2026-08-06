package commands

import "testing"

// Real `mount` output from a Mac with a WendyOS card in the built-in reader.
const sampleMountOutput = `/dev/disk3s1s1 on / (apfs, sealed, local, read-only, journaled)
devfs on /dev (devfs, local, nobrowse)
/dev/disk3s5 on /System/Volumes/Data (apfs, local, journaled, nobrowse)
/dev/disk11s2 on /Volumes/config (msdos, local, nodev, nosuid, noowners, noatime, fskit)
/dev/disk11s1 on /Volumes/boot (msdos, local, nodev, nosuid, noowners, noatime, fskit)`

func TestParseMountPoint(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		partDev string
		want    string
	}{
		{
			name:    "mounted config partition",
			output:  sampleMountOutput,
			partDev: "/dev/disk11s2",
			want:    "/Volumes/config",
		},
		{
			name:    "a different partition on the same disk",
			output:  sampleMountOutput,
			partDev: "/dev/disk11s1",
			want:    "/Volumes/boot",
		},
		{
			name:    "not mounted",
			output:  sampleMountOutput,
			partDev: "/dev/disk11s3",
			want:    "",
		},
		{
			// A prefix match must not be mistaken for the device itself:
			// disk1s2 is not disk11s2.
			name:    "device name is a prefix of another",
			output:  sampleMountOutput,
			partDev: "/dev/disk1s2",
			want:    "",
		},
		{
			name:    "mount point containing spaces",
			output:  `/dev/disk9s1 on /Volumes/My Card (msdos, local, noowners)`,
			partDev: "/dev/disk9s1",
			want:    "/Volumes/My Card",
		},
		{
			// A mount point with " (" in its name: only the LAST one opens the
			// options list.
			name:    "mount point containing an open paren",
			output:  `/dev/disk9s1 on /Volumes/Card (2) (msdos, local, noowners)`,
			partDev: "/dev/disk9s1",
			want:    "/Volumes/Card (2)",
		},
		{
			name:    "empty output",
			output:  "",
			partDev: "/dev/disk11s2",
			want:    "",
		},
		{
			name:    "malformed line without options",
			output:  `/dev/disk11s2 on /Volumes/config`,
			partDev: "/dev/disk11s2",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMountPoint(tt.output, tt.partDev); got != tt.want {
				t.Errorf("parseMountPoint(_, %q) = %q, want %q", tt.partDev, got, tt.want)
			}
		})
	}
}
