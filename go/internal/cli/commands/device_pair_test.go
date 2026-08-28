package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestSensorSourceItemsFiltersToCapable(t *testing.T) {
	devs := []models.DiscoveredDevice{
		{DisplayName: "hub", Sensorlink: true, AssetID: 5, OrgID: 3},
		{DisplayName: "jetson", Sensorlink: false, AssetID: 6, OrgID: 3},
	}
	items := sensorSourceItems(devs)
	if len(items) != 1 || items[0].Name != "hub" {
		t.Fatalf("expected only the sensorlink device, got %+v", items)
	}
}

func TestSameOrgRejectsMismatch(t *testing.T) {
	if err := sameOrg(3, 3); err != nil {
		t.Fatalf("same org should pass: %v", err)
	}
	if err := sameOrg(3, 9); err == nil {
		t.Fatal("cross-org should fail")
	}
}
