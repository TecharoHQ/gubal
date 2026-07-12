package smoketest

import "fmt"

// shaSuffix renders " (`<sha>`)" when sha is set, else "".
func shaSuffix(sha string) string {
	if sha == "" {
		return ""
	}
	return fmt.Sprintf(" (`%s`)", sha)
}

// bodyAck is posted when a submit is accepted, before the sweep runs.
func bodyAck(sha string, versionCount int) string {
	return fmt.Sprintf("🧪 Running Gubal smoke test against %d browser version(s)%s. This takes a while; results will follow in a new comment.", versionCount, shaSuffix(sha))
}

// bodyBusy is posted when a submit is rejected because a sweep is already running.
func bodyBusy() string {
	return "⏳ Gubal is already running another smoke test. Re-run `/gubaltest` once it finishes."
}

// bodyResult is posted when the sweep finishes. success drives the header emoji;
// report is the rendered Markdown table.
func bodyResult(success bool, report, sha string) string {
	header := "✅ Gubal smoke test passed"
	if !success {
		header = "❌ Gubal smoke test found failures"
	}
	return fmt.Sprintf("%s%s\n\n%s", header, shaSuffix(sha), report)
}

// bodyRunError is posted when the sweep itself could not run (infra error).
func bodyRunError(sha string, err error) string {
	return fmt.Sprintf("❌ Gubal smoke test failed to run%s: %v", shaSuffix(sha), err)
}
