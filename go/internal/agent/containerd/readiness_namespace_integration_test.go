//go:build linux

package containerd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// Runs only in the explicitly enabled privileged deployment fixture. The child
// test process owns a genuinely separate network namespace, just as a task in
// an isolated container does; no shell, unshare utility, or image is required.
func TestReadinessNetworkNamespaceIntegration(t *testing.T) {
	if os.Getenv("WENDY_READINESS_NAMESPACE_CHILD") == "1" {
		readinessNamespaceChild(t)
		return
	}
	if os.Getenv("WENDY_DEPLOYMENT_TEST_SOCKET") == "" {
		t.Skip("set WENDY_DEPLOYMENT_TEST_SOCKET in the privileged deployment fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadinessNetworkNamespaceIntegration$")
	cmd.Env = append(os.Environ(), "WENDY_READINESS_NAMESPACE_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("creating isolated network namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("namespace child: %v: %s", err, stderr.String())
		}
	})
	portLine := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); portLine <- line }()
	var port int
	select {
	case line := <-portLine:
		port, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil || port < 1 || port > 65535 {
			t.Fatalf("namespace child did not report listener port: %q", line)
		}
	case <-ctx.Done():
		t.Fatal("namespace child did not become ready")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if hostConn, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "tcp4", address); err == nil {
		_ = hostConn.Close()
		t.Fatal("isolated listener is unexpectedly reachable on the agent's host loopback")
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialReadinessNamespace(ctx, uint32(cmd.Process.Pid), network, address)
	}
	for name, probe := range map[string]*appconfig.ReadinessConfig{
		"tcp":  {TCPSocket: &appconfig.TCPSocketProbe{Port: port}},
		"http": {HTTPGet: &appconfig.HTTPGetProbe{Port: port, Path: "/ready"}},
	} {
		t.Run(name, func(t *testing.T) {
			probeCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			if err := checkNetworkReadiness(probeCtx, probe, dial); err != nil {
				t.Fatalf("probe did not reach isolated task namespace: %v", err)
			}
		})
	}
	t.Log("TCP and HTTP readiness reached the isolated task loopback; agent host loopback could not")
}

func readinessNamespaceChild(t *testing.T) {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), ReadHeaderTimeout: time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	fmt.Fprintln(os.Stdout, listener.Addr().(*net.TCPAddr).Port)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
