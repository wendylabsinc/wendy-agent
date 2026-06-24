package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func TestCloudDeviceInfoIncludesID(t *testing.T) {
	a := &cloudpb.Asset{Id: 77}
	info := cloudDeviceInfoFromAsset(a, nil)
	if info.ID != 77 {
		t.Fatalf("expected ID 77, got %d", info.ID)
	}
}
