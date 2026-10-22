package meshcatalog

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

func TestOperatorLinkDoesNotRequireDeviceCatalogue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		c, _ := f.newCatalog(t, p533, "default", nil, nil)
		r, _ := NewRuntime(c, func() localmesh.NodeSnapshot { return localmesh.NodeSnapshot{} })
		operator, err := meshid.PeerFromPrincipal("spiffe://wendy.sh/tenant/" + c.device.Tenant().String() + "/operator/alice")
		if err != nil {
			t.Fatal(err)
		}
		hub := localmesh.NewControlHub()
		sent, closed := false, false
		hub.Add(localmesh.ControlPeer{ID: 1, Device: operator}, func(context.Context, []byte) error { sent = true; return nil }, func() { closed = true })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(ctx, hub) }()
		time.Sleep(90 * time.Second)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if sent || closed {
			t.Fatalf("operator link required catalogue: sent=%v closed=%v", sent, closed)
		}
		if len(hub.Peers()) != 1 {
			t.Fatal("operator routing peer removed")
		}
	})
}
