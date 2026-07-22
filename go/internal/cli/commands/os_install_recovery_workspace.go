package commands

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// prepareMutableWorkspace creates a per-run view of sourceDir: immutable files
// are hard-linked for zero-copy reads, while mutableSource gets a private copy.
// Callers must remove the returned workspace. The source cache is never opened
// for writing.
func prepareMutableWorkspace(sourceDir, mutableSource string) (workspace, mutableCopy string, err error) {
	mutableRel, err := filepath.Rel(sourceDir, mutableSource)
	if err != nil || mutableRel == ".." || strings.HasPrefix(mutableRel, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("mutable recovery file is outside its source directory")
	}
	workspace, err = os.MkdirTemp(filepath.Dir(sourceDir), ".wendy-recovery-run-")
	if err != nil {
		return "", "", err
	}
	err = filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil || rel == "." {
			return err
		}
		dst := filepath.Join(workspace, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("recovery workspace entry %s is not a regular file", rel)
		}
		if rel == mutableRel {
			return copyRegularFile(path, dst, info.Mode().Perm())
		}
		if err := os.Link(path, dst); err != nil {
			return fmt.Errorf("hard-linking immutable recovery file %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		_ = os.RemoveAll(workspace)
		return "", "", err
	}
	return workspace, filepath.Join(workspace, mutableRel), nil
}

func copyRegularFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
