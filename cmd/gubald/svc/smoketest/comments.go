package smoketest

import (
	"fmt"
	"unicode/utf8"
)

// maxCommentBytes is GitHub's hard limit on issue/PR comment length. Bodies over
// this are rejected with HTTP 422, so we truncate to stay safely under it.
const maxCommentBytes = 65536

// capTo truncates s to at most max bytes, rune-safe, appending a note when it
// does. Used to keep comment bodies under GitHub's per-comment limit.
func capTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const note = "\n\n_…report truncated (exceeded GitHub's comment size limit)_"
	limit := max - len(note)
	if limit < 0 {
		limit = 0
	}
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

// bodyResult is posted when the sweep finishes. The report is truncated if needed,
// but the header and the bundle link (if any) are always preserved.
func bodyResult(success bool, report, sha, bundleURL string) string {
	header := "✅ Gubal smoke test passed"
	if !success {
		header = "❌ Gubal smoke test found failures"
	}
	head := header + shaSuffix(sha) + "\n\n"
	tail := ""
	if bundleURL != "" {
		tail = fmt.Sprintf("\n\n📦 [Download report bundle (frames + logs)](%s)", bundleURL)
	}
	return head + capTo(report, maxCommentBytes-len(head)-len(tail)) + tail
}

// bodyRunError is posted when the sweep itself could not run (infra error).
func bodyRunError(sha string, err error) string {
	return capTo(fmt.Sprintf("❌ Gubal smoke test failed to run%s: %v", shaSuffix(sha), err), maxCommentBytes)
}
