package chromesweep

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBundle(t *testing.T) {
	dir := t.TempDir()
	f150 := filepath.Join(dir, "150.png")
	if err := os.WriteFile(f150, []byte("PNG-150"), 0o644); err != nil {
		t.Fatal(err)
	}
	f120 := filepath.Join(dir, "120.png")
	if err := os.WriteFile(f120, []byte("PNG-120"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []Result{
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusPass, FramePath: f150},
		{Policy: "fast", Browser: "chrome", Tag: "120", Status: StatusPass, FramePath: f120},
		{Policy: "fast", Browser: "chrome", Tag: "999", Status: StatusFail}, // no frame — must be skipped
	}
	reportJSON := []byte(`{"results":[]}`)
	reportMarkdown := []byte("# Chrome version sweep — 2/3 passed\n")
	zipPath := filepath.Join(dir, "report.zip")
	if err := WriteBundle(zipPath, reportJSON, reportMarkdown, results); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := b.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		got[f.Name] = b.Bytes()
	}

	if !bytes.Equal(got["report.json"], reportJSON) {
		t.Fatalf("report.json = %q, want %q", got["report.json"], reportJSON)
	}
	if !bytes.Equal(got["report.md"], reportMarkdown) {
		t.Fatalf("report.md = %q, want %q", got["report.md"], reportMarkdown)
	}
	if !bytes.Equal(got["frames/fast/chrome-150.png"], []byte("PNG-150")) {
		t.Fatalf("frames/fast/chrome-150.png = %q", got["frames/fast/chrome-150.png"])
	}
	if !bytes.Equal(got["frames/fast/chrome-120.png"], []byte("PNG-120")) {
		t.Fatalf("frames/fast/chrome-120.png = %q", got["frames/fast/chrome-120.png"])
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 entries (report.json + report.md + 2 frames), got %d", len(got))
	}
}

func TestWriteBundleIncludesLogs(t *testing.T) {
	dir := t.TempDir()
	frame := filepath.Join(dir, "firefox-152.png")
	if err := os.WriteFile(frame, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []Result{
		{
			Policy:    "fast",
			Browser:   "firefox",
			Tag:       "152",
			Status:    StatusFail,
			FramePath: frame,
			Logs: []LogCapture{
				{Container: "firefox", Content: "bidi client closed"},
				{Container: "chrome-bully", Content: "fatal: loading url"},
				{Container: "smoke", Content: ""}, // empty — must be skipped
			},
		},
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusPass}, // no logs, no frame
	}
	zipPath := filepath.Join(dir, "report.zip")
	if err := WriteBundle(zipPath, []byte("{}"), []byte("# report\n"), results); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		got[f.Name] = buf.Bytes()
	}

	if !bytes.Equal(got["logs/fast/firefox-152-firefox.log"], []byte("bidi client closed")) {
		t.Fatalf("firefox log = %q", got["logs/fast/firefox-152-firefox.log"])
	}
	if !bytes.Equal(got["logs/fast/firefox-152-chrome-bully.log"], []byte("fatal: loading url")) {
		t.Fatalf("chrome-bully log = %q", got["logs/fast/firefox-152-chrome-bully.log"])
	}
	if _, ok := got["logs/fast/firefox-152-smoke.log"]; ok {
		t.Fatal("empty log content must be skipped")
	}
	// report.json + report.md + 1 frame + 2 logs = 5.
	if len(got) != 5 {
		t.Fatalf("expected 5 entries, got %d: %v", len(got), keys(got))
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestWriteBundleSeparatesPolicies is the reason bundles gained subfolders: the
// same browser+tag under two policies must not collide on one zip entry name.
func TestWriteBundleSeparatesPolicies(t *testing.T) {
	dir := t.TempDir()
	frame := filepath.Join(dir, "chrome-150.png")
	if err := os.WriteFile(frame, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []Result{
		{Policy: "default-config", Browser: "chrome", Tag: "150", Status: StatusFail, FramePath: frame,
			Logs: []LogCapture{{Container: "chrome-bully", Content: "under default-config"}}},
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusFail, FramePath: frame,
			Logs: []LogCapture{{Container: "chrome-bully", Content: "under fast"}}},
	}
	zipPath := filepath.Join(dir, "report.zip")
	if err := WriteBundle(zipPath, []byte("{}"), []byte("# report\n"), results); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if _, dup := got[f.Name]; dup {
			t.Fatalf("duplicate zip entry %q", f.Name)
		}
		got[f.Name] = buf.Bytes()
	}

	if !bytes.Equal(got["logs/default-config/chrome-150-chrome-bully.log"], []byte("under default-config")) {
		t.Fatalf("default-config log = %q", got["logs/default-config/chrome-150-chrome-bully.log"])
	}
	if !bytes.Equal(got["logs/fast/chrome-150-chrome-bully.log"], []byte("under fast")) {
		t.Fatalf("fast log = %q", got["logs/fast/chrome-150-chrome-bully.log"])
	}
}

func TestBundlePathsWithoutPolicy(t *testing.T) {
	// Anubis's live ruleset produces an empty Policy; the subfolder is omitted.
	r := Result{Browser: "chrome", Tag: "150", FramePath: "/tmp/x/chrome-150.png"}
	if got := r.BundleFramePath(); got != "frames/chrome-150.png" {
		t.Fatalf("BundleFramePath = %q", got)
	}
	if got := r.BundleLogPath("smoke"); got != "logs/chrome-150-smoke.log" {
		t.Fatalf("BundleLogPath = %q", got)
	}
	if got := (Result{Browser: "chrome", Tag: "150"}).BundleFramePath(); got != "" {
		t.Fatalf("a result with no frame must yield %q, got %q", "", got)
	}
}
