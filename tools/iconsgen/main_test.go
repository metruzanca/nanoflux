package main

import (
	"image/color"
	"testing"
)

// TestDrawIconRendersGlyph asserts the accent mark actually rasterizes (rather
// than emitting a flat tile — the bug a missing DPI caused in NewFace) and that
// it is roughly centered and sized at the requested fraction.
func TestDrawIconRendersGlyph(t *testing.T) {
	for _, size := range []int{16, 32, 48, 128, 192, 512} {
		img := drawIcon(size, 0.5)
		b := img.Bounds()
		ink := 0
		minX, minY := b.Max.X, b.Max.Y
		maxX, maxY := b.Min.X, b.Min.Y
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				c := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)}
				if c != bg {
					ink++
					if x < minX {
						minX = x
					}
					if y < minY {
						minY = y
					}
					if x > maxX {
						maxX = x
					}
					if y > maxY {
						maxY = y
					}
				}
			}
		}
		if ink == 0 {
			t.Fatalf("size %d: icon is a flat tile (glyph did not render)", size)
		}
		frac := float64(maxY-minY+1) / float64(b.Dy())
		if frac < 0.3 || frac > 0.75 {
			t.Errorf("size %d: ink height fraction = %.2f, want ~0.5", size, frac)
		}
		cx := float64(minX+maxX) / 2 / float64(b.Dx())
		cy := float64(minY+maxY) / 2 / float64(b.Dy())
		if cx < 0.4 || cx > 0.6 || cy < 0.4 || cy > 0.6 {
			t.Errorf("size %d: ink center = (%.2f, %.2f), want centered", size, cx, cy)
		}
	}
}
