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
	"sync/atomic"
	"time"

	"github.com/google/gousb"
)

var (
	errNoDevice  = errors.New("no microscope found")
	errFrameRead = errors.New("no frame within timeout")
	jpegSOI      = []byte{0xff, 0xd8}
	jpegEOI      = []byte{0xff, 0xd9}
	magicInit    = []byte{0xFF, 0x55, 0xFF, 0x55, 0xEE, 0x10}
	connectCmd   = []byte{0xBB, 0xAA, 0x05, 0x00, 0x00}
	validCIDs    = map[byte]bool{7: true, 11: true}
)

type usbID struct {
	Vendor  gousb.ID
	Product gousb.ID
}

var knownDevices = []usbID{{Vendor: 0x2CE3, Product: 0x3828}, {Vendor: 0x0329, Product: 0x2022}}

const (
	epOut         = 0x01
	epIn          = 0x81
	epIAPOut      = 0x02
	epIAPIn       = 0x82
	packetSize    = 0x400
	usbHeaderSize = 5
	payloadOffset = 12
	resolutionW   = 640
	resolutionH   = 480
)

type Stats struct {
	Frames     uint64
	USBErrors  uint64
	BadFrames  uint64
	Reconnects uint64
}

type DeviceInfo struct {
	VendorID     gousb.ID
	ProductID    gousb.ID
	Manufacturer string
	Product      string
	Serial       string
}

type Frame struct {
	JPEG  []byte
	Image image.Image
	At    time.Time
}

type Stream struct {
	mu         sync.Mutex
	ctx        *gousb.Context
	device     *gousb.Device
	config     *gousb.Config
	iface0     *gousb.Interface
	iface1     *gousb.Interface
	inEP       *gousb.InEndpoint
	outEP      *gousb.OutEndpoint
	iapInEP    *gousb.InEndpoint
	iapOutEP   *gousb.OutEndpoint
	buf        []byte
	curFID     *byte
	framesRead atomic.Uint64
	stats      Stats
	start      time.Time
	info       DeviceInfo
	closed     bool
}

func Open() (*Stream, error) {
	s := &Stream{start: time.Now()}
	if err := s.connectWithRetry(3); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Stream) DeviceInfo() DeviceInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Stream) FPS() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(s.start).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(s.stats.Frames) / elapsed
}

func (s *Stream) Resolution() image.Point {
	return image.Pt(resolutionW, resolutionH)
}

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

func (s *Stream) DebugPacketHeaders(count int, timeout time.Duration) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.inEP == nil {
		return nil, errors.New("stream is closed")
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

func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.disconnectLocked()
}

func (s *Stream) connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectLocked()
}

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

func (s *Stream) connectLocked() error {
	ctx := gousb.NewContext()
	var opened *gousb.Device
	devices, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		for _, id := range knownDevices {
			if desc.Vendor == id.Vendor && desc.Product == id.Product {
				return true
			}
		}
		return false
	})
	if err != nil {
		ctx.Close()
		return fmt.Errorf("open usb devices: %w", err)
	}
	for i, dev := range devices {
		if i == 0 {
			opened = dev
		} else {
			dev.Close()
		}
	}
	if opened == nil {
		ctx.Close()
		return errNoDevice
	}
	opened.ControlTimeout = time.Second

	config, err := opened.Config(1)
	if err != nil {
		opened.Close()
		ctx.Close()
		return fmt.Errorf("open config: %w", err)
	}
	iface0, err := config.Interface(0, 0)
	if err != nil {
		config.Close()
		opened.Close()
		ctx.Close()
		return fmt.Errorf("claim interface 0: %w", err)
	}
	iface1, err := config.Interface(1, 1)
	if err != nil {
		iface0.Close()
		config.Close()
		opened.Close()
		ctx.Close()
		return fmt.Errorf("claim interface 1: %w", err)
	}
	inEP, err := iface1.InEndpoint(1)
	if err != nil {
		iface1.Close()
		iface0.Close()
		config.Close()
		opened.Close()
		ctx.Close()
		return fmt.Errorf("open stream in endpoint: %w", err)
	}
	outEP, err := iface1.OutEndpoint(1)
	if err != nil {
		iface1.Close()
		iface0.Close()
		config.Close()
		opened.Close()
		ctx.Close()
		return fmt.Errorf("open stream out endpoint: %w", err)
	}
	iapIn, err := iface0.InEndpoint(2)
	if err != nil {
		iface1.Close()
		iface0.Close()
		config.Close()
		opened.Close()
		ctx.Close()
		return fmt.Errorf("open iap in endpoint: %w", err)
	}
	iapOut, err := iface0.OutEndpoint(2)
	if err != nil {
		iface1.Close()
		iface0.Close()
		config.Close()
		opened.Close()
		ctx.Close()
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
	// Python path explicitly clears halt on bulk OUT before sending connect.
	if _, err := s.device.Control(0x02, 0x01, 0x0000, uint16(epOut), nil); err != nil {
		// Ignore if the endpoint is not halted; this is a recovery step.
	}
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

func (s *Stream) readJPEGLocked(timeout time.Duration) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readJPEGWithTimeoutLocked(timeout)
}

func (s *Stream) readJPEGWithTimeoutLocked(timeout time.Duration) ([]byte, error) {
	if s.closed || s.inEP == nil {
		return nil, errors.New("stream is closed")
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
		if pkt[0] != 0xAA || pkt[1] != 0xBB || !validCIDs[pkt[2]] {
			continue
		}
		length := int(binary.LittleEndian.Uint16(pkt[3:5]))
		if usbHeaderSize+length > n || length < 7 {
			continue
		}
		fid := pkt[5]
		chunk := append([]byte(nil), pkt[payloadOffset:usbHeaderSize+length]...)
		if s.curFID != nil && fid != *s.curFID && len(s.buf) > 0 {
			frame := append([]byte(nil), s.buf...)
			s.buf = append(s.buf[:0], chunk...)
			s.curFID = &fid
			if isJPEG(frame) {
				s.framesRead.Add(1)
				return frame, nil
			}
			continue
		}
		s.buf = append(s.buf, chunk...)
		s.curFID = &fid
	}
	return nil, errFrameRead
}

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
