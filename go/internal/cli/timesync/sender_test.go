package clitimesync

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

func TestFetchProofPacketMemoizes(t *testing.T) {
	calls := 0
	orig := roughtimeQueryFn
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		calls++
		return roughtime.Result{Server: "test", Nonce: []byte("nonce"), RawResponse: []byte("resp")}, nil
	}
	t.Cleanup(func() { roughtimeQueryFn = orig; resetProofCache() })
	resetProofCache()

	pkt1, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket: %v", err)
	}
	pkt2, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket (2): %v", err)
	}
	if calls != 1 {
		t.Fatalf("roughtime query called %d times, want 1 (memoized)", calls)
	}
	if len(pkt1) == 0 || string(pkt1) != string(pkt2) {
		t.Fatal("expected identical non-empty packets across calls")
	}
}
