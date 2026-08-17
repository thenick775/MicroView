// Package imaging contains small image helpers for preview transforms, overlays,
// and thumbnails used by the MicroView UI.
package imaging

import (
	"image"
	"image/color"
	"image/draw"
)

// ApplyTransforms clones src, applies quarter-turn rotations, and optionally
// draws the crosshair overlay.
func ApplyTransforms(src image.Image, rotations int, crosshair bool) image.Image {
	// rotate in 90 degree steps before drawing overlays so the crosshair stays centered
	// in the final orientation shown to the user.
	img := cloneToRGBA(src)
	for range rotations % 4 {
		img = rotate90(img)
	}
	if crosshair {
		drawCrosshair(img)
	}
	return img
}

func cloneToRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func rotate90(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dy()-1-y, x, src.At(x, y))
		}
	}
	return dst
}

func drawCrosshair(img *image.RGBA) {
	b := img.Bounds()
	cx, cy := b.Dx()/2, b.Dy()/2
	clr := color.RGBA{R: 0, G: 255, B: 120, A: 255}
	// draw a solid center band and spaced tick marks farther from the center so the
	// overlay remains visible without obscuring too much of the image.
	for x := 0; x < b.Dx(); x++ {
		if abs(x-cx) < 20 || x%6 == 0 {
			img.Set(x, cy, clr)
		}
	}
	for y := 0; y < b.Dy(); y++ {
		if abs(y-cy) < 20 || y%6 == 0 {
			img.Set(cx, y, clr)
		}
	}
	for dx := -8; dx <= 8; dx++ {
		img.Set(cx+dx, cy, color.White)
	}
	for dy := -8; dy <= 8; dy++ {
		img.Set(cx, cy+dy, color.White)
	}
}

// Scale returns a nearest-neighbor thumbnail that fits within maxW by maxH
// while preserving aspect ratio.
func Scale(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return src
	}
	// preserve aspect ratio by choosing the tighter width or height scale factor,
	// then sample source pixels with nearest-neighbor math for lightweight thumbnails.
	scaleW := float64(maxW) / float64(w)
	scaleH := float64(maxH) / float64(h)
	scale := scaleW
	if scaleH < scale {
		scale = scaleH
	}
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			sy := b.Min.Y + y*h/nh
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// Blank returns a solid placeholder image of the requested size.
func Blank(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 17, G: 24, B: 39, A: 255}}, image.Point{}, draw.Src)
	return img
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
