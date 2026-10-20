//go:build linux

package meshingress

import (
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestAdmittedEndpointSurvivesDNATLoss(t *testing.T) {
	if os.Getenv("WENDY_MESH_NAMESPACE_TEST") != "1" {
		t.Skip("requires disposable privileged network namespace")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestAdmittedEndpointProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "WENDY_INGRESS_ENDPOINT_PROCESS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("endpoint namespace: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestAdmittedEndpointProcess(t *testing.T) {
	if os.Getenv("WENDY_INGRESS_ENDPOINT_PROCESS") != "1" {
		t.Skip("namespace subprocess")
	}
	run := func(binary string, args ...string) {
		t.Helper()
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", binary, args, err, out)
		}
	}
	run("ip", "link", "set", "lo", "up")
	run("ip", "address", "add", "10.201.150.2/32", "dev", "lo")
	var hostDials atomic.Int32
	serve := func(address, reply string) net.Listener {
		t.Helper()
		l, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				if reply == "host" {
					hostDials.Add(1)
				}
				_, _ = io.WriteString(c, reply)
				c.Close()
			}
		}()
		return l
	}
	app := serve("10.201.150.2:0", "app")
	host := serve("127.0.0.1:0", "host")
	hostPort := uint16(host.Addr().(*net.TCPAddr).Port)
	appPort := uint16(app.Addr().(*net.TCPAddr).Port)
	rule := []string{"-d", "127.0.0.1/32", "-p", "tcp", "--dport", strconv.Itoa(int(hostPort)), "-j", "DNAT", "--to-destination", app.Addr().String()}
	run("iptables", append([]string{"-t", "nat", "-A", "OUTPUT"}, rule...)...)
	r := NewRegistry()
	if err := r.Claim("running-app", hostPort, "10.201.150.2", appPort); err != nil {
		t.Fatal(err)
	}
	dial := func() (net.Conn, error) {
		return r.DialAuthorized(hostPort, func(address string) (net.Conn, error) { return net.DialTimeout("tcp4", address, time.Second) })
	}
	for _, remove := range []bool{false, true} {
		if remove {
			run("iptables", append([]string{"-t", "nat", "-D", "OUTPUT"}, rule...)...)
		}
		c, err := dial()
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		body, err := io.ReadAll(c)
		c.Close()
		if err != nil || string(body) != "app" {
			t.Fatalf("removed=%v: got %q %v", remove, body, err)
		}
	}
	app.Close()
	if c, err := dial(); err == nil {
		c.Close()
		t.Fatal("missing container endpoint fell back to host listener")
	}
	if hostDials.Load() != 0 {
		t.Fatalf("unrelated host listener received %d dials", hostDials.Load())
	}
	t.Log("live app reached with and without DNAT; unavailable app failed closed while unrelated host listener stayed live")
}
