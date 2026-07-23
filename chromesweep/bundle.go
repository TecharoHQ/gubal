package chromesweep

import (
	"archive/zip"
	"fmt"
	"os"
)

// WriteBundle writes a zip archive to path containing report.json, report.md,
// every result's captured frame under frames/<policy>/, and every result's
// captured container logs under logs/<policy>/. The <policy> segment is omitted
// when a result has no policy (Anubis's live ruleset). Results without a
// captured frame (or logs) skip those entries.
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
		if r.FramePath != "" {
			data, rerr := os.ReadFile(r.FramePath)
			if rerr != nil {
				return fmt.Errorf("reading frame %s: %w", r.FramePath, rerr)
			}
			if err := addZipFile(zw, r.BundleFramePath(), data); err != nil {
				return err
			}
		}
		for _, lg := range r.Logs {
			if lg.Content == "" {
				continue
			}
			if err := addZipFile(zw, r.BundleLogPath(lg.Container), []byte(lg.Content)); err != nil {
				return err
			}
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

// BundleFramePath returns the frame's path inside the bundle WriteBundle
// produces, or "" when no frame was captured. Reports link to this rather than
// FramePath, which points into a scratch dir that does not outlive the sweep.
func (r Result) BundleFramePath() string {
	if r.FramePath == "" {
		return ""
	}
	return bundlePath("frames", r.Policy, fmt.Sprintf("%s-%s.png", r.Browser, r.Tag))
}

// BundleLogPath returns the given container log's path inside the bundle.
func (r Result) BundleLogPath(container string) string {
	return bundlePath("logs", r.Policy, fmt.Sprintf("%s-%s-%s.log", r.Browser, r.Tag, container))
}

// bundlePath joins a bundle top-level dir, an optional policy subfolder, and a
// leaf name. Policy is what keeps the same browser+tag from colliding across
// passes; an empty policy (Anubis's live ruleset) omits the subfolder. Always
// forward slashes: these are zip entry names, not OS paths.
func bundlePath(kind, policy, leaf string) string {
	if policy == "" {
		return kind + "/" + leaf
	}
	return kind + "/" + policy + "/" + leaf
}
