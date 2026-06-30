package mount

import (
	"context"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/webdav"
)

// ServeWebdav starts a WebDAV HTTP server bound to loopback, backed by fs.
// It returns the listening address (host:port), a stop function, and any error.
// Cancelling ctx also closes the server.
func ServeWebdav(ctx context.Context, fs webdav.FileSystem) (string, func() error, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	handler := &webdav.Handler{
		FileSystem: fs,
		LockSystem: webdav.NewMemLS(),
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(lis) }()

	var once sync.Once
	done := make(chan struct{})
	closeServer := func() error {
		var err error
		once.Do(func() {
			close(done)
			err = srv.Close()
		})
		return err
	}
	go func() {
		select {
		case <-ctx.Done():
			closeServer()
		case <-done:
		}
	}()
	return lis.Addr().String(), closeServer, nil
}
