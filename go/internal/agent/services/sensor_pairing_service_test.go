package services_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/mcusource"
	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
)

func TestAddAndListSensorPairing(t *testing.T) {
	store := mcusource.NewPairingStore(filepath.Join(t.TempDir(), "p.json"))
	_ = store.Load()
	started := map[int32]string{}
	const agentOrgID = int32(42)
	svc := services.NewSensorPairingService(zap.NewNop(), store, func() int32 { return agentOrgID }, func(p mcusource.SensorPairing, addr string) { started[p.SourceAssetID] = addr })

	addResp, err := svc.AddSensorPairing(context.Background(), &agentpbv2.AddSensorPairingRequest{SourceAssetId: 7, SourceAddress: "1.2.3.4:7000", Name: "hub"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if addResp.Pairing.OrgId != agentOrgID {
		t.Fatalf("expected returned pairing to carry the agent's org id %d, got %d", agentOrgID, addResp.Pairing.OrgId)
	}
	if started[7] != "1.2.3.4:7000" {
		t.Fatalf("supervisor not started: %v", started)
	}
	resp, err := svc.ListSensorPairings(context.Background(), &agentpbv2.ListSensorPairingsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Pairings) != 1 || resp.Pairings[0].SourceAssetId != 7 {
		t.Fatalf("bad list: %+v", resp.Pairings)
	}
	if resp.Pairings[0].OrgId != agentOrgID {
		t.Fatalf("expected stored pairing to carry the agent's org id %d, got %d", agentOrgID, resp.Pairings[0].OrgId)
	}
}
