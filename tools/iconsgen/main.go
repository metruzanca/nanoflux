// Command iconsgen renders the nanoflux PWA icons: a dark tile with the "η"
// brand mark in the app accent color, written as PNGs into the embedded static
// dir. Regenerate with `make icons` (or `go run ./tools/iconsgen`).
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

// drawIcon renders a size x size tile with the η mark centered, sized to fit
// the maskable safe zone (content inside the central ~66%).
func drawIcon(size int) image.Image {
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
	face, err := opentype.NewFace(fontBytes, &opentype.FaceOptions{
		Size:    float64(size) * 0.5,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic(err)
	}
	defer face.Close()

	d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face}
	width := d.MeasureString("η")
	metrics := face.Metrics()
	// Center the glyph box on the tile (baseline adjusted by half the line
	// height so the visible mark sits in the middle, not above it).
	x := (fixed.I(size) - width) / 2
	y := fixed.I(size)/2 + (metrics.Ascent - metrics.Height/2)
	d.Dot = fixed.Point26_6{X: x, Y: y}
	d.DrawString("η")
	return img
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func main() {
	outDir := "internal/web/static"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sizes := []struct {
		name string
		size int
	}{
		{"pwa-192.png", 192},
		{"pwa-512.png", 512},
		{"apple-touch-icon.png", 180},
	}
	for _, s := range sizes {
		path := filepath.Join(outDir, s.name)
		if err := writePNG(path, drawIcon(s.size)); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", s.name, err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}
