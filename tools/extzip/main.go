// Command extzip writes the embedded browser extension to
// dist/nanoflux-extension.zip for load-unpacked distribution. It reuses the
// same embedded file set the server serves from /settings/extension.zip.
package main

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"

	ext "github.com/metruzanca/nanoflux/extension"
)

func main() {
	if err := os.MkdirAll("dist", 0o755); err != nil {
		panic(err)
	}
	const out = "dist/nanoflux-extension.zip"
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	err = fs.WalkDir(ext.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := ext.FS.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(path)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	fmt.Println("wrote", out)
}
