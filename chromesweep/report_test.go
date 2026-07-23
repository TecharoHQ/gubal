package chromesweep

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(Report{
		AnubisImage: "reg/backend:v9",
		Results: []Result{
			{Policy: "default", Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/default-chrome-150.png"},
			{Policy: "default", Browser: "chrome", Tag: "110", Status: StatusFail, Detail: "job failed"},
			{Policy: "default", Browser: "firefox", Tag: "152", Status: StatusPass, BrowserVersion: "152.0.5", FramePath: "var/sweep/default-firefox-152.png"},
			{Policy: "hard", Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114"},
		},
	})
	for _, want := range []string{
		"## Anubis policy results",
		"| default | fail | 2/3 |",
		"| hard | pass | 1/1 |",
		"# Policy: default",
		"# Policy: hard",
		"## Chrome version sweep — 1/2 passed",
		"## Firefox version sweep — 1/1 passed",
		"| 150 |", "| 110 |", "job failed", "| 152 |",
		"Anubis image:", "reg/backend:v9",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// default policy section precedes hard (first-seen order).
	if strings.Index(md, "# Policy: default") > strings.Index(md, "# Policy: hard") {
		t.Fatalf("policy sections out of order:\n%s", md)
	}
}

func TestRenderMarkdownEmbedsFailureLogs(t *testing.T) {
	md := RenderMarkdown(Report{
		Results: []Result{
			{Browser: "firefox", Tag: "152", Status: StatusFail, Detail: "smoke job failed", Logs: []LogCapture{
				// 7 lines; only the last 5 (l3..l7) should be embedded.
				{Container: "firefox", Content: "l1\nl2\nl3\nl4\nl5\nl6\nl7\n"},
				{Container: "chrome-bully", Content: "bidi client closed <-32000>"},
			}},
			{Browser: "firefox", Tag: "140", Status: StatusPass, Logs: []LogCapture{
				{Container: "firefox", Content: "should-not-appear-passed"},
			}},
		},
	})
	for _, want := range []string{
		"<details><summary>Firefox 152 (fail) — logs (last 5 lines each; full logs in bundle)</summary>",
		"<b>firefox</b>",
		"<b>chrome-bully</b>",
		"l3\nl4\nl5\nl6\nl7",                // last 5 lines only
		"bidi client closed &lt;-32000&gt;", // HTML-escaped
		"</details>",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// The trimmed-off head lines must NOT appear.
	for _, gone := range []string{"l1", "l2"} {
		if strings.Contains(md, gone) {
			t.Fatalf("line %q should have been trimmed to the last 5:\n%s", gone, md)
		}
	}
	// A passing run's logs must NOT be embedded.
	if strings.Contains(md, "should-not-appear-passed") {
		t.Fatalf("passing run logs must not be embedded:\n%s", md)
	}
	if strings.Contains(md, "Firefox 140 (pass)") {
		t.Fatalf("no details block for a passing run:\n%s", md)
	}
}

func TestRenderMarkdownOmitsAnubisWhenEmpty(t *testing.T) {
	md := RenderMarkdown(Report{Results: []Result{{Browser: "chrome", Tag: "150", Status: StatusPass}}})
	if strings.Contains(md, "Anubis image:") {
		t.Fatalf("empty anubis image should be omitted:\n%s", md)
	}
}

func TestRenderJSON(t *testing.T) {
	b, err := RenderJSON(Report{
		AnubisImage: "reg/backend:v9",
		Results:     []Result{{Browser: "chrome", Tag: "150", Status: StatusPass}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out Report
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.AnubisImage != "reg/backend:v9" {
		t.Fatalf("anubis image round-trip = %q", out.AnubisImage)
	}
	if len(out.Results) != 1 || out.Results[0].Tag != "150" || out.Results[0].Status != StatusPass {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestPolicyStats(t *testing.T) {
	stats := PolicyStats([]Result{
		{Policy: "default", Status: StatusPass},
		{Policy: "default", Status: StatusFail},
		{Policy: "hard", Status: StatusPass},
		{Policy: "hard", Status: StatusPass},
	})
	if len(stats) != 2 {
		t.Fatalf("want 2 policies, got %d: %+v", len(stats), stats)
	}
	if stats[0].Name != "default" || stats[0].Passed != 1 || stats[0].Total != 2 || stats[0].Status() != StatusFail {
		t.Fatalf("default stat wrong: %+v", stats[0])
	}
	if stats[1].Name != "hard" || stats[1].Passed != 2 || stats[1].Total != 2 || stats[1].Status() != StatusPass {
		t.Fatalf("hard stat wrong: %+v", stats[1])
	}
}

// TestRenderMarkdownLinksIntoBundle checks the frame column names a path that
// exists inside report.zip, not the scratch dir the sweep happened to use.
func TestRenderMarkdownLinksIntoBundle(t *testing.T) {
	md := RenderMarkdown(Report{Results: []Result{
		{Policy: "default", Browser: "chrome", Tag: "150", Status: StatusPass, FramePath: "/tmp/sweep-123/default-chrome-150.png"},
	}})
	if !strings.Contains(md, "frames/default/chrome-150.png") {
		t.Fatalf("report should link the bundle-relative frame path:\n%s", md)
	}
	if strings.Contains(md, "/tmp/sweep-123") {
		t.Fatalf("report must not leak the local scratch path:\n%s", md)
	}
}

func TestResultPolicyRoundTrips(t *testing.T) {
	b, err := RenderJSON(Report{Results: []Result{{Policy: "hard", Browser: "chrome", Tag: "150", Status: StatusPass}}})
	if err != nil {
		t.Fatal(err)
	}
	var out Report
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Results[0].Policy != "hard" {
		t.Fatalf("policy round-trip = %q", out.Results[0].Policy)
	}
}
