package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
)

func TestVMRegistryRoutesEveryBuilderToSelectedVM(t *testing.T) {
	stubVMStatuses(t, runningVM("one", vm.NetUser, 50051), runningVM("two", vm.NetUser, 50053))
	old := vmRegistryPort
	t.Cleanup(func() { vmRegistryPort = old })
	ports := map[string]int{}
	for _, name := range []string{"one", "two"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, name) }))
		t.Cleanup(srv.Close)
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
		ports[name], _ = strconv.Atoi(port)
	}
	vmRegistryPort = func(_ context.Context, name string) (int, error) { return ports[name], nil }
	for _, builder := range []string{"swift", "docker", "apple"} {
		for i, name := range []string{"one", "two"} {
			conn := &grpcclient.AgentConnection{Host: "127.0.0.1", Addr: fmt.Sprintf("127.0.0.1:%d", 50051+2*i)}
			var addr string
			var done func()
			var err error
			switch builder {
			case "swift":
				addr, _, done, _, err = resolveRegistryForSwiftAgent(context.Background(), conn, 5000)
			case "docker":
				addr, done, _, err = resolveRegistryForAgent(context.Background(), conn, 5000)
			case "apple":
				addr, done, _, _, err = resolveRegistryForAppleContainer(context.Background(), conn, 5000)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Get("http://127.0.0.1:" + port + "/v2/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			done()
			conn.Close()
			if err != nil || string(body) != name {
				t.Fatalf("%s for %s reached %q: %v", builder, name, body, err)
			}
		}
	}
	vmRegistryPort = func(context.Context, string) (int, error) { return 0, fmt.Errorf("monitor unavailable") }
	conn := &grpcclient.AgentConnection{Host: "127.0.0.1", Addr: "127.0.0.1:50053"}
	if _, _, _, _, err := resolveRegistryForSwiftAgent(context.Background(), conn, 5000); err == nil {
		t.Fatal("fell back to another VM's registry")
	}
}
