// Command iconsgen renders the nanoflux brand icons: a dark tile with the "η"
// mark in the app accent color. It writes the PWA icons into the embedded
// static dir and the browser-extension icons into extension/icons.
// Regenerate with `make icons` (or `go run ./tools/iconsgen`).
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	bg = color.RGBA{R: 0x11, G: 0x13, B: 0x18, A: 0xff} // --bg (#111318)
	fg = color.RGBA{R: 0x5b, G: 0x8c, B: 0xff, A: 0xff} // --accent (#5b8cff)
)

// drawIcon renders a size x size tile with the η mark centered, its ink box
// occupying glyphFrac of the tile. DPI must be set: opentype.NewFace derives
// its scale from Size*DPI*64/72, so a zero DPI yields a zero scale and an empty
// glyph. The ink is measured at a reference size and the face is then scaled so
// the glyph occupies exactly glyphFrac of the tile, centered on its ink bounds.
func drawIcon(size int, glyphFrac float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, bg)
		}
	}

	fontBytes, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic(err)
	}
	// Measure the glyph's ink at a reference face size (equal to the tile), then
	// derive the face size that makes the ink glyphFrac of the tile. Ink scales
	// linearly with the face size, so the ratio is exact.
	ref, err := opentype.NewFace(fontBytes, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	refInk, _, _, _, ok := ref.Glyph(fixed.Point26_6{}, 'η')
	ref.Close()
	if !ok || refInk.Dy() == 0 {
		panic("η glyph unavailable")
	}
	face, err := opentype.NewFace(fontBytes, &opentype.FaceOptions{
		Size:    float64(size) * glyphFrac * float64(size) / float64(refInk.Dy()),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic(err)
	}
	defer face.Close()

	ink, _, _, _, ok := face.Glyph(fixed.Point26_6{}, 'η')
	if !ok {
		panic("η glyph unavailable")
	}
	// ink is an integer-pixel bounding box relative to the glyph origin; shift
	// the dot so the ink box is centered on the tile.
	cx := (ink.Min.X + ink.Max.X) / 2
	cy := (ink.Min.Y + ink.Max.Y) / 2
	d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face}
	d.Dot = fixed.Point26_6{X: fixed.I(size/2 - cx), Y: fixed.I(size/2 - cy)}
	d.DrawString("η")
	return img
}

// render writes one icon, creating the output directory if needed.
func render(dir, name string, size int, glyphFrac float64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, drawIcon(size, glyphFrac)); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func main() {
	pwaDir := "internal/web/static"
	extDir := "extension/icons"
	if len(os.Args) > 1 {
		pwaDir = os.Args[1]
	}
	if len(os.Args) > 2 {
		extDir = os.Args[2]
	}

	// PWA icons are maskable: keep the mark inside the central safe zone.
	pwa := []struct {
		name string
		size int
	}{
		{"pwa-192.png", 192},
		{"pwa-512.png", 512},
		{"apple-touch-icon.png", 180},
	}
	for _, s := range pwa {
		if err := render(pwaDir, s.name, s.size, 0.5); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", s.name, err)
			os.Exit(1)
		}
	}

	// Browser-extension toolbar icons: fill more of the canvas so the mark
	// stays legible when Chrome draws them small.
	ext := []struct {
		name string
		size int
	}{
		{"icon16.png", 16},
		{"icon32.png", 32},
		{"icon48.png", 48},
		{"icon128.png", 128},
	}
	for _, s := range ext {
		if err := render(extDir, s.name, s.size, 0.78); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", s.name, err)
			os.Exit(1)
		}
	}
}
