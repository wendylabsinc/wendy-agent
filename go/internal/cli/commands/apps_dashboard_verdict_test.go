package commands

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUserFacingGRPCErrorMapsTunnelVerdict(t *testing.T) {
	err := status.Error(codes.Unavailable, `connection error: desc = "transport: authentication handshake failed: cloud tunnel closed by broker: rpc error: code = PermissionDenied desc = user is not a current member of this organization"`)
	got := userFacingGRPCError(err)
	if got != "Poll error: you don't have permission to do this. Check your access and try again." {
		t.Fatalf("poll error %q hides the broker's verdict", got)
	}
	if strings.Contains(got, "authentication handshake failed") {
		t.Fatalf("poll error %q still carries the transport envelope", got)
	}
}

// Every poll path shows the same friendly text: a broker-closed tunnel must
// not read as a raw transport error on one line and as a verdict on another.
func TestAppsDashboardModel_PollErrorsShowTunnelVerdict(t *testing.T) {
	tunnelErr := status.Error(codes.Unavailable, `connection error: desc = "transport: authentication handshake failed: cloud tunnel closed by broker: rpc error: code = PermissionDenied desc = user is not a current member of this organization"`)
	for name, msg := range map[string]interface{}{
		"containers": appsDashContainersMsg{err: tunnelErr},
		"stats":      appsDashStatsMsg{err: tunnelErr},
		"volumes":    appsDashVolumesMsg{err: tunnelErr},
	} {
		t.Run(name, func(t *testing.T) {
			m := newAppsDashboardModel(nil, context.Background())
			updated, _ := m.Update(msg)
			flash := updated.(appsDashboardModel).flash
			if !strings.Contains(flash, "Poll error: you don't have permission to do this. Check your access and try again.") {
				t.Fatalf("flash = %q, want the broker's verdict", flash)
			}
			if strings.Contains(flash, "authentication handshake failed") {
				t.Fatalf("flash = %q still carries the transport envelope", flash)
			}
		})
	}
}
