package discovery

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestLANDeviceFromService(t *testing.T) {
	svc := MDNSService{
		InstanceName:  "orin",
		Hostname:      "orin.local",
		IPAddress:     "10.0.0.5",
		Port:          50051,
		InterfaceName: "en0",
		TXTRecords: map[string]string{
			"displayname":   "Orin Nano",
			"wendyosdevice": "dev-1",
			"tls":           "true",
			"assetid":       "3",
			"orgid":         "7",
			"name":          "brave-dolphin",
		},
	}
	dev := lanDeviceFromService(svc)
	if dev.ID != "dev-1" || dev.DisplayName != "Orin Nano" || !dev.IsMTLS ||
		dev.AssetID != 3 || dev.OrgID != 7 || dev.MeshName != "brave-dolphin" ||
		dev.Hostname != "orin.local" || dev.IPAddress != "10.0.0.5" || dev.Port != 50051 ||
		!dev.IsWendyDevice || dev.InterfaceType != string(models.InterfaceLAN) {
		t.Fatalf("mapper: %+v", dev)
	}

	// fallbacks: no TXT id → "id" key → display name; no displayname → hostname sans .local
	bare := lanDeviceFromService(MDNSService{InstanceName: "x", Hostname: "orin.local", Port: 50051, TXTRecords: map[string]string{}})
	if bare.DisplayName != "orin" || bare.ID != "orin" {
		t.Fatalf("fallbacks: %+v", bare)
	}
	// assetid/orgid: 0 or unparseable stays 0
	z := lanDeviceFromService(MDNSService{Hostname: "a.local", Port: 1, TXTRecords: map[string]string{"assetid": "0", "orgid": "junk"}})
	if z.AssetID != 0 || z.OrgID != 0 {
		t.Fatalf("zero/invalid ids must stay 0: %+v", z)
	}
}
