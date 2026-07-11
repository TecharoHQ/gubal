package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := renderMarkdown(Report{
		AnubisImage: "reg/backend:v9",
		Results: []Result{
			{Tag: "150", Status: StatusPass, ChromeVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/150.png"},
			{Tag: "110", Status: StatusFail, Detail: "job failed"},
		},
	})
	for _, want := range []string{"| 150 |", "pass", "| 110 |", "fail", "job failed", "1/2 passed", "Anubis image:", "reg/backend:v9"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderMarkdownOmitsAnubisWhenEmpty(t *testing.T) {
	md := renderMarkdown(Report{Results: []Result{{Tag: "150", Status: StatusPass}}})
	if strings.Contains(md, "Anubis image:") {
		t.Fatalf("empty anubis image should be omitted:\n%s", md)
	}
}

func TestRenderJSON(t *testing.T) {
	b, err := renderJSON(Report{
		AnubisImage: "reg/backend:v9",
		Results:     []Result{{Tag: "150", Status: StatusPass}},
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
