package camera

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/gousb"
)

// PacketHeaderDump opens the microscope, and captures a short packet-header sample,
// returning its output.
func PacketHeaderDump(count int, timeout time.Duration) (string, error) {
	var b strings.Builder
	b.WriteString("open: start\n")

	stream, err := Open()
	if err != nil {
		return b.String(), err
	}
	defer stream.Close()
	b.WriteString("open: success\n")
	b.WriteString("debug: start\n")

	lines, err := stream.DebugPacketHeaders(count, timeout)
	if err != nil {
		return b.String(), err
	}
	fmt.Fprintf(&b, "captured %d packet log lines\n", len(lines))
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// ProbeCapture opens the microscope, saves one JPEG frame to the system temp
// directory, and returns a short summary of device and stream stats.
func ProbeCapture(timeout time.Duration) (string, error) {
	stream, err := Open()
	if err != nil {
		return "", err
	}
	defer stream.Close()

	info := stream.DeviceInfo()
	frame, err := stream.ReadFrame(timeout)
	if err != nil {
		return "", err
	}
	out := filepath.Join(os.TempDir(), "microview-probe.jpg")
	if err := os.WriteFile(out, frame.JPEG, 0o644); err != nil {
		return "", err
	}
	stats := stream.Stats()
	return fmt.Sprintf(
		"device: %s %s serial=%s usb=%s:%s\nwrote %s\nframes=%d usb_errors=%d bad_frames=%d fps=%0.1f",
		info.Manufacturer,
		info.Product,
		info.Serial,
		info.VendorID,
		info.ProductID,
		out,
		stats.Frames,
		stats.USBErrors,
		stats.BadFrames,
		stream.FPS(),
	), nil
}

// DescriptorDump enumerates matching USB devices and returns their configs,
// interfaces, and endpoint descriptors as plain text.
func DescriptorDump() (string, error) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		for _, id := range knownDevices {
			if desc.Vendor == id.Vendor && desc.Product == id.Product {
				return true
			}
		}
		return false
	})
	if err != nil {
		return "", err
	}
	defer func() {
		for _, d := range devs {
			d.Close()
		}
	}()
	if len(devs) == 0 {
		return "", errNoDevice
	}

	d := devs[0]
	var b strings.Builder
	fmt.Fprintf(&b, "device %s\n", d)
	for _, cfg := range d.Desc.Configs {
		fmt.Fprintf(&b, "config %d\n", cfg.Number)
		for _, intf := range cfg.Interfaces {
			for _, alt := range intf.AltSettings {
				fmt.Fprintf(&b, "  interface %d alt %d class=%s endpoints=%d\n", alt.Number, alt.Alternate, alt.Class, len(alt.Endpoints))
				for addr, ep := range alt.Endpoints {
					fmt.Fprintf(&b, "    ep addr=%s num=%d dir=%v type=%v max=%d\n", addr, ep.Number, ep.Direction, ep.TransferType, ep.MaxPacketSize)
				}
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
