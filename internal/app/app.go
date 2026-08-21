package app

import (
	"bytes"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	app       fyne.App
	win       fyne.Window
	session   *session
	recent    []capture
	rotations int
	crosshair bool
	saveDir   string
	diagLog   []string
	themeMode string

	preview          *canvas.Image
	statusLabel      *widget.Label
	diagnosticsLabel *widget.Label
	deviceLabel      *widget.Label
	folderLabel      *widget.Label
	recentGrid       *fyne.Container
	crosshairCheck   *widget.Check
	rotateLabel      *widget.Label
	snapshotBtn      *widget.Button
	reconnectBtn     *widget.Button
}

// Run constructs the Fyne UI, wires it to a microscope session, and starts
// the desktop app event loop.
func Run() {
	app := app.NewWithID(appID)
	themeMode := app.Preferences().StringWithFallback("themeMode", theming.ModeDark)
	app.Settings().SetTheme(theming.ThemeForMode(themeMode))
	window := app.NewWindow("MicroView")
	window.Resize(fyne.NewSize(1320, 860))

	state := &uiState{
		app:       app,
		win:       window,
		saveDir:   expandHome(defaultSaveDir),
		crosshair: true,
		diagLog:   []string{"Ready"},
		themeMode: themeMode,
	}
	if saved := app.Preferences().StringWithFallback("saveDir", ""); saved != "" {
		state.saveDir = saved
	}
	state.session = newSession(sessionHooks{
		setStatus: func(text string) {
			state.statusLabel.SetText(text)
		},
		setDevice: func(text string) {
			state.deviceLabel.SetText(text)
		},
		setReconnectEnabled: func(enabled bool) {
			if enabled {
				state.reconnectBtn.Enable()
				return
			}
			state.reconnectBtn.Disable()
		},
		addEvent:          state.appendDiag,
		updateDiagnostics: state.updateDiagnostics,
		refreshPreview:    state.refreshPreview,
	})

	window.SetMainMenu(state.buildMainMenu())
	state.buildUI()
	state.win.SetCloseIntercept(func() {
		state.session.Stop()
		state.win.Close()
	})
	state.win.Show()
	state.session.Start()
	app.Run()
}

func (s *uiState) buildUI() {
	s.preview = canvas.NewImageFromImage(imaging.Blank(640, 480))
	s.preview.FillMode = canvas.ImageFillContain
	s.preview.SetMinSize(fyne.NewSize(320, 240))

	s.statusLabel = widget.NewLabel("Connecting to microscope...")
	s.statusLabel.Wrapping = fyne.TextWrapWord
	s.diagnosticsLabel = widget.NewLabel("No active stream")
	s.diagnosticsLabel.Wrapping = fyne.TextWrapWord
	s.deviceLabel = widget.NewLabel("Device: not connected")
	s.deviceLabel.Wrapping = fyne.TextWrapWord
	s.folderLabel = widget.NewLabel(s.saveDir)
	s.folderLabel.Wrapping = fyne.TextWrapWord
	s.rotateLabel = widget.NewLabel("Rotation: 0°")

	s.crosshairCheck = widget.NewCheck("Crosshair", func(v bool) {
		s.crosshair = v
		s.refreshPreview()
	})
	s.crosshairCheck.SetChecked(s.crosshair)

	s.snapshotBtn = widget.NewButtonWithIcon("Snapshot", theme.DocumentSaveIcon(), s.saveSnapshot)
	s.reconnectBtn = widget.NewButtonWithIcon("Reconnect", theme.ViewRefreshIcon(), s.reconnect)

	rotateBtn := widget.NewButtonWithIcon("Rotate", theme.ViewRefreshIcon(), func() {
		s.rotations = (s.rotations + 1) % 4
		s.rotateLabel.SetText(fmt.Sprintf("Rotation: %d°", s.rotations*90))
		s.refreshPreview()
	})
	folderBtn := widget.NewButtonWithIcon("Choose Folder", theme.FolderOpenIcon(), s.chooseFolder)

	leftTop := container.NewBorder(nil, container.NewVBox(
		widget.NewSeparator(),
		container.NewGridWithColumns(4,
			s.snapshotBtn,
			rotateBtn,
			s.reconnectBtn,
			folderBtn,
		),
		container.NewHBox(s.crosshairCheck, layout.NewSpacer(), s.rotateLabel),
	), nil, nil, s.preview)

	s.recentGrid = container.NewGridWithColumns(2)
	sessionContent := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabel("Capture folder and current device"),
		widget.NewSeparator(),
		widget.NewLabel("Capture Folder"),
		s.folderLabel,
		widget.NewSeparator(),
		s.deviceLabel,
		widget.NewSeparator(),
		s.statusLabel,
	)))
	diagnosticsContent := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabel("Device and stream status"),
		widget.NewSeparator(),
		s.diagnosticsLabel,
	)))
	capturesContent := container.NewVScroll(container.NewPadded(container.NewVBox(
		widget.NewLabel("Latest snapshots"),
		widget.NewSeparator(),
		s.recentGrid,
	)))

	tabs := container.NewAppTabs(
		container.NewTabItem("Session", sessionContent),
		container.NewTabItem("Diagnostics", diagnosticsContent),
		container.NewTabItem("Captures", capturesContent),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	content := container.NewHSplit(leftTop, tabs)
	content.SetOffset(0.82)

	s.win.SetContent(content)
	s.refreshRecentGrid()
	s.updateDiagnostics()
}

func (s *uiState) refreshPreview() {
	frame := s.session.LatestFrame()
	if frame == nil || frame.Image == nil {
		return
	}
	img := imaging.ApplyTransforms(frame.Image, s.rotations, s.crosshair)
	s.preview.Image = img
	s.preview.Refresh()
	if snap := s.session.Snapshot(); snap.Connected {
		s.statusLabel.SetText(fmt.Sprintf("Live stream active. %0.1f FPS", snap.FPS))
	}
}

func (s *uiState) saveSnapshot() {
	frame := s.session.LatestFrame()
	if frame == nil {
		dialog.ShowInformation("No Frame Yet", "Wait for the first live frame before saving a snapshot.", s.win)
		return
	}
	if err := os.MkdirAll(s.saveDir, 0o755); err != nil {
		s.showErrorDialog("Create Capture Folder Failed", err)
		return
	}
	name := fmt.Sprintf("microview-%s.jpg", time.Now().Format("20060102-150405"))
	path := filepath.Join(s.saveDir, name)
	if err := os.WriteFile(path, frame.JPEG, 0o644); err != nil {
		s.showErrorDialog("Save Snapshot Failed", err)
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
		openBtn := widget.NewButton(item.name, func() {
			u, err := url.Parse("file://" + item.path)
			if err == nil {
				_ = fyne.CurrentApp().OpenURL(u)
			}
		})
		card := widget.NewCard(item.stamp.Format("15:04:05"), filepath.Base(item.path), container.NewBorder(nil, openBtn, nil, nil, thumb))
		s.recentGrid.Add(card)
	}
	s.recentGrid.Refresh()
}

func (s *uiState) chooseFolder() {
	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			s.showErrorDialog("Choose Folder Failed", err)
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
	s.session.Reconnect()
}

func (s *uiState) appendDiag(msg string) {
	stamp := time.Now().Format("15:04:05")
	s.diagLog = append([]string{fmt.Sprintf("[%s] %s", stamp, msg)}, s.diagLog...)
	if len(s.diagLog) > 8 {
		s.diagLog = s.diagLog[:8]
	}
}

func (s *uiState) showErrorDialog(title string, err error) {
	label := widget.NewLabel(err.Error())
	label.Wrapping = fyne.TextWrapWord
	content := container.NewVScroll(label)
	d := dialog.NewCustom(title, "Close", content, s.win)
	d.Resize(fyne.NewSize(720, 220))
	d.Show()
}

func (s *uiState) updateDiagnostics() {
	snap := s.session.Snapshot()
	if !snap.Connected {
		s.diagnosticsLabel.SetText("No active stream")
		return
	}
	text := fmt.Sprintf(
		"Manufacturer: %s\nProduct: %s\nSerial: %s\nResolution: 640x480\n\nFrames: %d\nFPS: %0.1f\nUSB Errors: %d\nBad Frames: %d\nReconnects: %d\n\nRecent Events:\n%s",
		safe(snap.Info.Manufacturer, "Unknown"),
		safe(snap.Info.Product, "supercamera"),
		safe(snap.Info.Serial, "unavailable"),
		snap.Stats.Frames,
		snap.FPS,
		snap.Stats.USBErrors,
		snap.Stats.BadFrames,
		snap.Stats.Reconnects,
		joinLines(s.diagLog),
	)
	s.diagnosticsLabel.SetText(text)
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
	closeBtn.OnTapped = dbg.Close
	dbg.Show()

	s.session.RunDiagnostic(title, requiresExclusiveStream, fn, func(result string, err error) {
		if err != nil {
			result = strings.TrimSuffix(result, "\n")
			if result != "" {
				result += "\n"
			}
			result += fmt.Sprintf("error: %v", err)
		}
		if strings.TrimSpace(result) == "" {
			result = "No diagnostic output"
		}
		output.SetText(result)
	})
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
