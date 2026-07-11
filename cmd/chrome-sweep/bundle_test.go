package main

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
		{Tag: "150", Status: StatusPass, FramePath: f150},
		{Tag: "120", Status: StatusPass, FramePath: f120},
		{Tag: "999", Status: StatusFail}, // no frame — must be skipped
	}
	reportJSON := []byte(`{"results":[]}`)
	reportMarkdown := []byte("# Chrome version sweep — 2/3 passed\n")
	zipPath := filepath.Join(dir, "report.zip")
	if err := writeBundle(zipPath, reportJSON, reportMarkdown, results); err != nil {
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
	if !bytes.Equal(got["frames/150.png"], []byte("PNG-150")) {
		t.Fatalf("frames/150.png = %q", got["frames/150.png"])
	}
	if !bytes.Equal(got["frames/120.png"], []byte("PNG-120")) {
		t.Fatalf("frames/120.png = %q", got["frames/120.png"])
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 entries (report.json + report.md + 2 frames), got %d", len(got))
	}
}
