package httpapi

import (
	"archive/zip"
	"io/fs"
	"net/http"

	"github.com/charmbracelet/log"

	ext "github.com/metruzanca/nanoflux/extension"
)

// settingsExtensionZip streams the embedded browser extension as a zip so any
// signed-in user can download and load it unpacked.
func (s *Server) settingsExtensionZip(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="nanoflux-extension.zip"`)
	zw := zip.NewWriter(w)
	err := fs.WalkDir(ext.FS, ".", func(path string, d fs.DirEntry, err error) error {
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
		f, err := zw.Create(path)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err != nil {
		log.Error("zip extension", "err", err)
	}
	if err := zw.Close(); err != nil {
		log.Error("close extension zip", "err", err)
	}
}
