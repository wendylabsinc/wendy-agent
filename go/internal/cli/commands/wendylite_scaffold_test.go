package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// fakeDeviceProvider is a minimal providers.DeviceProvider test double whose
// Key() is configurable; every other method is a no-op stub.
type fakeDeviceProvider struct{ key string }

func (f fakeDeviceProvider) Key() string         { return f.key }
func (f fakeDeviceProvider) DisplayName() string { return "" }
func (f fakeDeviceProvider) IsAvailable(ctx context.Context) bool         { return true }
func (f fakeDeviceProvider) CheckRequirements(ctx context.Context) error  { return nil }
func (f fakeDeviceProvider) DiscoverDevices(ctx context.Context) ([]models.ExternalDevice, error) {
	return nil, nil
}
func (f fakeDeviceProvider) SupportedBuildTypes() []string  { return nil }
func (f fakeDeviceProvider) CanBuild(projectPath string) bool { return false }
func (f fakeDeviceProvider) Build(ctx context.Context, device models.ExternalDevice, projectPath, projectType, product string, debug bool) (*providers.BuiltApp, error) {
	return nil, nil
}
func (f fakeDeviceProvider) Run(ctx context.Context, app *providers.BuiltApp, detach bool, output chan<- providers.RunOutput) error {
	return nil
}
func (f fakeDeviceProvider) Stop(ctx context.Context, app *providers.BuiltApp) error { return nil }
func (f fakeDeviceProvider) GetDeviceInfo(ctx context.Context, device models.ExternalDevice) (*providers.ProviderDeviceInfo, error) {
	return nil, nil
}

func TestShouldOfferWendyLiteESPIDFScaffold(t *testing.T) {
	usbWendyLite := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "USB"}},
		Provider: fakeDeviceProvider{key: "wendy-lite"},
	}
	lanWendyLite := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "LAN"}},
		Provider: fakeDeviceProvider{key: "wendy-lite"},
	}
	usbOtherProvider := &SelectedDevice{
		External: &models.ExternalDevice{ConnectionInfo: map[string]string{"type": "USB"}},
		Provider: fakeDeviceProvider{key: "docker"},
	}

	tests := []struct {
		name        string
		cfgMissing  bool
		projectType string
		target      *SelectedDevice
		want        bool
	}{
		{"all conditions met", true, "esp-idf", usbWendyLite, true},
		{"wendy.json already present", false, "esp-idf", usbWendyLite, false},
		{"not an esp-idf project shape", true, "docker", usbWendyLite, false},
		{"wendy-lite over LAN, not USB", true, "esp-idf", lanWendyLite, false},
		{"USB but not the wendy-lite provider", true, "esp-idf", usbOtherProvider, false},
		{"nil target", true, "esp-idf", nil, false},
		{"target with no External/Provider (agent path)", true, "esp-idf", &SelectedDevice{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldOfferWendyLiteESPIDFScaffold(tt.cfgMissing, tt.projectType, tt.target)
			if got != tt.want {
				t.Errorf("shouldOfferWendyLiteESPIDFScaffold(%v, %q, ...) = %v, want %v", tt.cfgMissing, tt.projectType, got, tt.want)
			}
		})
	}
}
