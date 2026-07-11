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

// renderMarkdown produces a human-readable summary table.
func renderMarkdown(results []Result) string {
	var b strings.Builder
	passed := 0
	for _, r := range results {
		if r.Status == StatusPass {
			passed++
		}
	}
	fmt.Fprintf(&b, "# Chrome version sweep — %d/%d passed\n\n", passed, len(results))
	b.WriteString("| tag | status | chrome version | frame | detail |\n")
	b.WriteString("|-----|--------|----------------|-------|--------|\n")
	for _, r := range results {
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

// renderJSON serializes the results as indented JSON.
func renderJSON(results []Result) ([]byte, error) {
	return json.MarshalIndent(results, "", "  ")
}
