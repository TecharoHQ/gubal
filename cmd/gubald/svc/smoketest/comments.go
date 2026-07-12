package smoketest

import (
	"fmt"
	"unicode/utf8"
)

// maxCommentBytes is GitHub's hard limit on issue/PR comment length. Bodies over
// this are rejected with HTTP 422, so we truncate to stay safely under it.
const maxCommentBytes = 65536

// capBody truncates s to safely under GitHub's comment limit, appending a note
// when it does. Truncation is rune-safe (never splits a multi-byte character).
func capBody(s string) string {
	if len(s) <= maxCommentBytes {
		return s
	}
	const note = "\n\n_…report truncated (exceeded GitHub's comment size limit)_"
	limit := maxCommentBytes - len(note)
	// Back off to a rune boundary so we never split a multi-byte character.
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + note
}

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
	return capBody(fmt.Sprintf("%s%s\n\n%s", header, shaSuffix(sha), report))
}

// bodyRunError is posted when the sweep itself could not run (infra error).
func bodyRunError(sha string, err error) string {
	return capBody(fmt.Sprintf("❌ Gubal smoke test failed to run%s: %v", shaSuffix(sha), err))
}
