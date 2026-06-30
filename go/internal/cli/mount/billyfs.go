package mount

import (
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type billyFS struct {
	c    *FSClient
	root string // chroot prefix, "" at top
}

// NewBillyFS adapts an FSClient to a billy.Filesystem for the NFS frontend.
func NewBillyFS(c *FSClient) billy.Filesystem { return &billyFS{c: c} }

func (b *billyFS) abs(name string) string {
	return strings.TrimPrefix(path.Join("/", b.root, path.Clean("/"+name)), "/")
}

func (b *billyFS) Create(filename string) (billy.File, error) {
	if _, err := b.c.Create(b.abs(filename), 0o644); err != nil {
		return nil, err
	}
	return &billyFile{c: b.c, name: filename, abs: b.abs(filename)}, nil
}

func (b *billyFS) Open(filename string) (billy.File, error) {
	return b.OpenFile(filename, os.O_RDONLY, 0)
}

func (b *billyFS) OpenFile(filename string, flag int, perm os.FileMode) (billy.File, error) {
	abs := b.abs(filename)
	if flag&os.O_CREATE != 0 {
		if _, err := b.c.Create(abs, uint32(perm.Perm())); err != nil {
			return nil, err
		}
	} else if _, err := b.c.Stat(abs); err != nil {
		return nil, err
	}
	if flag&os.O_TRUNC != 0 {
		if err := b.c.Truncate(abs, 0); err != nil {
			return nil, err
		}
	}
	f := &billyFile{c: b.c, name: filename, abs: abs}
	if flag&os.O_APPEND != 0 {
		if at, err := b.c.Stat(abs); err == nil {
			f.off = at.GetSize()
		}
	}
	return f, nil
}

func (b *billyFS) Stat(filename string) (os.FileInfo, error) {
	at, err := b.c.Stat(b.abs(filename))
	if err != nil {
		return nil, err
	}
	return attrToFileInfo(path.Base(filename), at), nil
}

func (b *billyFS) Lstat(filename string) (os.FileInfo, error) { return b.Stat(filename) }

func (b *billyFS) Rename(oldpath, newpath string) error {
	return b.c.Rename(b.abs(oldpath), b.abs(newpath))
}

func (b *billyFS) Remove(filename string) error {
	at, err := b.c.Stat(b.abs(filename))
	if err != nil {
		return err
	}
	if at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR {
		return b.c.Rmdir(b.abs(filename))
	}
	return b.c.Unlink(b.abs(filename))
}

func (b *billyFS) Join(elem ...string) string { return path.Join(elem...) }

func (b *billyFS) ReadDir(p string) ([]os.FileInfo, error) {
	entries, err := b.c.ReadDir(b.abs(p))
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, attrToFileInfo(e.GetName(), e.GetAttr()))
	}
	return out, nil
}

func (b *billyFS) MkdirAll(filename string, perm os.FileMode) error {
	parts := strings.Split(strings.Trim(path.Clean("/"+filename), "/"), "/")
	cur := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		if _, err := b.c.Stat(b.abs(cur)); err == nil {
			continue
		}
		if err := b.c.Mkdir(b.abs(cur), uint32(perm.Perm())); err != nil {
			return err
		}
	}
	return nil
}

func (b *billyFS) TempFile(dir, prefix string) (billy.File, error) {
	name := path.Join(dir, prefix+time.Now().Format("20060102T150405.000000000"))
	return b.Create(name)
}

func (b *billyFS) Symlink(target, link string) error { return billy.ErrNotSupported }

func (b *billyFS) Readlink(link string) (string, error) {
	at, err := b.c.Stat(b.abs(link))
	if err != nil {
		return "", err
	}
	if at.GetType() != agentpbv2.FileType_FILE_TYPE_SYMLINK {
		return "", billy.ErrNotSupported
	}
	return at.GetSymlinkTarget(), nil
}

func (b *billyFS) Chroot(p string) (billy.Filesystem, error) {
	return &billyFS{c: b.c, root: path.Join(b.root, p)}, nil
}

func (b *billyFS) Root() string { return path.Join("/", b.root) }

type billyFile struct {
	c    *FSClient
	name string
	abs  string
	off  int64
}

func (f *billyFile) Name() string { return f.name }

func (f *billyFile) Read(p []byte) (int, error) {
	data, eof, err := f.c.ReadAt(f.abs, f.off, len(p))
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

func (f *billyFile) ReadAt(p []byte, off int64) (int, error) {
	data, eof, err := f.c.ReadAt(f.abs, off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(p) && eof {
		return n, io.EOF
	}
	return n, nil
}

func (f *billyFile) Write(p []byte) (int, error) {
	n, err := f.c.WriteAt(f.abs, f.off, p)
	f.off += int64(n)
	return n, err
}

func (f *billyFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.off = offset
	case io.SeekCurrent:
		f.off += offset
	case io.SeekEnd:
		at, err := f.c.Stat(f.abs)
		if err != nil {
			return 0, err
		}
		f.off = at.GetSize() + offset
	default:
		return 0, os.ErrInvalid
	}
	return f.off, nil
}

func (f *billyFile) Close() error              { return nil }
func (f *billyFile) Lock() error               { return nil }
func (f *billyFile) Unlock() error             { return nil }
func (f *billyFile) Truncate(size int64) error { return f.c.Truncate(f.abs, size) }

// fileInfo adapts an Attr to os.FileInfo.
type fileInfo struct {
	name string
	at   *agentpbv2.Attr
}

func attrToFileInfo(name string, at *agentpbv2.Attr) os.FileInfo { return &fileInfo{name: name, at: at} }

func (i *fileInfo) Name() string { return i.name }
func (i *fileInfo) Size() int64  { return i.at.GetSize() }
func (i *fileInfo) Mode() os.FileMode {
	m := os.FileMode(i.at.GetMode()).Perm()
	switch i.at.GetType() {
	case agentpbv2.FileType_FILE_TYPE_DIR:
		m |= os.ModeDir
	case agentpbv2.FileType_FILE_TYPE_SYMLINK:
		m |= os.ModeSymlink
	}
	return m
}
func (i *fileInfo) ModTime() time.Time { return time.Unix(0, i.at.GetMtimeUnixNano()) }
func (i *fileInfo) IsDir() bool        { return i.at.GetType() == agentpbv2.FileType_FILE_TYPE_DIR }
func (i *fileInfo) Sys() any           { return nil }
