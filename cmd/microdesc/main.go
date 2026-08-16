package main

import (
	"fmt"

	"github.com/google/gousb"
)

func main() {
	ctx := gousb.NewContext()
	defer ctx.Close()
	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		return (desc.Vendor == 0x0329 && desc.Product == 0x2022) || (desc.Vendor == 0x2ce3 && desc.Product == 0x3828)
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		for _, d := range devs {
			d.Close()
		}
	}()
	if len(devs) == 0 {
		panic("no device")
	}
	d := devs[0]
	fmt.Printf("device %s\n", d)
	for _, cfg := range d.Desc.Configs {
		fmt.Printf("config %d\n", cfg.Number)
		for _, intf := range cfg.Interfaces {
			for _, alt := range intf.AltSettings {
				fmt.Printf("  interface %d alt %d class=%s endpoints=%d\n", alt.Number, alt.Alternate, alt.Class, len(alt.Endpoints))
				for addr, ep := range alt.Endpoints {
					fmt.Printf("    ep addr=%s num=%d dir=%v type=%v max=%d\n", addr, ep.Number, ep.Direction, ep.TransferType, ep.MaxPacketSize)
				}
			}
		}
	}
}
