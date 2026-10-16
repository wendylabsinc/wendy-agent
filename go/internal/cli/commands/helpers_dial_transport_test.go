package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestDialAddrDoesNotRetryCertificatesAfterTCPFailure(t *testing.T) {
	setTempConfig(t, &config.Config{})
	setPinCache(t)
	addr := deadAgentAddr(t)
	walk := &mtlsWalk{target: dialTarget{Addr: addr}}
	for i := 0; i < 7; i++ {
		walk.allCerts = append(walk.allCerts, selfSignedCLICert(t, i+1))
		walk.probeOrder = append(walk.probeOrder, i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := walk.dialAddr(ctx, addr, true)
	if conn != nil {
		conn.Close()
		t.Fatal("closed ports returned a connection")
	}
	if err != nil {
		t.Fatalf("transport failure should allow other addresses: %v", err)
	}
	if len(walk.attempts) != 2 {
		t.Fatalf("got %d attempts for seven certificates, want one per port", len(walk.attempts))
	}
	if walk.primaryRejectedOurCert() || walk.anyCertRejection {
		t.Fatal("a TCP failure was reported as a certificate rejection")
	}
	if walk.primaryMTLSPortNonCertFails != 1 {
		t.Fatalf("primary mTLS port failures = %d, want 1", walk.primaryMTLSPortNonCertFails)
	}
}
