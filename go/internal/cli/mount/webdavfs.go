package mount

import (
	"context"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type webdavFS struct{ c *FSClient }

// NewWebdavFS adapts an FSClient to a webdav.FileSystem for the Windows frontend.
func NewWebdavFS(c *FSClient) webdav.FileSystem { return &webdavFS{c: c} }

func rel(name string) string { return strings.TrimPrefix(path.Clean("/"+name), "/") }

func (w *webdavFS) Mkdir(_ context.Context, name string, perm os.FileMode) error {
	return w.c.Mkdir(rel(name), uint32(perm.Perm()))
}

func (w *webdavFS) OpenFile(_ context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	abs := rel(name)
	if flag&os.O_CREATE != 0 {
		if _, err := w.c.Create(abs, uint32(perm.Perm())); err != nil {
			return nil, err
		}
	} else {
		if _, err := w.c.Stat(abs); err != nil {
			return nil, err
		}
	}
	if flag&os.O_TRUNC != 0 {
		if err := w.c.Truncate(abs, 0); err != nil {
			return nil, err
		}
	}
	f := &webdavFile{c: w.c, name: abs}
	if flag&os.O_APPEND != 0 {
		at, err := w.c.Stat(abs)
		if err != nil {
			return nil, err
		}
		f.off = at.GetSize()
	}
	return f, nil
}

func (w *webdavFS) RemoveAll(_ context.Context, name string) error {
	abs := rel(name)
	at, err := w.c.Stat(abs)
	if err != nil {
		return err
	}
	if at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR {
		return w.c.Rmdir(abs)
	}
	return w.c.Unlink(abs)
}

func (w *webdavFS) Rename(_ context.Context, oldName, newName string) error {
	return w.c.Rename(rel(oldName), rel(newName))
}

func (w *webdavFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	at, err := w.c.Stat(rel(name))
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(rel(name)), at), nil
}

type webdavFile struct {
	c       *FSClient
	name    string
	off     int64
	dirSnap []os.FileInfo // nil until first Readdir
	dirPos  int
}

func (f *webdavFile) Read(p []byte) (int, error) {
	data, eof, err := f.c.ReadAt(f.name, f.off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	f.off += int64(n)
	if eof {
		return n, io.EOF
	}
	return n, nil
}

func (f *webdavFile) Write(p []byte) (int, error) {
	n, err := f.c.WriteAt(f.name, f.off, p)
	f.off += int64(n)
	return n, err
}

func (f *webdavFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		at, err := f.c.Stat(f.name)
		if err != nil {
			return 0, err
		}
		f.off = at.GetSize() + offset
	default:
		return 0, os.ErrInvalid
	}
	return f.off, nil
}

func (f *webdavFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.dirSnap == nil {
		entries, err := f.c.ReadDir(f.name)
		if err != nil {
			return nil, err
		}
		f.dirSnap = make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			f.dirSnap = append(f.dirSnap, attrToFileInfo(e.GetName(), e.GetAttr()))
		}
	}
	if count <= 0 {
		rest := f.dirSnap[f.dirPos:]
		f.dirPos = len(f.dirSnap)
		return rest, nil
	}
	if f.dirPos >= len(f.dirSnap) {
		return nil, io.EOF
	}
	end := f.dirPos + count
	if end > len(f.dirSnap) {
		end = len(f.dirSnap)
	}
	batch := f.dirSnap[f.dirPos:end]
	f.dirPos = end
	return batch, nil
}

func (f *webdavFile) Stat() (os.FileInfo, error) {
	at, err := f.c.Stat(f.name)
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(f.name), at), nil
}

func (f *webdavFile) Close() error { return nil }
