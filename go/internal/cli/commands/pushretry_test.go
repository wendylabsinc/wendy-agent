package commands

import "testing"

func TestIsTransientPushError(t *testing.T) {
	transient := []string{
		`Head "https://host.docker.internal:50342/v2/x/blobs/sha256:ab": net/http: TLS handshake timeout`,
		`ERROR: failed to solve: failed to push host.docker.internal:50342/app:latest: failed to do request`,
		`error: read tcp 127.0.0.1->...: connection reset by peer`,
		`dial tcp: i/o timeout`,
		`unexpected EOF`,
		`error: write tcp 127.0.0.1:50342->127.0.0.1:64880: write: broken pipe ; retrying in 1s`,
		`write: connection timed out`,
		`received unexpected HTTP status: 503 Service Unavailable`,
		`429 Too Many Requests`,
	}
	for _, s := range transient {
		if !isTransientPushError(s) {
			t.Errorf("expected transient for %q", s)
		}
	}

	notTransient := []string{
		``,
		`ERROR: failed to solve: process "/bin/sh -c go build" did not complete successfully: exit code: 1`,
		`Dockerfile:5: COPY failed: file not found`,
		`undefined: someSymbol`,
	}
	for _, s := range notTransient {
		if isTransientPushError(s) {
			t.Errorf("did not expect transient for %q", s)
		}
	}
}

func TestMultiBuildConcurrency(t *testing.T) {
	tests := []struct {
		numServices int
		want        int
	}{
		{0, 1},  // floor of 1
		{1, 1},  // capped to numServices
		{3, 3},  // small group, below default cap
		{4, 4},  // at default cap
		{7, 4},  // still default cap just under large threshold
		{8, 2},  // large group -> throttled
		{14, 2}, // the go2 template -> throttled
	}
	for _, tt := range tests {
		if got := multiBuildConcurrency(tt.numServices); got != tt.want {
			t.Errorf("multiBuildConcurrency(%d) = %d, want %d", tt.numServices, got, tt.want)
		}
	}
}

func TestResolveBuildConcurrency(t *testing.T) {
	tests := []struct {
		buildCount int
		override   int
		want       int
	}{
		// override = 0 -> auto heuristic (multiBuildConcurrency)
		{0, 0, 1},  // nothing to build -> floor 1
		{4, 0, 4},  // small group, default cap
		{14, 0, 2}, // large group -> auto-throttled
		// override > 0 -> use it, clamped to [1, buildCount]
		{14, 1, 1}, // explicit serialization
		{14, 6, 6}, // explicit higher than auto
		{3, 10, 3}, // override capped to buildCount
		{5, 0, 4},  // 5 services, below large threshold -> default cap 4
	}
	for _, tt := range tests {
		if got := resolveBuildConcurrency(tt.buildCount, tt.override); got != tt.want {
			t.Errorf("resolveBuildConcurrency(%d, %d) = %d, want %d", tt.buildCount, tt.override, got, tt.want)
		}
	}
}
