package app

import (
	"bytes"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"microview/internal/camera"
	"microview/internal/imaging"
	"microview/internal/theming"
)

const (
	appID           = "com.nickvancise.microview"
	defaultSaveDir  = "~/Desktop/MicroView Captures"
	maxRecentFrames = 8
)

type capture struct {
	name  string
	path  string
	img   image.Image
	stamp time.Time
}

type uiState struct {
	app         fyne.App
	win         fyne.Window
	stream      *camera.Stream
	streamMu    sync.Mutex
	connectMu   sync.Mutex
	frameMu     sync.RWMutex
	latestFrame *camera.Frame
	recent      []capture
	rotations   int
	crosshair   bool
	fullscreen  bool
	saveDir     string
	connected   bool
	streamStop  chan struct{}
	diagLog     []string
	themeMode   string
	connecting  bool
	closing     bool

	preview          *canvas.Image
	statusLabel      *widget.Label
	diagnosticsEntry *widget.Entry
	deviceLabel      *widget.Label
	folderLabel      *widget.Label
	recentGrid       *fyne.Container
	crosshairCheck   *widget.Check
	rotateLabel      *widget.Label
	snapshotBtn      *widget.Button
	reconnectBtn     *widget.Button
}

func Run() {
	a := app.NewWithID(appID)
	themeMode := a.Preferences().StringWithFallback("themeMode", theming.ModeDark)
	a.Settings().SetTheme(theming.ThemeForMode(themeMode))
	w := a.NewWindow("MicroView")
	w.Resize(fyne.NewSize(1320, 860))

	state := &uiState{
		app:       a,
		win:       w,
		saveDir:   expandHome(defaultSaveDir),
		crosshair: true,
		diagLog:   []string{"Ready"},
		themeMode: themeMode,
	}
	if saved := a.Preferences().StringWithFallback("saveDir", ""); saved != "" {
		state.saveDir = saved
	}

	w.SetMainMenu(state.buildMainMenu())
	state.buildUI()
	state.win.SetCloseIntercept(func() {
		state.connectMu.Lock()
		state.closing = true
		state.connectMu.Unlock()
		state.stopStreaming()
		state.win.Close()
	})
	state.win.Show()
	state.startConnect()
	a.Run()
}

func (s *uiState) buildUI() {
	s.preview = canvas.NewImageFromImage(imaging.Blank(640, 480))
	s.preview.FillMode = canvas.ImageFillContain
	s.preview.SetMinSize(fyne.NewSize(320, 240))

	s.statusLabel = widget.NewLabel("Connecting to microscope...")
	s.statusLabel.Wrapping = fyne.TextWrapWord
	s.diagnosticsEntry = widget.NewMultiLineEntry()
	s.diagnosticsEntry.Wrapping = fyne.TextWrapWord
	s.diagnosticsEntry.Disable()
	s.diagnosticsEntry.SetMinRowsVisible(8)
	s.deviceLabel = widget.NewLabel("Device: not connected")
	s.folderLabel = widget.NewLabel(s.saveDir)
	s.folderLabel.Wrapping = fyne.TextWrapWord
	s.rotateLabel = widget.NewLabel("Rotation: 0°")

	s.crosshairCheck = widget.NewCheck("Crosshair", func(v bool) {
		s.crosshair = v
		s.refreshPreview()
	})
	s.crosshairCheck.SetChecked(s.crosshair)

	s.snapshotBtn = widget.NewButtonWithIcon("Snapshot", theme.DocumentSaveIcon(), func() {
		s.saveSnapshot()
	})
	s.reconnectBtn = widget.NewButtonWithIcon("Reconnect", theme.ViewRefreshIcon(), func() {
		s.reconnect()
	})

	rotateBtn := widget.NewButtonWithIcon("Rotate", theme.ViewRefreshIcon(), func() {
		s.rotations = (s.rotations + 1) % 4
		s.rotateLabel.SetText(fmt.Sprintf("Rotation: %d°", s.rotations*90))
		s.refreshPreview()
	})
	fullscreenBtn := widget.NewButtonWithIcon("Fullscreen", theme.ViewFullScreenIcon(), func() {
		s.fullscreen = !s.fullscreen
		s.win.SetFullScreen(s.fullscreen)
	})
	folderBtn := widget.NewButtonWithIcon("Choose Folder", theme.FolderOpenIcon(), func() {
		s.chooseFolder()
	})

	leftTop := container.NewBorder(nil, container.NewVBox(
		widget.NewSeparator(),
		container.NewGridWithColumns(5,
			s.snapshotBtn,
			rotateBtn,
			s.reconnectBtn,
			fullscreenBtn,
			folderBtn,
		),
		container.NewHBox(s.crosshairCheck, layout.NewSpacer(), s.rotateLabel),
	), nil, nil, s.preview)

	s.recentGrid = container.NewGridWithColumns(2)
	recentCard := widget.NewCard("Recent Captures", "Latest snapshots", container.NewVScroll(s.recentGrid))

	settingsCard := widget.NewCard("Session", "Capture folder and current device",
		container.NewVBox(
			widget.NewLabel("Capture Folder"),
			s.folderLabel,
			widget.NewSeparator(),
			s.deviceLabel,
			widget.NewSeparator(),
			s.statusLabel,
		),
	)
	tabs := container.NewAppTabs(
		container.NewTabItem("Session", settingsCard),
		container.NewTabItem("Diagnostics", widget.NewCard("Diagnostics", "Device and stream status", s.diagnosticsEntry)),
		container.NewTabItem("Captures", recentCard),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	content := container.NewHSplit(leftTop, tabs)
	content.SetOffset(0.82)

	s.win.SetContent(content)
	s.refreshRecentGrid()
	s.updateDiagnostics()
}

func (s *uiState) connectAndStream(status, diag string) error {
	s.stopStreaming()

	stream, err := camera.Open()
	if err != nil {
		return err
	}
	if err := s.attachStream(stream); err != nil {
		_ = stream.Close()
		return err
	}
	s.finishConnectionSuccess(stream, status, diag)
	go s.streamLoop(stream, s.streamStop)
	s.updateDiagnostics()
	return nil
}

func (s *uiState) startConnect() {
	s.runConnectionAttempt(
		"Connecting to microscope...",
		"connect started",
		"Connection failed",
		"connect failed",
		true,
		"MicroView Error",
		func() error {
			return s.connectAndStream("Connected. Streaming live video.", "stream connected")
		},
	)
}

func (s *uiState) runConnectionAttempt(status, startDiag, failStatus, failDiag string, showDialog bool, dialogTitle string, fn func() error) {
	s.connectMu.Lock()
	if s.connecting || s.closing {
		s.connectMu.Unlock()
		return
	}
	s.connecting = true
	s.connectMu.Unlock()
	s.statusLabel.SetText(status)
	s.appendDiag(startDiag)
	s.reconnectBtn.Disable()
	s.updateDiagnostics()

	go func() {
		defer func() {
			s.connectMu.Lock()
			s.connecting = false
			s.connectMu.Unlock()
			s.reconnectBtn.Enable()
		}()

		s.connectMu.Lock()
		closing := s.closing
		s.connectMu.Unlock()
		if closing {
			return
		}

		if err := fn(); err != nil {
			s.connected = false
			s.finishConnectionFailure(failStatus, failDiag, err, showDialog, dialogTitle)
			s.updateDiagnostics()
		}
	}()
}

func (s *uiState) stopStreaming() {
	s.streamMu.Lock()
	stop := s.streamStop
	stream := s.stream
	s.streamStop = nil
	s.stream = nil
	s.streamMu.Unlock()

	if stop != nil {
		close(stop)
	}
	if stream != nil {
		_ = stream.Close()
	}
}

func (s *uiState) streamLoop(stream *camera.Stream, stop <-chan struct{}) {
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
			s.statusLabel.SetText(fmt.Sprintf("Stream error: %v", err))
			s.appendDiag(fmt.Sprintf("stream error: %v", err))
			s.updateDiagnostics()
			continue
		}

		s.frameMu.Lock()
		s.latestFrame = frame
		s.frameMu.Unlock()
		s.refreshPreview()
		s.updateDiagnostics()
	}
}

func (s *uiState) refreshPreview() {
	s.frameMu.RLock()
	frame := s.latestFrame
	s.frameMu.RUnlock()
	if frame == nil || frame.Image == nil {
		return
	}
	img := imaging.ApplyTransforms(frame.Image, s.rotations, s.crosshair)
	s.preview.Image = img
	s.preview.Refresh()
	if s.connected {
		s.statusLabel.SetText(fmt.Sprintf("Live stream active. %0.1f FPS", s.stream.FPS()))
	}
}

func (s *uiState) saveSnapshot() {
	s.frameMu.RLock()
	frame := s.latestFrame
	s.frameMu.RUnlock()
	if frame == nil {
		dialog.ShowInformation("No Frame Yet", "Wait for the first live frame before saving a snapshot.", s.win)
		return
	}
	if err := os.MkdirAll(s.saveDir, 0o755); err != nil {
		s.showCopyableError("Create Capture Folder Failed", err)
		return
	}
	name := fmt.Sprintf("microview-%s.jpg", time.Now().Format("20060102-150405"))
	path := filepath.Join(s.saveDir, name)
	if err := os.WriteFile(path, frame.JPEG, 0o644); err != nil {
		s.showCopyableError("Save Snapshot Failed", err)
		return
	}
	c := capture{name: name, path: path, img: imaging.ApplyTransforms(frame.Image, s.rotations, s.crosshair), stamp: time.Now()}
	s.recent = append([]capture{c}, s.recent...)
	if len(s.recent) > maxRecentFrames {
		s.recent = s.recent[:maxRecentFrames]
	}
	s.refreshRecentGrid()
	s.statusLabel.SetText(fmt.Sprintf("Saved snapshot to %s", path))
	s.appendDiag(fmt.Sprintf("snapshot saved: %s", name))
	s.updateDiagnostics()
}

func (s *uiState) refreshRecentGrid() {
	s.recentGrid.Objects = nil
	if len(s.recent) == 0 {
		s.recentGrid.Add(widget.NewLabel("No captures yet"))
		s.recentGrid.Refresh()
		return
	}
	for _, item := range s.recent {
		thumb := canvas.NewImageFromImage(imaging.Scale(item.img, 150, 112))
		thumb.FillMode = canvas.ImageFillContain
		thumb.SetMinSize(fyne.NewSize(96, 72))
		openBtn := widget.NewButton(item.name, func(path string) func() {
			return func() {
				u, err := url.Parse("file://" + path)
				if err == nil {
					_ = fyne.CurrentApp().OpenURL(u)
				}
			}
		}(item.path))
		card := widget.NewCard(item.stamp.Format("15:04:05"), filepath.Base(item.path), container.NewBorder(nil, openBtn, nil, nil, thumb))
		s.recentGrid.Add(card)
	}
	s.recentGrid.Refresh()
}

func (s *uiState) chooseFolder() {
	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			s.showCopyableError("Choose Folder Failed", err)
			return
		}
		if uri == nil {
			return
		}
		s.saveDir = uri.Path()
		s.folderLabel.SetText(s.saveDir)
		s.app.Preferences().SetString("saveDir", s.saveDir)
		s.appendDiag("capture folder changed")
		s.updateDiagnostics()
	}, s.win)
}

func (s *uiState) reconnect() {
	s.streamMu.Lock()
	stream := s.stream
	s.streamMu.Unlock()
	s.runConnectionAttempt(
		"Reconnecting to microscope...",
		"reconnect started",
		"Reconnect failed",
		"reconnect failed",
		true,
		"Reconnect Failed",
		func() error {
			if stream == nil {
				return s.connectAndStream("Reconnected successfully. Streaming live video.", "reconnected")
			}
			if err := stream.Reconnect(); err != nil {
				return err
			}
			s.finishConnectionSuccess(stream, "Reconnected successfully. Streaming live video.", "reconnected")
			s.updateDiagnostics()
			return nil
		},
	)
}

func (s *uiState) finishConnectionFailure(statusPrefix, diagPrefix string, err error, showDialog bool, dialogTitle string) {
	s.statusLabel.SetText(fmt.Sprintf("%s: %v", statusPrefix, err))
	s.deviceLabel.SetText("Device: not connected")
	s.appendDiag(fmt.Sprintf("%s: %v", diagPrefix, err))
	if showDialog {
		s.showCopyableError(dialogTitle, err)
	}
}

func (s *uiState) finishConnectionSuccess(stream *camera.Stream, status, diag string) {
	s.connected = true
	info := stream.DeviceInfo()
	s.deviceLabel.SetText(fmt.Sprintf("Device: %s %s (%s)\nUSB: %s:%s", safe(info.Manufacturer, "Unknown"), safe(info.Product, "supercamera"), safe(info.Serial, "no serial"), info.VendorID, info.ProductID))
	s.statusLabel.SetText(status)
	s.appendDiag(diag)
	if s.reconnectBtn.Disabled() {
		s.reconnectBtn.Enable()
	}
}

func (s *uiState) attachStream(stream *camera.Stream) error {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streamStop != nil || s.stream != nil {
		return fmt.Errorf("stream already active")
	}
	stop := make(chan struct{})
	s.stream = stream
	s.streamStop = stop
	return nil
}

func (s *uiState) appendDiag(msg string) {
	stamp := time.Now().Format("15:04:05")
	s.diagLog = append([]string{fmt.Sprintf("[%s] %s", stamp, msg)}, s.diagLog...)
	if len(s.diagLog) > 8 {
		s.diagLog = s.diagLog[:8]
	}
}

func (s *uiState) showCopyableError(title string, err error) {
	msg := err.Error()
	entry := widget.NewMultiLineEntry()
	entry.SetText(msg)
	entry.Wrapping = fyne.TextWrapWord
	entry.SetMinRowsVisible(4)
	copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		s.app.Clipboard().SetContent(msg)
	})
	content := container.NewBorder(nil, copyBtn, nil, nil, entry)
	d := dialog.NewCustom(title, "Close", content, s.win)
	d.Resize(fyne.NewSize(720, 220))
	d.Show()
}

func (s *uiState) updateDiagnostics() {
	stream := s.stream
	if stream == nil {
		s.diagnosticsEntry.SetText("No active stream")
		return
	}
	stats := stream.Stats()
	info := stream.DeviceInfo()
	text := fmt.Sprintf(
		"Manufacturer: %s\nProduct: %s\nSerial: %s\nResolution: 640x480\nFrames: %d\nFPS: %0.1f\nUSB Errors: %d\nBad Frames: %d\nReconnects: %d\n\nRecent Events:\n%s",
		safe(info.Manufacturer, "Unknown"),
		safe(info.Product, "supercamera"),
		safe(info.Serial, "unavailable"),
		stats.Frames,
		stream.FPS(),
		stats.USBErrors,
		stats.BadFrames,
		stats.Reconnects,
		joinLines(s.diagLog),
	)
	s.diagnosticsEntry.SetText(text)
}

func (s *uiState) runDebugAction(title, running string, requiresExclusiveStream bool, fn func() (string, error)) {
	output := widget.NewMultiLineEntry()
	output.Wrapping = fyne.TextWrapWord
	output.Disable()
	output.SetMinRowsVisible(20)
	output.SetText(running)

	copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		s.app.Clipboard().SetContent(output.Text)
	})
	closeBtn := widget.NewButton("Close", nil)
	content := container.NewBorder(nil, container.NewHBox(copyBtn, layout.NewSpacer(), closeBtn), nil, nil, output)
	dbg := s.app.NewWindow(title)
	dbg.Resize(fyne.NewSize(860, 520))
	dbg.SetContent(content)
	closeBtn.OnTapped = func() {
		dbg.Close()
	}
	dbg.Show()

	go func() {
		s.statusLabel.SetText("Running debug diagnostics...")
		s.appendDiag(strings.ToLower(title) + " started")

		wasConnected := false
		if requiresExclusiveStream {
			s.streamMu.Lock()
			wasConnected = s.stream != nil
			s.streamMu.Unlock()
			if wasConnected {
				s.stopStreaming()
			}
		}

		result, err := fn()
		if err != nil {
			result = strings.TrimSuffix(result, "\n")
			if result != "" {
				result += "\n"
			}
			result += fmt.Sprintf("panic: %v", err)
			s.appendDiag(strings.ToLower(title) + " failed")
		} else {
			s.appendDiag(strings.ToLower(title) + " completed")
		}
		if strings.TrimSpace(result) == "" {
			result = "No diagnostic output"
		}
		output.SetText(result)
		if requiresExclusiveStream && wasConnected {
			_ = s.connectAndStream("Connected. Streaming live video.", "stream connected")
		}
		s.updateDiagnostics()
	}()
}

func safe(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return "No events yet"
	}
	buf := bytes.Buffer{}
	for i, line := range lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	return buf.String()
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

func (s *uiState) buildMainMenu() *fyne.MainMenu {
	setTheme := func(mode string) func() {
		return func() {
			s.themeMode = mode
			s.app.Preferences().SetString("themeMode", mode)
			s.app.Settings().SetTheme(theming.ThemeForMode(mode))
			s.win.SetMainMenu(s.buildMainMenu())
		}
	}

	systemItem := fyne.NewMenuItem("System", setTheme(theming.ModeSystem))
	darkItem := fyne.NewMenuItem("Dark", setTheme(theming.ModeDark))
	lightItem := fyne.NewMenuItem("Light", setTheme(theming.ModeLight))
	systemItem.Checked = s.themeMode == theming.ModeSystem
	darkItem.Checked = s.themeMode == theming.ModeDark
	lightItem.Checked = s.themeMode == theming.ModeLight
	themeItem := fyne.NewMenuItem("Theme", nil)
	themeItem.ChildMenu = fyne.NewMenu("", systemItem, darkItem, lightItem)

	viewMenu := fyne.NewMenu("View",
		themeItem,
	)

	debugMenu := fyne.NewMenu("Debug",
		fyne.NewMenuItem("Packet Headers", func() {
			s.runDebugAction("Packet Headers", "Capturing packet headers...", true, func() (string, error) {
				return camera.PacketHeaderDump(20, 5*time.Second)
			})
		}),
		fyne.NewMenuItem("Probe Capture", func() {
			s.runDebugAction("Probe Capture", "Capturing probe frame...", true, func() (string, error) {
				return camera.ProbeCapture(5 * time.Second)
			})
		}),
		fyne.NewMenuItem("USB Descriptor Dump", func() {
			s.runDebugAction("USB Descriptor Dump", "Reading USB descriptors...", false, camera.DescriptorDump)
		}),
	)

	helpMenu := fyne.NewMenu("Help",
		fyne.NewMenuItem("About MicroView", func() {
			dialog.ShowInformation("About MicroView", "MicroView\n\nCompact microscope viewer for the Geek szitman / supercamera family.", s.win)
		}),
	)

	return fyne.NewMainMenu(viewMenu, debugMenu, helpMenu)
}
