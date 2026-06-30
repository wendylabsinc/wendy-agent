package commands

import "testing"

// The hidden "__usb-setup" subcommand is the privileged half of the USB-C
// auto-setup flow, re-executed under sudo by maybeOfferUSBSetup.
func TestNewUSBSetupHiddenCmd_Flags(t *testing.T) {
	cmd := newUSBSetupHiddenCmd()
	if cmd.Use != "__usb-setup" {
		t.Fatalf("Use = %q, want __usb-setup", cmd.Use)
	}
	if !cmd.Hidden {
		t.Error("expected __usb-setup to be hidden")
	}
	if cmd.Flags().Lookup("iface") == nil {
		t.Error("missing flag --iface")
	}
}
