package chromesweep

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
)

// WriteBundle writes a zip archive to path containing report.json, report.md, and
// every result's captured frame under frames/. Results without a captured frame
// are skipped.
func WriteBundle(path string, reportJSON, reportMarkdown []byte, results []Result) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	zw := zip.NewWriter(f)
	if err := addZipFile(zw, "report.json", reportJSON); err != nil {
		return err
	}
	if err := addZipFile(zw, "report.md", reportMarkdown); err != nil {
		return err
	}
	for _, r := range results {
		if r.FramePath == "" {
			continue
		}
		data, rerr := os.ReadFile(r.FramePath)
		if rerr != nil {
			return fmt.Errorf("reading frame %s: %w", r.FramePath, rerr)
		}
		if err := addZipFile(zw, "frames/"+filepath.Base(r.FramePath), data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// addZipFile writes one in-memory file into the zip.
func addZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
