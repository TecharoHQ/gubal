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
			{Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/chrome-150.png"},
			{Browser: "chrome", Tag: "110", Status: StatusFail, Detail: "job failed"},
			{Browser: "firefox", Tag: "152", Status: StatusPass, BrowserVersion: "152.0.5", FramePath: "var/sweep/firefox-152.png"},
		},
	})
	for _, want := range []string{
		"# Chrome version sweep — 1/2 passed",
		"# Firefox version sweep — 1/1 passed",
		"| 150 |", "| 110 |", "job failed", "| 152 |",
		"Anubis image:", "reg/backend:v9",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// Chrome section precedes Firefox (first-seen order).
	if strings.Index(md, "Chrome version sweep") > strings.Index(md, "Firefox version sweep") {
		t.Fatalf("browser sections out of order:\n%s", md)
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
