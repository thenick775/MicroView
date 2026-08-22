package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"microview/internal/camera"

	"fyne.io/fyne/v2"
)

type sessionSnapshot struct {
	Connected bool
	Info      camera.DeviceInfo
	Stats     camera.Stats
	FPS       float64
}

type sessionHooks struct {
	setStatus           func(string)
	setDevice           func(string)
	setReconnectEnabled func(bool)
	addEvent            func(string)
	updateDiagnostics   func()
	refreshPreview      func()
	setConnecting       func(bool)
}

type session struct {
	streamMu    sync.RWMutex
	connectMu   sync.Mutex
	frameMu     sync.RWMutex
	stream      *camera.Stream
	streamStop  chan struct{}
	diagStop    chan struct{}
	latestFrame *camera.Frame
	connected   bool
	connecting  bool
	closing     bool
	hooks       sessionHooks
}

func newSession(hooks sessionHooks) *session {
	return &session{hooks: hooks}
}

// Start begins the initial microscope connection attempt and starts streaming
// on success.
func (s *session) Start() {
	s.runConnectionAttempt(
		"Connecting to microscope...",
		"connect started",
		"Connection failed",
		"connect failed",
		func() error {
			return s.openAndStream("Connected. Streaming live video.", "stream connected")
		},
	)
}

// Reconnect retries the current microscope connection and refreshes session
// state when the device comes back.
func (s *session) Reconnect() {
	stream := s.currentStream()
	s.runConnectionAttempt(
		"Reconnecting to microscope...",
		"reconnect started",
		"Reconnect failed",
		"reconnect failed",
		func() error {
			if stream == nil {
				return s.openAndStream("Reconnected successfully. Streaming live video.", "reconnected")
			}
			if err := stream.Reconnect(); err != nil {
				return err
			}
			s.applyConnectionSuccess(stream, "Reconnected successfully. Streaming live video.", "reconnected")
			s.hooks.updateDiagnostics()
			return nil
		},
	)
}

// Stop marks the session as closing and tears down any active stream.
func (s *session) Stop() {
	s.connectMu.Lock()
	s.closing = true
	s.connectMu.Unlock()
	s.stopStreaming()
}

// LatestFrame returns the most recently decoded camera frame, if available.
func (s *session) LatestFrame() *camera.Frame {
	s.frameMu.RLock()
	defer s.frameMu.RUnlock()
	return s.latestFrame
}

// Snapshot returns the current connection state and stream counters for UI
// diagnostics.
func (s *session) Snapshot() sessionSnapshot {
	s.streamMu.RLock()
	stream := s.stream
	connected := s.connected
	s.streamMu.RUnlock()
	if stream == nil {
		return sessionSnapshot{Connected: connected}
	}
	return sessionSnapshot{
		Connected: connected,
		Info:      stream.DeviceInfo(),
		Stats:     stream.Stats(),
		FPS:       stream.FPS(),
	}
}

// RunDiagnostic executes a debug action off the UI goroutine and reports the
// final text result through onDone.
func (s *session) RunDiagnostic(title string, requiresExclusiveStream bool, fn func() (string, error), onDone func(string, error)) {
	go func() {
		fyne.Do(func() {
			s.hooks.setStatus("Running debug diagnostics...")
			s.hooks.addEvent(strings.ToLower(title) + " started")
		})

		wasConnected := false
		if requiresExclusiveStream {
			wasConnected = s.currentStream() != nil
			if wasConnected {
				s.stopStreaming()
			}
		}

		result, err := fn()
		if err != nil {
			fyne.Do(func() {
				s.hooks.addEvent(strings.ToLower(title) + " failed")
			})
		} else {
			fyne.Do(func() {
				s.hooks.addEvent(strings.ToLower(title) + " completed")
			})
		}
		if requiresExclusiveStream && wasConnected {
			_ = s.openAndStream("Connected. Streaming live video.", "stream connected")
		}
		fyne.Do(s.hooks.updateDiagnostics)
		fyne.Do(func() {
			onDone(result, err)
		})
	}()
}

func (s *session) runConnectionAttempt(status, startDiag, failStatus, failDiag string, fn func() error) {
	s.connectMu.Lock()
	if s.connecting || s.closing {
		s.connectMu.Unlock()
		return
	}
	s.connecting = true
	s.connectMu.Unlock()

	s.hooks.setStatus(status)
	s.hooks.setConnecting(true)
	s.hooks.addEvent(startDiag)
	s.hooks.setReconnectEnabled(false)
	s.hooks.updateDiagnostics()

	go func() {
		defer func() {
			s.connectMu.Lock()
			s.connecting = false
			s.connectMu.Unlock()
			fyne.Do(func() {
				s.hooks.setReconnectEnabled(true)
				s.hooks.setConnecting(false)
			})
		}()

		s.connectMu.Lock()
		closing := s.closing
		s.connectMu.Unlock()
		if closing {
			return
		}

		if err := fn(); err != nil {
			s.streamMu.Lock()
			s.connected = false
			s.streamMu.Unlock()
			fyne.Do(func() {
				s.hooks.setStatus(fmt.Sprintf("%s: %v", failStatus, err))
				s.hooks.setDevice("Device: not connected")
				s.hooks.addEvent(fmt.Sprintf("%s: %v", failDiag, err))
				s.hooks.updateDiagnostics()
			})
		}
	}()
}

func (s *session) openAndStream(status, event string) error {
	s.stopStreaming()

	stream, err := camera.Open()
	if err != nil {
		return err
	}
	if err := s.attachStream(stream); err != nil {
		_ = stream.Close()
		return err
	}
	s.applyConnectionSuccess(stream, status, event)
	go s.diagnosticsLoop(s.currentDiagStop())
	go s.streamLoop(stream, s.currentStop())
	s.hooks.updateDiagnostics()
	return nil
}

func (s *session) applyConnectionSuccess(stream *camera.Stream, status, event string) {
	s.streamMu.Lock()
	s.connected = true
	s.streamMu.Unlock()
	info := stream.DeviceInfo()
	s.hooks.setDevice(fmt.Sprintf("Device: %s %s (%s)\nUSB: %s:%s", safe(info.Manufacturer, "Unknown"), safe(info.Product, "supercamera"), safe(info.Serial, "no serial"), info.VendorID, info.ProductID))
	s.hooks.setStatus(status)
	s.hooks.addEvent(event)
}

func (s *session) stopStreaming() {
	s.streamMu.Lock()
	stop := s.streamStop
	diagStop := s.diagStop
	stream := s.stream
	s.streamStop = nil
	s.diagStop = nil
	s.stream = nil
	s.connected = false
	s.streamMu.Unlock()

	if stop != nil {
		close(stop)
	}
	if diagStop != nil {
		close(diagStop)
	}
	if stream != nil {
		_ = stream.Close()
	}
	// Keep the last frame available for preview and snapshot history until replaced.
}

func (s *session) streamLoop(stream *camera.Stream, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		frame, err := stream.ReadFrame(3 * time.Second)
		if err != nil {
			select {
			case <-stop:
				return
			default:
			}
			fyne.Do(func() {
				s.hooks.setStatus(fmt.Sprintf("Stream error: %v", err))
				s.hooks.addEvent(fmt.Sprintf("stream error: %v", err))
				s.hooks.updateDiagnostics()
			})
			continue
		}

		s.frameMu.Lock()
		s.latestFrame = frame
		s.frameMu.Unlock()
		fyne.Do(s.hooks.refreshPreview)
	}
}

func (s *session) diagnosticsLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			fyne.Do(s.hooks.updateDiagnostics)
		}
	}
}

func (s *session) attachStream(stream *camera.Stream) error {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streamStop != nil || s.stream != nil {
		return fmt.Errorf("stream already active")
	}
	s.stream = stream
	s.streamStop = make(chan struct{})
	s.diagStop = make(chan struct{})
	return nil
}

func (s *session) currentStream() *camera.Stream {
	s.streamMu.RLock()
	defer s.streamMu.RUnlock()
	return s.stream
}

func (s *session) currentStop() <-chan struct{} {
	s.streamMu.RLock()
	defer s.streamMu.RUnlock()
	return s.streamStop
}

func (s *session) currentDiagStop() <-chan struct{} {
	s.streamMu.RLock()
	defer s.streamMu.RUnlock()
	return s.diagStop
}
