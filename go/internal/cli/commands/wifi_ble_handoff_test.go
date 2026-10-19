package commands

import (
	"context"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestBLEWiFiHandoffRequiresVerifiedIdentityOnEveryCandidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tls      bool
		observed string
		wantOK   bool
	}{
		{"same device", true, "42", true}, {"wrong device", true, "99", false}, {"plaintext", false, "42", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStream, oldDial, oldIdentity := wifiHandoffStreamFn, wifiHandoffDialFn, wifiHandoffIdentityFn
			t.Cleanup(func() {
				wifiHandoffStreamFn, wifiHandoffDialFn, wifiHandoffIdentityFn = oldStream, oldDial, oldIdentity
			})
			expected := certs.WendyIdentity{OrgID: 7, EntityType: certs.EntityAsset, EntityID: "42"}
			wifiHandoffStreamFn = func(context.Context, discovery.StreamOptions) <-chan discovery.LANEvent {
				ch := make(chan discovery.LANEvent, 1)
				ch <- discovery.LANEvent{Kind: discovery.LANFound, Device: models.LANDevice{Hostname: "board.local", IPAddress: "192.0.2.42", Port: 50052, IsMTLS: true}}
				return ch
			}
			wifiHandoffDialFn = func(_ context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
				if target.Expected == nil || !target.Expected.SameEntity(expected) {
					t.Fatal("dial lost authenticated BLE identity")
				}
				if !target.pinned() {
					t.Fatal("handoff allowed plaintext fallback")
				}
				return &grpcclient.AgentConnection{IsMTLS: tc.tls}, nil, nil
			}
			wifiHandoffIdentityFn = func(*grpcclient.AgentConnection) (certs.WendyIdentity, bool) {
				observed := expected
				observed.EntityID = tc.observed
				return observed, true
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := waitForBLEAgentLAN(ctx, &models.BluetoothDevice{Hostname: "board", Identity: "urn:wendy:org:7:asset:99"}, expected)
			if (err == nil) != tc.wantOK {
				t.Fatalf("conn=%v err=%v", conn, err)
			}
		})
	}
}
