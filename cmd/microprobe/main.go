package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"microview/internal/camera"
)

func main() {
	stream, err := camera.Open()
	if err != nil {
		panic(err)
	}
	defer stream.Close()

	info := stream.DeviceInfo()
	fmt.Printf("device: %s %s serial=%s usb=%s:%s\n", info.Manufacturer, info.Product, info.Serial, info.VendorID, info.ProductID)
	frame, err := stream.ReadFrame(5 * time.Second)
	if err != nil {
		panic(err)
	}
	out := filepath.Join(os.TempDir(), "microview-probe.jpg")
	if err := os.WriteFile(out, frame.JPEG, 0o644); err != nil {
		panic(err)
	}
	stats := stream.Stats()
	fmt.Printf("wrote %s\nframes=%d usb_errors=%d bad_frames=%d fps=%0.1f\n", out, stats.Frames, stats.USBErrors, stats.BadFrames, stream.FPS())
}
