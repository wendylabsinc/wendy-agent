//go:build windows

package winusb

import (
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/rcm"
)

func TestSelectStageOneRecoveryDeviceFailsClosed(t *testing.T) {
	const (
		path     = "PCIROOT(0)#USBROOT(0)#USB(2)"
		serial   = "0C08FF6100000042442ED38714621008"
		instance = `USB\VID_0955&PID_7026\0C08FF6100000042442ED38714621008`
	)
	wanted := Device{InstanceID: instance, PID: ProductThor, Serial: serial, LocationPath: path}
	digest := wanted.RecoveryDevice().ECIDDigest()
	opts := StageOneOptions{Location: path, Instance: instance, ExpectedProduct: ProductThor, ExpectedECIDDigest: digest}

	got, err := selectStageOneRecoveryDevice([]Device{wanted}, opts)
	if err != nil || got.InstanceID != instance {
		t.Fatalf("selected = %+v, %v", got, err)
	}

	tests := []struct {
		name    string
		devices []Device
		mutate  func(*StageOneOptions)
	}{
		{"zero", nil, nil},
		{"identity mismatch", []Device{{InstanceID: `USB\VID_0955&PID_7026\10000000000000000000000000000000`, PID: ProductThor, Serial: "10000000000000000000000000000000", LocationPath: path}}, nil},
		{"instance changed", []Device{wanted}, func(o *StageOneOptions) { o.Instance = `USB\VID_0955&PID_7026\DIFFERENT` }},
		{"missing identity", []Device{{InstanceID: `USB\VID_0955&PID_7026\7&ABC`, PID: ProductThor, Serial: "7&ABC", LocationPath: path}}, nil},
		{"multiple", []Device{wanted, wanted}, nil},
		{"missing product", []Device{wanted}, func(o *StageOneOptions) { o.ExpectedProduct = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := opts
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			_, err := selectStageOneRecoveryDevice(tc.devices, candidate)
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
			if strings.Contains(strings.ToLower(err.Error()), "80012641783de2442400000016ff80c0") || strings.Contains(err.Error(), digest) {
				t.Fatalf("identity leaked in error: %v", err)
			}
		})
	}

	tooMany := make([]Device, rcm.MaxRecoveryDevices+1)
	for i := range tooMany {
		tooMany[i] = wanted
		tooMany[i].InstanceID += string(rune('a' + i%26))
	}
	if _, err := selectStageOneRecoveryDevice(tooMany, opts); err == nil {
		t.Fatal("oversized recovery discovery accepted")
	}
}

func TestRecoveryOpenErrorsNeverExposeInstanceIdentifiers(t *testing.T) {
	const (
		reversed  = "0C08FF6100000042442ED38714621008"
		canonical = "80012641783DE2442400000016FF80C0"
	)
	instance := `USB\VID_0955&PID_7523\` + reversed
	_, productErr := OpenInstance(instance, ProductThor) // pure mismatch: no OS access
	errs := []error{
		productErr,
		noRecoveryInterfaceError(),
		wrapRecoveryInterfaceOpenError(errors.New("access denied")),
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected error")
		}
		for _, forbidden := range []string{instance, reversed, canonical} {
			if strings.Contains(strings.ToUpper(err.Error()), strings.ToUpper(forbidden)) {
				t.Fatalf("recovery identity leaked in error: %v", err)
			}
		}
	}
}
