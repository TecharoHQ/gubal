package chromesweep

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

type Status string

const (
	StatusPass     Status = "pass"
	StatusFail     Status = "fail"
	StatusNotReady Status = "not-ready"
	StatusError    Status = "error"
)

// Result is the outcome of testing one browser image tag.
type Result struct {
	Browser        string `json:"browser,omitempty"`
	Tag            string `json:"tag"`
	Status         Status `json:"status"`
	BrowserVersion string `json:"browser_version,omitempty"`
	ReportedUA     string `json:"reported_ua,omitempty"`
	FramePath      string `json:"frame_path,omitempty"`
	Detail         string `json:"detail,omitempty"`
	// Logs holds the captured container logs (browser + client) for this version.
	// Carried in memory only (not serialized to report.json): WriteBundle persists
	// them under logs/ and RenderMarkdown embeds them for failed runs.
	Logs []LogCapture `json:"-"`
}

// LogCapture is one container's captured logs for a swept version.
type LogCapture struct {
	Container string
	Content   string
}

// Report is the full outcome of a sweep run: the per-version results plus the
// Anubis image they were tested against (run-wide, since Anubis is a singleton).
type Report struct {
	AnubisImage string   `json:"anubis_image,omitempty"`
	Results     []Result `json:"results"`
}

// AllPassed reports whether every tested version passed. An empty run counts as
// passing (nothing failed).
func (r Report) AllPassed() bool {
	for _, res := range r.Results {
		if res.Status != StatusPass {
			return false
		}
	}
	return true
}

// RenderMarkdown produces a human-readable summary: one section per browser (in
// first-seen order), each with its own pass count and results table.
func RenderMarkdown(rep Report) string {
	var b strings.Builder
	if rep.AnubisImage != "" {
		fmt.Fprintf(&b, "Anubis image: `%s`\n\n", rep.AnubisImage)
	}
	var order []string
	groups := map[string][]Result{}
	for _, r := range rep.Results {
		if _, ok := groups[r.Browser]; !ok {
			order = append(order, r.Browser)
		}
		groups[r.Browser] = append(groups[r.Browser], r)
	}
	for _, br := range order {
		rs := groups[br]
		passed := 0
		for _, r := range rs {
			if r.Status == StatusPass {
				passed++
			}
		}
		fmt.Fprintf(&b, "# %s version sweep — %d/%d passed\n\n", titleCase(br), passed, len(rs))
		b.WriteString("| tag | status | browser version | frame | detail |\n")
		b.WriteString("|-----|--------|-----------------|-------|--------|\n")
		for _, r := range rs {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				r.Tag, r.Status, dash(r.BrowserVersion), dash(r.FramePath), dash(r.Detail))
		}
		b.WriteString("\n")
		// Attach captured logs for any non-passing run so a failure is diagnosable
		// straight from the report (the full logs also live in the bundle).
		for _, r := range rs {
			if r.Status == StatusPass || len(r.Logs) == 0 {
				continue
			}
			writeFailureLogs(&b, r)
		}
	}
	return b.String()
}

// writeFailureLogs renders a result's captured container logs inside a collapsed
// <details> block, one <pre> per container, HTML-escaped so log content can't
// break the surrounding markup.
func writeFailureLogs(b *strings.Builder, r Result) {
	fmt.Fprintf(b, "<details><summary>%s %s (%s) — logs</summary>\n\n", titleCase(r.Browser), r.Tag, r.Status)
	for _, lg := range r.Logs {
		fmt.Fprintf(b, "<b>%s</b>\n<pre>%s</pre>\n\n", html.EscapeString(lg.Container), html.EscapeString(lg.Content))
	}
	b.WriteString("</details>\n\n")
}

// titleCase upper-cases the first byte of s ("chrome" -> "Chrome").
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// RenderJSON serializes the report as indented JSON.
func RenderJSON(rep Report) ([]byte, error) {
	return json.MarshalIndent(rep, "", "  ")
}
