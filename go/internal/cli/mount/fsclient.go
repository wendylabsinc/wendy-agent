package mount

import (
	"context"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// FSClient is a thin, path-oriented wrapper over WendyVolumeFsService scoped to
// a single volume. It is the shared backend for the NFS (billy) and WebDAV
// frontends.
type FSClient struct {
	ctx    context.Context
	cl     agentpbv2.WendyVolumeFsServiceClient
	volume string
}

func NewFSClient(ctx context.Context, cl agentpbv2.WendyVolumeFsServiceClient, volume string) *FSClient {
	return &FSClient{ctx: ctx, cl: cl, volume: volume}
}

// Stat returns attributes for path within the volume.
func (c *FSClient) Stat(path string) (*agentpbv2.Attr, error) {
	r, err := c.cl.Stat(c.ctx, &agentpbv2.StatRequest{Volume: c.volume, Path: path})
	if err != nil {
		return nil, err
	}
	return r.GetAttr(), nil
}

// ReadDir lists the directory entries at path within the volume.
func (c *FSClient) ReadDir(path string) ([]*agentpbv2.DirEntry, error) {
	r, err := c.cl.ReadDir(c.ctx, &agentpbv2.ReadDirRequest{Volume: c.volume, Path: path})
	if err != nil {
		return nil, err
	}
	return r.GetEntries(), nil
}

// ReadAt reads up to n bytes from path at offset off; eof is true when the end of file is reached.
func (c *FSClient) ReadAt(path string, off int64, n int) ([]byte, bool, error) {
	r, err := c.cl.Read(c.ctx, &agentpbv2.ReadRequest{Volume: c.volume, Path: path, Offset: off, Length: int32(n)})
	if err != nil {
		return nil, false, err
	}
	return r.GetData(), r.GetEof(), nil
}

// WriteAt writes data to path at offset off, returning the number of bytes written.
func (c *FSClient) WriteAt(path string, off int64, data []byte) (int, error) {
	r, err := c.cl.Write(c.ctx, &agentpbv2.WriteRequest{Volume: c.volume, Path: path, Offset: off, Data: data})
	if err != nil {
		return 0, err
	}
	return int(r.GetWritten()), nil
}

// Create creates a new file at path with the given mode and returns its attributes.
func (c *FSClient) Create(path string, mode uint32) (*agentpbv2.Attr, error) {
	r, err := c.cl.Create(c.ctx, &agentpbv2.CreateRequest{Volume: c.volume, Path: path, Mode: mode})
	if err != nil {
		return nil, err
	}
	return r.GetAttr(), nil
}

// Mkdir creates a directory at path with the given mode.
func (c *FSClient) Mkdir(path string, mode uint32) error {
	_, err := c.cl.Mkdir(c.ctx, &agentpbv2.MkdirRequest{Volume: c.volume, Path: path, Mode: mode})
	return err
}

// Unlink removes the file at path.
func (c *FSClient) Unlink(path string) error {
	_, err := c.cl.Unlink(c.ctx, &agentpbv2.UnlinkRequest{Volume: c.volume, Path: path})
	return err
}

// Rmdir removes the directory at path.
func (c *FSClient) Rmdir(path string) error {
	_, err := c.cl.Rmdir(c.ctx, &agentpbv2.RmdirRequest{Volume: c.volume, Path: path})
	return err
}

// Rename moves a file or directory from from to to within the volume.
func (c *FSClient) Rename(from, to string) error {
	_, err := c.cl.Rename(c.ctx, &agentpbv2.RenameRequest{Volume: c.volume, From: from, To: to})
	return err
}

// Truncate sets the size of the file at path to size bytes.
func (c *FSClient) Truncate(path string, size int64) error {
	_, err := c.cl.SetAttr(c.ctx, &agentpbv2.SetAttrRequest{Volume: c.volume, Path: path, Size: &size})
	return err
}

// Chmod sets the permission bits of the file at path.
func (c *FSClient) Chmod(path string, mode uint32) error {
	_, err := c.cl.SetAttr(c.ctx, &agentpbv2.SetAttrRequest{Volume: c.volume, Path: path, Mode: &mode})
	return err
}

// StatFs returns total and free byte counts for the volume's backing filesystem.
func (c *FSClient) StatFs() (total, free uint64, err error) {
	r, err := c.cl.StatFs(c.ctx, &agentpbv2.StatFsRequest{Volume: c.volume})
	if err != nil {
		return 0, 0, err
	}
	return r.GetTotalBytes(), r.GetFreeBytes(), nil
}
