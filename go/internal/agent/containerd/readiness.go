package containerd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

var _ services.ContainerReadinessProber = (*Client)(nil)

func (c *Client) ProbeReadiness(ctx context.Context, name string, probe *appconfig.ReadinessConfig) error {
	ctx = c.withNamespace(ctx)
	ctr, err := c.runningContainerForApp(ctx, name)
	if err != nil {
		return err
	}
	task, err := ctr.Task(ctx, nil)
	if err != nil {
		return fmt.Errorf("application process is not running: %w", err)
	}
	if err := readinessRunning(ctx, task); err != nil {
		return err
	}
	if probe == nil {
		return nil
	}
	if probe.Exec != nil {
		if err := c.execReadiness(ctx, ctr, task, probe.Exec); err != nil {
			return err
		}
	} else {
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialReadinessNamespace(ctx, task.Pid(), network, address)
		}
		if err := checkNetworkReadiness(ctx, probe, dial); err != nil {
			return err
		}
	}
	// A successful socket/HTTP/exec check must not hide a process that exited
	// while the probe was running.
	return readinessRunning(ctx, task)
}

func readinessRunning(ctx context.Context, task containerd.Task) error {
	state, err := task.Status(ctx)
	if err != nil {
		return fmt.Errorf("reading application process state: %w", err)
	}
	if state.Status != containerd.Running {
		return fmt.Errorf("application process is %s (exit code %d)", state.Status, state.ExitStatus)
	}
	return nil
}

func checkNetworkReadiness(ctx context.Context, probe *appconfig.ReadinessConfig, dial func(context.Context, string, string) (net.Conn, error)) error {
	if tcp := probe.TCPSocket; tcp != nil {
		conn, err := dial(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port)))
		if err != nil {
			return fmt.Errorf("TCP readiness on port %d: %w", tcp.Port, err)
		}
		return conn.Close()
	}
	if get := probe.HTTPGet; get != nil {
		transport := &http.Transport{DialContext: dial, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		path := get.Path
		if path == "" {
			path = "/"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(get.Port)+path, nil)
		if err != nil {
			return fmt.Errorf("HTTP readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("HTTP readiness: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP readiness returned %s", resp.Status)
		}
		return nil
	}
	return fmt.Errorf("readiness has no probe")
}

// Each exec attempt has independent bounded cleanup. In particular, cancellation
// must not send Kill with an already-cancelled context and then wait forever.
func (c *Client) execReadiness(ctx context.Context, ctr containerd.Container, task containerd.Task, command []string) error {
	spec, err := ctr.Spec(ctx)
	if err != nil || spec.Process == nil {
		return fmt.Errorf("reading readiness process spec: %v", err)
	}
	process := *spec.Process
	process.Args = append([]string(nil), command...)
	process.Env = append([]string(nil), process.Env...)
	process.Terminal = false
	id := fmt.Sprintf("readiness-%d-%d", time.Now().UnixNano(), execCounter.Add(1))
	proc, err := task.Exec(ctx, id, &process, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
	if err != nil {
		return fmt.Errorf("creating readiness exec: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = proc.Delete(cleanupCtx, containerd.WithProcessKill)
	}()
	statusCh, err := proc.Wait(ctx)
	if err != nil {
		return fmt.Errorf("waiting for readiness exec: %w", err)
	}
	if err := proc.Start(ctx); err != nil {
		return fmt.Errorf("starting readiness exec: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case exit, ok := <-statusCh:
		if !ok {
			return fmt.Errorf("readiness exec ended without an exit status")
		}
		if err := exit.Error(); err != nil {
			return fmt.Errorf("readiness exec: %w", err)
		}
		if exit.ExitCode() != 0 {
			return fmt.Errorf("readiness exec exited with code %d", exit.ExitCode())
		}
		return nil
	}
}
