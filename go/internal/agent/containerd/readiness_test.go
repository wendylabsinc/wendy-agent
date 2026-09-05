package containerd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestHTTPReadinessStatusAndNoRedirect(t *testing.T) {
	for _, code := range []int{200, 204, 302, 399, 400, 503} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != "/health?ready=1" {
					t.Errorf("path=%s", r.URL.RequestURI())
				}
				w.Header().Set("Location", "http://must-not-follow.invalid/")
				w.WriteHeader(code)
			}))
			defer server.Close()
			_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			err := checkNetworkReadiness(context.Background(), &appconfig.ReadinessConfig{HTTPGet: &appconfig.HTTPGetProbe{Port: port, Path: "/health?ready=1"}}, (&net.Dialer{}).DialContext)
			if (err == nil) != (code >= 200 && code < 400) {
				t.Fatalf("code=%d err=%v", code, err)
			}
		})
	}
}

func TestNetworkReadinessDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := checkNetworkReadiness(ctx, &appconfig.ReadinessConfig{HTTPGet: &appconfig.HTTPGetProbe{Port: port}}, (&net.Dialer{}).DialContext)
	if err == nil {
		t.Fatal("hanging HTTP probe passed")
	}
}

func TestTCPReadinessUsesLoopback(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	var address string
	err = checkNetworkReadiness(context.Background(), &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}}, func(ctx context.Context, network, addr string) (net.Conn, error) {
		address = addr
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	if err != nil || address != "127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("address=%q err=%v", address, err)
	}
}
