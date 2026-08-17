# MicroView

<div>
  <a href="https://github.com/thenick775/MicroView/actions/workflows/build-macos.yml">
    <img alt="GitHub Actions Workflow Status" src="https://img.shields.io/github/actions/workflow/status/thenick775/MicroView/build-macos.yml">
  </a>
</div>

Compact macOS microscope viewer built with Go and Fyne for the Geek szitman / supercamera / USeePlus USB microscope family.

## Features

- Live microscope preview
- Snapshot capture
- Capture folder picker
- Recent captures gallery
- Rotation in 90 degree steps
- Optional crosshair overlay
- Reconnect button
- Fullscreen toggle
- Device and stream diagnostics

## Hardware

Test target:

- USB VID:PID `2ce3:3828`
- USB VID:PID `0329:2022`
- KEEMIKA model `321P`
- Amazon ASIN `B0C7S6ZNYT`

This is a non-UVC microscope. It does not work like a standard webcam. This app talks to the device over its proprietary packet protocol.

Reference product listing:

- Amazon: <https://www.amazon.com/KEEMIKA-Microscope-50x-1600x-Magnification-Compatible/dp/B0C7S6ZNYT>

Where/why i got the product above, I sharpen knives and found the product here:

- [CBRx YouTube channel](https://www.youtube.com/@CBRxLIVE)

## Run

```bash
go run .
```

Launch the packaged app bundle:

```bash
open MicroView.app
```

## Build

```bash
go build -o MicroView
```

To package as a Fyne app bundle on macOS:

```bash
go install fyne.io/tools/cmd/fyne@latest
~/go/bin/fyne package -os darwin -name MicroView
```

## Acknowledgements

Protocol understanding and interoperability work were informed by:

- `hbens/geek-szitman-supercamera`
- `MAkcanca/useeplus-linux-driver`
- `jmz3/EndoscopeCamera`
- `echase/ProbeView`

MicroView is not affiliated with or endorsed by the microscope vendor.

## Notes

- Built with Go `1.26.6`
- Built against `fyne.io/fyne/v2 v2.7.0`
