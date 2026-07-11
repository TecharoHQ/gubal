package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := renderMarkdown([]Result{
		{Tag: "150", Status: StatusPass, ChromeVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/150.png"},
		{Tag: "110", Status: StatusFail, Detail: "job failed"},
	})
	for _, want := range []string{"| 150 |", "pass", "| 110 |", "fail", "job failed", "1/2 passed"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	b, err := renderJSON([]Result{{Tag: "150", Status: StatusPass}})
	if err != nil {
		t.Fatal(err)
	}
	var out []Result
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out) != 1 || out[0].Tag != "150" || out[0].Status != StatusPass {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
