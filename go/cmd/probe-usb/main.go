package main

import (
	"fmt"
	"github.com/google/gousb"
)

func main() {
	ctx := gousb.NewContext()
	defer ctx.Close()
	ctx.Debug(0)

	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return uint16(desc.Vendor) == 0x0955
	})
	if err != nil {
		fmt.Printf("error opening: %v\n", err)
	}
	for _, dev := range devs {
		fmt.Printf("Device: VID=%04x PID=%04x\n", uint16(dev.Desc.Vendor), uint16(dev.Desc.Product))
		for cfgNum, cfg := range dev.Desc.Configs {
			fmt.Printf("  Config %d:\n", cfgNum)
			for ifNum, iface := range cfg.Interfaces {
				fmt.Printf("    Interface %d:\n", ifNum)
				for altNum, alt := range iface.AltSettings {
					fmt.Printf("      AltSetting %d (class=%d subclass=%d proto=%d):\n", altNum, alt.Class, alt.SubClass, alt.Protocol)
					for _, ep := range alt.Endpoints {
						fmt.Printf("        Endpoint %d: dir=%v type=%v maxPkt=%d\n", ep.Number, ep.Direction, ep.TransferType, ep.MaxPacketSize)
					}
				}
			}
		}
		dev.Close()
	}
}
