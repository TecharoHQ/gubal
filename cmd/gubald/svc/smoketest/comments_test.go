package smoketest

import (
	"errors"
	"strings"
	"testing"
)

func TestBodyAck(t *testing.T) {
	t.Parallel()
	b := bodyAck("abc1234", 16)
	if !strings.Contains(b, "🧪") || !strings.Contains(b, "16") {
		t.Fatalf("ack missing marker or count: %q", b)
	}
	if !strings.Contains(b, "abc1234") {
		t.Fatalf("ack missing sha: %q", b)
	}
	// No sha -> no empty parens / stray sha text.
	if strings.Contains(bodyAck("", 3), "()") {
		t.Fatalf("empty sha should not leave empty parens")
	}
}

func TestBodyBusy(t *testing.T) {
	t.Parallel()
	if !strings.Contains(bodyBusy(), "⏳") {
		t.Fatal("busy note missing marker")
	}
}

func TestBodyResult(t *testing.T) {
	t.Parallel()
	pass := bodyResult(true, "REPORT_MD", "sha1")
	if !strings.Contains(pass, "✅") || !strings.Contains(pass, "REPORT_MD") {
		t.Fatalf("pass body wrong: %q", pass)
	}
	fail := bodyResult(false, "REPORT_MD", "sha1")
	if !strings.Contains(fail, "❌") || !strings.Contains(fail, "REPORT_MD") {
		t.Fatalf("fail body wrong: %q", fail)
	}
}

func TestBodyRunError(t *testing.T) {
	t.Parallel()
	b := bodyRunError("sha1", errors.New("boom"))
	if !strings.Contains(b, "boom") || !strings.Contains(b, "❌") {
		t.Fatalf("run-error body wrong: %q", b)
	}
}
