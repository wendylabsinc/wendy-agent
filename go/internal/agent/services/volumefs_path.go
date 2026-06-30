package services

import (
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// volumeRoot validates the volume name and returns its on-disk root directory.
// The name must be a single path segment (no separators, no "..").
func volumeRoot(volume string) (string, error) {
	if volume == "" || strings.ContainsAny(volume, `/\`) || volume == "." || volume == ".." {
		return "", status.Errorf(codes.InvalidArgument, "invalid volume name %q", volume)
	}
	if strings.IndexFunc(volume, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", status.Errorf(codes.InvalidArgument, "invalid volume name %q", volume)
	}
	return filepath.Join(volumesDir, volume), nil
}

// resolveVolumePath maps a volume-relative path to an absolute on-disk path
// confined to volumesDir/<volume>. Absolute inputs are treated as relative to
// the volume root. Any existing symlink component is resolved and must also
// remain within the root, preventing symlink-based escapes.
func resolveVolumePath(volume, relPath string) (string, error) {
	root, err := volumeRoot(volume)
	if err != nil {
		return "", err
	}

	// Strip any leading slash so both "/" and "sub/file" are treated as
	// volume-relative. filepath.Clean inside Join normalises ".." components.
	rel := strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	full := filepath.Join(root, filepath.FromSlash(rel))
	// Verify we didn't escape after normalisation.
	if !withinRoot(root, full) {
		return "", status.Errorf(codes.InvalidArgument, "path %q escapes volume", relPath)
	}

	// Resolve symlinks so that a symlink pointing outside the volume is caught.
	// Also resolve root itself to handle OS-level symlinks (e.g. /var → /private/var).
	// We use the longest existing prefix so that non-existent leaf paths are still checked.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot, _ = evalSymlinksPartial(root)
	}
	resolved, err := evalSymlinksPartial(full)
	if err == nil && !withinRoot(realRoot, resolved) {
		return "", status.Errorf(codes.InvalidArgument, "path %q resolves outside volume", relPath)
	}
	return full, nil
}

// evalSymlinksPartial resolves symlinks on the longest existing prefix of p,
// appending any non-existent suffix back on. This lets us detect symlink
// escapes even when the final path component does not yet exist.
func evalSymlinksPartial(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	suffix := filepath.Base(p)
	dir := filepath.Dir(p)
	for dir != p {
		resolved, err = filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(resolved, suffix), nil
		}
		suffix = filepath.Join(filepath.Base(dir), suffix)
		p = dir
		dir = filepath.Dir(dir)
	}
	return p, nil
}

func withinRoot(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}
