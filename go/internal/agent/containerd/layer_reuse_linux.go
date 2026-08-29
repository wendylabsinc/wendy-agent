//go:build linux

package containerd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type clonedDirectory struct {
	source string
	target string
	info   fs.FileInfo
}

// cloneSnapshotUpper duplicates an immutable overlayfs upper directory using
// hard links for every non-directory inode. Source and target belong to the
// same overlay snapshotter filesystem, so this turns a multi-gigabyte model
// layer into directory metadata work while preserving regular-file contents,
// symlinks, hard links, device whiteouts, ownership, and xattrs exactly.
func cloneSnapshotUpper(targetRoot, sourceRoot string) error {
	var dirs []clonedDirectory
	err := filepath.WalkDir(sourceRoot, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, source)
		if err != nil {
			return err
		}
		target := targetRoot
		if rel != "." {
			target = filepath.Join(targetRoot, rel)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." {
				if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
					return fmt.Errorf("creating cloned layer directory %s: %w", rel, err)
				}
			}
			dirs = append(dirs, clonedDirectory{source: source, target: target, info: info})
			return nil
		}

		// link(2) does not dereference symbolic links. It also preserves special
		// overlayfs whiteout inodes and every inode xattr without reinterpretation.
		if err := os.Link(source, target); err != nil {
			return fmt.Errorf("hard-linking cloned layer entry %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Creating children changes directory timestamps, so restore directory
	// metadata in reverse order only after the tree is complete.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := copyClonedDirectoryMetadata(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func copyClonedDirectoryMetadata(dir clonedDirectory) error {
	stat, ok := dir.info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("reading directory stat for %s", dir.source)
	}
	if err := os.Lchown(dir.target, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("setting directory owner for %s: %w", dir.target, err)
	}
	if err := os.Chmod(dir.target, dir.info.Mode()); err != nil {
		return fmt.Errorf("setting directory mode for %s: %w", dir.target, err)
	}
	if err := copyDirectoryXattrs(dir.target, dir.source); err != nil {
		return fmt.Errorf("copying directory xattrs for %s: %w", dir.target, err)
	}
	times := []unix.Timespec{
		{Sec: stat.Atim.Sec, Nsec: stat.Atim.Nsec},
		{Sec: stat.Mtim.Sec, Nsec: stat.Mtim.Nsec},
	}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, dir.target, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("setting directory times for %s: %w", dir.target, err)
	}
	return nil
}

func copyDirectoryXattrs(target, source string) error {
	size, err := unix.Listxattr(source, nil)
	if err != nil {
		if err == unix.ENOTSUP {
			return nil
		}
		return err
	}
	if size == 0 {
		return nil
	}
	names := make([]byte, size)
	n, err := unix.Listxattr(source, names)
	if err != nil {
		return err
	}
	for _, raw := range splitNullTerminated(names[:n]) {
		valueSize, err := unix.Getxattr(source, raw, nil)
		if err != nil {
			return err
		}
		value := make([]byte, valueSize)
		if _, err := unix.Getxattr(source, raw, value); err != nil {
			return err
		}
		if err := unix.Setxattr(target, raw, value, 0); err != nil {
			return err
		}
	}
	return nil
}

func splitNullTerminated(data []byte) []string {
	var out []string
	for len(data) > 0 {
		idx := 0
		for idx < len(data) && data[idx] != 0 {
			idx++
		}
		if idx > 0 {
			out = append(out, string(data[:idx]))
		}
		if idx == len(data) {
			break
		}
		data = data[idx+1:]
	}
	return out
}
