package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Status string

const (
	StatusPass     Status = "pass"
	StatusFail     Status = "fail"
	StatusNotReady Status = "not-ready"
	StatusError    Status = "error"
)

// Result is the outcome of testing one Chrome tag.
type Result struct {
	Tag           string `json:"tag"`
	Status        Status `json:"status"`
	ChromeVersion string `json:"chrome_version,omitempty"`
	ReportedUA    string `json:"reported_ua,omitempty"`
	FramePath     string `json:"frame_path,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// Report is the full outcome of a sweep run: the per-version results plus the
// Anubis image they were tested against (run-wide, since Anubis is a singleton).
type Report struct {
	AnubisImage string   `json:"anubis_image,omitempty"`
	Results     []Result `json:"results"`
}

// renderMarkdown produces a human-readable summary table.
func renderMarkdown(rep Report) string {
	var b strings.Builder
	passed := 0
	for _, r := range rep.Results {
		if r.Status == StatusPass {
			passed++
		}
	}
	fmt.Fprintf(&b, "# Chrome version sweep — %d/%d passed\n\n", passed, len(rep.Results))
	if rep.AnubisImage != "" {
		fmt.Fprintf(&b, "Anubis image: `%s`\n\n", rep.AnubisImage)
	}
	b.WriteString("| tag | status | chrome version | frame | detail |\n")
	b.WriteString("|-----|--------|----------------|-------|--------|\n")
	for _, r := range rep.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			r.Tag, r.Status, dash(r.ChromeVersion), dash(r.FramePath), dash(r.Detail))
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// renderJSON serializes the report as indented JSON.
func renderJSON(rep Report) ([]byte, error) {
	return json.MarshalIndent(rep, "", "  ")
}
