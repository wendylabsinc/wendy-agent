package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/env"
)

// TestInvocationAnalyticsProcessHelper executes main, including its exit-time
// flush, in a child process. Redirect telemetry before main can initialize it,
// and block every other HTTP destination so tests cannot send production events.
func TestInvocationAnalyticsProcessHelper(t *testing.T) {
	if os.Getenv("WENDY_ANALYTICS_PROCESS_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	destination, err := url.Parse(os.Getenv("WENDY_ANALYTICS_PROCESS_RECEIVER"))
	if err != nil || destination.Scheme != "http" || destination.Hostname() != "127.0.0.1" {
		t.Fatal("analytics receiver must use loopback HTTP")
	}
	transport := &http.Transport{}
	http.DefaultTransport = analyticsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "wendy-cli-telemetry-114319063177.us-central1.run.app" || r.URL.Path != "/v1/telemetry/events" {
			return nil, fmt.Errorf("unexpected HTTP request in analytics subprocess")
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = destination.Scheme, destination.Host
		return transport.RoundTrip(copy)
	})
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("WENDY_ANALYTICS_PROCESS_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"wendy"}, args...)
	main()
	t.Fatal("main returned without exiting")
}

func analyticsProcessEnv(configDir, receiver string, args []string) []string {
	ciVars := make(map[string]bool, len(env.CIEnvVars))
	for _, key := range env.CIEnvVars {
		ciVars[strings.ToUpper(key)] = true
	}
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if ciVars[key] || strings.HasPrefix(key, "WENDY_") || strings.HasPrefix(key, "HOMEBREW_") || strings.HasPrefix(key, "SUDO_") {
			continue
		}
		result = append(result, entry)
	}
	encoded, _ := json.Marshal(args)
	return append(result,
		"WENDY_ANALYTICS_PROCESS_HELPER=1",
		"WENDY_ANALYTICS_PROCESS_RECEIVER="+receiver,
		"WENDY_ANALYTICS_PROCESS_ARGS="+string(encoded),
		"WENDY_CONFIG_DIR="+configDir,
		"WENDY_ANALYTICS=true",
		"WENDY_NO_UPDATE_CHECK=1",
		"WENDY_SECRET_STORE=file")
}

// Exercise fresh processes rather than calling executeInvocation directly:
// main must initialize before early exits and flush before os.Exit on every OS.
func TestInvocationAnalyticsProcessDelivery(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command string
		args          []string
		exit          int
	}{
		{"bare invocation", "wendy", []string{}, 0},
		{"help flag", "wendy install", []string{"install", "--help"}, 0},
		{"version", "wendy", []string{"--version"}, 0},
		{"verbose version", "wendy", []string{"--version", "--verbose"}, 0},
		{"JSON version", "wendy", []string{"--version", "--json"}, 0},
		{"unknown flag", "wendy install", []string{"install", "--unknown-test-flag"}, 2},
		{"normal command", "wendy analytics status", []string{"analytics", "status"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type processEvent struct {
				receivedAnalytics
				OS   string `json:"os"`
				Arch string `json:"arch"`
			}
			var mu sync.Mutex
			var events []processEvent
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event processEvent
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestInvocationAnalyticsProcessHelper$")
			cmd.Env = analyticsProcessEnv(t.TempDir(), srv.URL, tc.args)
			// Pipe stdin prevents Windows from pausing for a sole console owner.
			cmd.Stdin = strings.NewReader("")
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("CLI subprocess did not finish: %v; output = %s", ctx.Err(), output)
			}
			exit := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					exit = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if exit != tc.exit {
				t.Fatalf("exit = %d, want %d; output = %s", exit, tc.exit, output)
			}
			mu.Lock()
			defer mu.Unlock()
			seen, finished := 0, 0
			for _, event := range events {
				if event.OS != runtime.GOOS || event.Arch != runtime.GOARCH {
					t.Errorf("event %s platform = %s/%s, want %s/%s", event.Event, event.OS, event.Arch, runtime.GOOS, runtime.GOARCH)
				}
				switch event.Event {
				case "cli_seen":
					seen++
					if event.CommandName != tc.command || !event.Success {
						t.Errorf("unexpected launch: %+v", event)
					}
				case "command_executed":
					finished++
					if event.CommandName != tc.command || event.Success != (tc.exit == 0) {
						t.Errorf("unexpected completion: %+v", event)
					}
				}
			}
			if seen != 1 || finished != 1 {
				t.Fatalf("received %d launches and %d completions, want one each; events = %+v", seen, finished, events)
			}
		})
	}
}
