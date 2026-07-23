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
	// Policy is the Anubis ruleset this version was tested against (the policy
	// filename without extension). Empty when the sweep used Anubis's live policy.
	Policy         string `json:"policy,omitempty"`
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

// PolicyStat is the pass tally for one Anubis policy across all browsers/versions.
type PolicyStat struct {
	Name   string
	Passed int
	Total  int
}

// Status is pass when every version under the policy passed, else fail.
func (p PolicyStat) Status() Status {
	if p.Total > 0 && p.Passed == p.Total {
		return StatusPass
	}
	return StatusFail
}

// PolicyStats tallies pass/total per policy in first-seen order.
func PolicyStats(results []Result) []PolicyStat {
	idx := map[string]int{}
	var stats []PolicyStat
	for _, r := range results {
		i, ok := idx[r.Policy]
		if !ok {
			i = len(stats)
			idx[r.Policy] = i
			stats = append(stats, PolicyStat{Name: r.Policy})
		}
		stats[i].Total++
		if r.Status == StatusPass {
			stats[i].Passed++
		}
	}
	return stats
}

// RenderMarkdown produces a human-readable summary: an Anubis-policy pass/fail
// table, then one section per policy (first-seen order), each grouping its
// browsers and their per-version results.
func RenderMarkdown(rep Report) string {
	var b strings.Builder
	if rep.AnubisImage != "" {
		fmt.Fprintf(&b, "Anubis image: `%s`\n\n", rep.AnubisImage)
	}
	if stats := PolicyStats(rep.Results); len(stats) > 0 {
		b.WriteString("## Anubis policy results\n\n")
		b.WriteString("| policy | status | versions passed |\n")
		b.WriteString("|--------|--------|-----------------|\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "| %s | %s | %d/%d |\n", dash(s.Name), s.Status(), s.Passed, s.Total)
		}
		b.WriteString("\n")
	}
	var order []string
	byPolicy := map[string][]Result{}
	for _, r := range rep.Results {
		if _, ok := byPolicy[r.Policy]; !ok {
			order = append(order, r.Policy)
		}
		byPolicy[r.Policy] = append(byPolicy[r.Policy], r)
	}
	for _, pol := range order {
		if pol != "" {
			fmt.Fprintf(&b, "# Policy: %s\n\n", pol)
		}
		renderBrowserGroups(&b, byPolicy[pol])
	}
	return b.String()
}

// renderBrowserGroups renders one browser section per browser (first-seen order)
// for results already scoped to a single policy: a header with the pass count, a
// results table, then collapsed failure-log blocks for any non-passing run.
func renderBrowserGroups(b *strings.Builder, results []Result) {
	var order []string
	groups := map[string][]Result{}
	for _, r := range results {
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
		fmt.Fprintf(b, "## %s version sweep — %d/%d passed\n\n", titleCase(br), passed, len(rs))
		b.WriteString("| tag | status | browser version | frame | detail |\n")
		b.WriteString("|-----|--------|-----------------|-------|--------|\n")
		for _, r := range rs {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
				r.Tag, r.Status, dash(r.BrowserVersion), dash(r.BundleFramePath()), dash(r.Detail))
		}
		b.WriteString("\n")
		for _, r := range rs {
			if r.Status == StatusPass || len(r.Logs) == 0 {
				continue
			}
			writeFailureLogs(b, r)
		}
	}
}

// failureLogTailLines is how many trailing lines of each container log are embedded
// in the report. The full log always lives in the bundle under logs/.
const failureLogTailLines = 5

// writeFailureLogs renders a result's captured container logs inside a collapsed
// <details> block, one <pre> per container, HTML-escaped so log content can't
// break the surrounding markup. Only the last failureLogTailLines lines of each
// log are shown; the full logs are in the bundle.
func writeFailureLogs(b *strings.Builder, r Result) {
	fmt.Fprintf(b, "<details><summary>%s %s (%s) — logs (last %d lines each; full logs in bundle)</summary>\n\n",
		titleCase(r.Browser), r.Tag, r.Status, failureLogTailLines)
	for _, lg := range r.Logs {
		fmt.Fprintf(b, "<b>%s</b>\n<pre>%s</pre>\n\n",
			html.EscapeString(lg.Container), html.EscapeString(lastLines(lg.Content, failureLogTailLines)))
	}
	b.WriteString("</details>\n\n")
}

// lastLines returns the last n lines of s. A single trailing newline is ignored
// (it does not count as a blank final line). If s has n or fewer lines it is
// returned with only that trailing newline trimmed.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
