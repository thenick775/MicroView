# MicroView

<div>
  <a href="https://github.com/thenick775/MicroView/releases">
    <img alt="GitHub Release" src="https://img.shields.io/github/v/release/thenick775/MicroView">
  </a>
  <a href="https://github.com/thenick775/MicroView/actions/workflows/build-macos.yml">
    <img alt="GitHub Actions Workflow Status" src="https://img.shields.io/github/actions/workflow/status/thenick775/MicroView/build-macos.yml">
  </a>
</div>

![MicroView App Desktop](./readme-images/microview-demo.gif)

A macOS microscope viewer app built with Go and Fyne for the Geek szitman / supercamera / USeePlus USB microscope family.

I built this because I purchased a microscope for use with a fixed angle sharpener. Sadly, all the hardware I had wasn't able to get it working on older devices with lightening ports, so here we are making it work on desktop! This app has functionality similar to the `sup-anesok` app, but built specifically for desktop and free/open source forever.

## Features

- Live microscope preview
- Snapshot capture
- Capture folder config
- Recent captures gallery
- Rotation in 90 degree steps
- Optional crosshair overlay
- Reconnect flow
- Device and stream diagnostics

## Hardware

Test target:

- USB VID:PID `2ce3:3828`
- USB VID:PID `0329:2022`
- KEEMIKA model `321P`
- Amazon ASIN `B0C7S6ZNYT`

This is a non-UVC microscope. It does not work like a standard webcam, and thus cannot be recognized as a typical camera device. This app talks to the device over its proprietary packet protocol.

Reference product listing:

- Amazon: <https://www.amazon.com/KEEMIKA-Microscope-50x-1600x-Magnification-Compatible/dp/B0C7S6ZNYT>

Where/why I got the product above, I sharpen knives and found the product above recommended here:

- [CBRx YouTube channel](https://www.youtube.com/@CBRxLIVE)

## Getting Started

### Run

```bash
go run .
```

### Build

```bash
go build -o MicroView
```

### Package

To package as a Fyne app bundle on macOS:

```bash
go install fyne.io/tools/cmd/fyne@latest
~/go/bin/fyne package -os darwin -name MicroView -release
```

Double click the built app or launch the packaged app bundle with:

```bash
open MicroView.app
```

## macOS App Download

To open `MicroView.app` from a downloaded release or actions artifact:

1. Unzip the downloaded artifact
2. Move `MicroView.app` somewhere convenient, like `Applications` or `Desktop`
3. Try opening `MicroView.app`
4. If macOS blocks it, open `System Settings > Privacy & Security`
5. Scroll down and click `Open Anyway` for `MicroView.app`

## Acknowledgements

Protocol understanding and interoperability work were informed by:

- [`hbens/geek-szitman-supercamera`](https://github.com/hbens/geek-szitman-supercamera)
- [`MAkcanca/useeplus-linux-driver`](https://github.com/MAkcanca/useeplus-linux-driver)
- [`jmz3/EndoscopeCamera`](https://github.com/jmz3/EndoscopeCamera)
- [`echase/ProbeView`](https://github.com/echase/ProbeView)
