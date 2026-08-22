package camera

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"sync"
	"time"

	"github.com/google/gousb"
)

var (
	errNoDevice     = errors.New("no microscope found")
	errFrameRead    = errors.New("no frame within timeout")
	errStreamClosed = errors.New("stream is closed")
	jpegSOI         = []byte{0xff, 0xd8}
	jpegEOI         = []byte{0xff, 0xd9}
	magicInit       = []byte{0xFF, 0x55, 0xFF, 0x55, 0xEE, 0x10}
	connectCmd      = []byte{0xBB, 0xAA, 0x05, 0x00, 0x00}
	knownDevices    = []usbID{{Vendor: 0x2CE3, Product: 0x3828}, {Vendor: 0x0329, Product: 0x2022}}
)

const (
	epOut         = 0x01
	packetSize    = 0x400
	usbHeaderSize = 5
	payloadOffset = 12
)

type usbID struct {
	Vendor  gousb.ID
	Product gousb.ID
}

// Stats tracks stream health and frame counters for the current connection.
type Stats struct {
	Frames     uint64
	USBErrors  uint64
	BadFrames  uint64
	Reconnects uint64
}

// DeviceInfo describes the currently opened microscope USB device.
type DeviceInfo struct {
	Manufacturer string
	Product      string
	Serial       string
	VendorID     gousb.ID
	ProductID    gousb.ID
}

// Frame contains one decoded microscope frame and its original JPEG payload.
type Frame struct {
	At    time.Time
	Image image.Image
	JPEG  []byte
}

// Stream owns the USB connection to the microscope and provides frame reads,
// reconnects, and basic device diagnostics.
type Stream struct {
	start    time.Time
	ctx      *gousb.Context
	device   *gousb.Device
	config   *gousb.Config
	iface0   *gousb.Interface
	iface1   *gousb.Interface
	inEP     *gousb.InEndpoint
	outEP    *gousb.OutEndpoint
	iapInEP  *gousb.InEndpoint
	iapOutEP *gousb.OutEndpoint
	curFID   *byte
	info     DeviceInfo
	buf      []byte
	stats    Stats
	mu       sync.Mutex
	closed   bool
}

// Open locates a supported microscope, performs its startup handshake, and
// returns a ready-to-read stream.
func Open() (*Stream, error) {
	s := &Stream{start: time.Now()}
	if err := s.connectWithRetry(3); err != nil {
		return nil, err
	}
	return s, nil
}

// DeviceInfo returns the USB identity strings for the currently opened device.
func (s *Stream) DeviceInfo() DeviceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// Stats returns cumulative stream counters since the current connection began.
func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// FPS returns the average delivered frame rate for the current connection.
func (s *Stream) FPS() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(s.start).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(s.stats.Frames) / elapsed
}

// ReadFrame waits for the next valid JPEG frame, decodes it, and returns both
// the raw bytes and decoded image.
func (s *Stream) ReadFrame(timeout time.Duration) (*Frame, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		jpegBytes, err := s.readJPEGLocked(time.Until(deadline))
		if err != nil {
			if errors.Is(err, errFrameRead) {
				continue
			}
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(jpegBytes))
		if err != nil {
			s.mu.Lock()
			s.stats.BadFrames++
			s.mu.Unlock()
			continue
		}
		frame := &Frame{JPEG: jpegBytes, Image: img, At: time.Now()}
		s.mu.Lock()
		s.stats.Frames++
		s.mu.Unlock()
		return frame, nil
	}
	return nil, errFrameRead
}

// DebugPacketHeaders captures raw packet header summaries for protocol
// debugging without attempting to assemble full JPEG frames.
func (s *Stream) DebugPacketHeaders(count int, timeout time.Duration) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.inEP == nil {
		return nil, errStreamClosed
	}
	lines := make([]string, 0, count)
	deadline := time.Now().Add(timeout)
	pkt := make([]byte, packetSize)
	for len(lines) < count && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), min(time.Second, time.Until(deadline)))
		n, err := s.inEP.ReadContext(ctx, pkt)
		cancel()
		if err != nil {
			lines = append(lines, fmt.Sprintf("read error: %v", err))
			continue
		}
		if n == 0 {
			lines = append(lines, "empty packet")
			continue
		}
		line := fmt.Sprintf("n=%d hdr=% x", n, pkt[:min(n, 16)])
		if n >= 12 {
			length := int(binary.LittleEndian.Uint16(pkt[3:5]))
			line = fmt.Sprintf("n=%d magic=%02x%02x cid=%d len=%d fid=%d head=% x", n, pkt[0], pkt[1], pkt[2], length, pkt[5], pkt[:min(n, 16)])
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// Reconnect tears down the current USB state and retries the microscope
// handshake in place so existing callers can keep using the same stream.
func (s *Stream) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.disconnectLocked(); err != nil {
		return err
	}
	s.stats.Reconnects++
	s.start = time.Now()
	var lastErr error
	for range 3 {
		if err := s.connectLocked(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		_ = s.disconnectLocked()
		time.Sleep(1500 * time.Millisecond)
	}
	return fmt.Errorf("reconnect failed after 3 attempts: %w", lastErr)
}

// Close releases the USB device and marks the stream as permanently closed.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.disconnectLocked()
}

// connect acquires the stream lock before delegating to the internal connect
// path used by Open and retry logic.
func (s *Stream) connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectLocked()
}

// connectWithRetry retries the full device open sequence a small number of
// times because these microscopes can briefly NAK while transitioning states.
func (s *Stream) connectWithRetry(attempts int) error {
	var lastErr error
	for range attempts {
		if err := s.connect(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		s.mu.Lock()
		_ = s.disconnectLocked()
		s.mu.Unlock()
		time.Sleep(1500 * time.Millisecond)
	}
	return fmt.Errorf("could not open device after %d attempts: %w", attempts, lastErr)
}

// connectLocked opens the matching USB device, claims the stream interfaces,
// and stores the resulting handles on the stream.
func (s *Stream) connectLocked() (err error) {
	ctx := gousb.NewContext()
	var (
		opened *gousb.Device
		config *gousb.Config
		iface0 *gousb.Interface
		iface1 *gousb.Interface
		inEP   *gousb.InEndpoint
		outEP  *gousb.OutEndpoint
		iapIn  *gousb.InEndpoint
		iapOut *gousb.OutEndpoint
	)
	defer func() {
		if err == nil {
			return
		}
		if iface1 != nil {
			iface1.Close()
		}
		if iface0 != nil {
			iface0.Close()
		}
		if config != nil {
			_ = config.Close()
		}
		if opened != nil {
			_ = opened.Close()
		}
		_ = ctx.Close()
	}()
	// The camera presents a proprietary, non-UVC interface. We match the known
	// VID:PID pairs and keep only the first successful open device.
	devices, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		for _, id := range knownDevices {
			if desc.Vendor == id.Vendor && desc.Product == id.Product {
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("open usb devices: %w", err)
	}
	for i, dev := range devices {
		if i == 0 {
			opened = dev
		} else {
			_ = dev.Close()
		}
	}
	if opened == nil {
		return errNoDevice
	}
	opened.ControlTimeout = time.Second

	config, err = opened.Config(1)
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}
	iface0, err = config.Interface(0, 0)
	if err != nil {
		return fmt.Errorf("claim interface 0: %w", err)
	}
	iface1, err = config.Interface(1, 1)
	if err != nil {
		return fmt.Errorf("claim interface 1: %w", err)
	}
	inEP, err = iface1.InEndpoint(1)
	if err != nil {
		return fmt.Errorf("open stream in endpoint: %w", err)
	}
	outEP, err = iface1.OutEndpoint(1)
	if err != nil {
		return fmt.Errorf("open stream out endpoint: %w", err)
	}
	iapIn, err = iface0.InEndpoint(2)
	if err != nil {
		return fmt.Errorf("open iap in endpoint: %w", err)
	}
	iapOut, err = iface0.OutEndpoint(2)
	if err != nil {
		return fmt.Errorf("open iap out endpoint: %w", err)
	}

	s.ctx = ctx
	s.device = opened
	s.config = config
	s.iface0 = iface0
	s.iface1 = iface1
	s.inEP = inEP
	s.outEP = outEP
	s.iapInEP = iapIn
	s.iapOutEP = iapOut
	s.buf = nil
	s.curFID = nil
	manufacturer, _ := opened.Manufacturer()
	product, _ := opened.Product()
	serial, _ := opened.SerialNumber()
	s.info = DeviceInfo{
		VendorID:     opened.Desc.Vendor,
		ProductID:    opened.Desc.Product,
		Manufacturer: manufacturer,
		Product:      product,
		Serial:       serial,
	}

	if err := s.handshakeLocked(); err != nil {
		_ = s.disconnectLocked()
		return err
	}
	return nil
}

// handshakeLocked issues the vendor-specific startup commands required before
// the stream endpoint begins delivering JPEG payloads.
func (s *Stream) handshakeLocked() error {
	buf := make([]byte, 512)
	for range 30 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := s.iapInEP.ReadContext(ctx, buf)
		cancel()
		if err != nil {
			break
		}
	}
	// Best-effort recovery: clear any halt/stall on the stream OUT endpoint
	// before sending the proprietary connect sequence.
	_, _ = s.device.Control(0x02, 0x01, 0x0000, uint16(epOut), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_, err := s.iapOutEP.WriteContext(ctx, magicInit)
	cancel()
	if err != nil {
		return fmt.Errorf("write magic init: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	_, err = s.outEP.WriteContext(ctx, connectCmd)
	cancel()
	if err != nil {
		return fmt.Errorf("write connect command: %w", err)
	}
	time.Sleep(300 * time.Millisecond)
	s.buf = nil
	s.curFID = nil
	return nil
}

// readJPEGLocked reads bulk packets until a complete JPEG frame boundary is
// observed, then returns the assembled JPEG bytes.
func (s *Stream) readJPEGLocked(timeout time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.inEP == nil {
		return nil, errStreamClosed
	}
	deadline := time.Now().Add(timeout)
	pkt := make([]byte, packetSize)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(time.Second, remaining))
		n, err := s.inEP.ReadContext(ctx, pkt)
		cancel()
		if err != nil {
			s.stats.USBErrors++
			continue
		}
		if n < payloadOffset {
			continue
		}
		if pkt[0] != 0xAA || pkt[1] != 0xBB || (pkt[2] != 7 && pkt[2] != 11) {
			continue
		}
		length := int(binary.LittleEndian.Uint16(pkt[3:5]))
		if usbHeaderSize+length > n || length < 7 {
			continue
		}
		fid := pkt[5]
		chunk := append([]byte(nil), pkt[payloadOffset:usbHeaderSize+length]...)
		// The device increments FID when it rolls over to the next JPEG, so a FID
		// change tells us the buffered payload belongs to a complete prior frame.
		if s.curFID != nil && fid != *s.curFID && len(s.buf) > 0 {
			frame := append([]byte(nil), s.buf...)
			s.buf = append(s.buf[:0], chunk...)
			s.curFID = &fid
			if isJPEG(frame) {
				return frame, nil
			}
			continue
		}
		s.buf = append(s.buf, chunk...)
		s.curFID = &fid
	}
	return nil, errFrameRead
}

// disconnectLocked releases the claimed USB resources in reverse ownership
// order and clears the cached stream state.
func (s *Stream) disconnectLocked() error {
	var firstErr error
	if s.iface1 != nil {
		s.iface1.Close()
		s.iface1 = nil
	}
	if s.iface0 != nil {
		s.iface0.Close()
		s.iface0 = nil
	}
	if s.config != nil {
		err := s.config.Close()
		if err != nil {
			firstErr = err
		}
		s.config = nil
	}
	if s.device != nil {
		err := s.device.Close()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
		s.device = nil
	}
	if s.ctx != nil {
		err := s.ctx.Close()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
		s.ctx = nil
	}
	s.inEP = nil
	s.outEP = nil
	s.iapInEP = nil
	s.iapOutEP = nil
	s.buf = nil
	s.curFID = nil
	return firstErr
}

func isJPEG(b []byte) bool {
	return len(b) >= 4 && bytes.HasPrefix(b, jpegSOI) && bytes.HasSuffix(b, jpegEOI)
}
