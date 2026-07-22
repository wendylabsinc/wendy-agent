//go:build unix

package hostexec

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellFromPasswd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "passwd")
	content := "daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n" +
		"root:x:0:0:root:/root:/bin/bash\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := shellFromPasswd(p, "root"); got != "/bin/bash" {
		t.Fatalf("shellFromPasswd = %q; want /bin/bash", got)
	}
	if got := shellFromPasswd(p, "missing"); got != "" {
		t.Fatalf("shellFromPasswd(missing) = %q; want empty", got)
	}
}

func TestRun_EchoesAndReturnsExit(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	var out strings.Builder
	resize := make(chan [2]uint32, 1)
	resize <- [2]uint32{24, 80}

	done := make(chan int, 1)
	go func() {
		code, err := New().Run(context.Background(),
			[]string{"/bin/sh", "-c", "printf hello; exit 3"},
			stdinR, &out, resize)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- code
	}()

	// The command ignores stdin and exits on its own; close stdin so the
	// spawner's stdin copy goroutine also unwinds, then close resize.
	_ = stdinW.Close()
	close(resize)

	select {
	case code := <-done:
		if code != 3 {
			t.Fatalf("exit code = %d; want 3", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return in time")
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("stdout = %q; want to contain hello", out.String())
	}
}
