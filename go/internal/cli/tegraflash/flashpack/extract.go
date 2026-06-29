package flashpack

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// extractZstTar extracts a .tar.zst into dest (created fresh). It is defensive
// against path traversal: every entry must stay within dest.
func extractZstTar(tarball, dest string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	// Extract into a temp sibling, then rename into place, so an interrupted
	// extraction never leaves a half-populated dir that open() would accept.
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if clean == "." {
			continue
		}
		if strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(tmp, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// The flashpack contains no links; skip rather than risk an escape.
			continue
		}
	}

	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}
