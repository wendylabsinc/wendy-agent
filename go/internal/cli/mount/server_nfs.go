package mount

import (
	"context"
	"net"
	"sync"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

// ServeNFS starts a userspace NFSv3 server bound to loopback, backed by fs.
// It returns the listening address and a stop function that closes the listener.
// Cancelling ctx also closes the listener.
func ServeNFS(ctx context.Context, fs billy.Filesystem) (string, func() error, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	handler := nfshelper.NewNullAuthHandler(fs)
	cacheHandler := nfshelper.NewCachingHandler(handler, 1024)

	go func() { _ = nfs.Serve(lis, cacheHandler) }()

	var once sync.Once
	done := make(chan struct{})
	stop := func() error {
		var err error
		once.Do(func() {
			close(done)
			err = lis.Close()
		})
		return err
	}
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	return lis.Addr().String(), stop, nil
}
