package main

import (
	"log/slog"
	"testing"

	"github.com/chromedp/cdproto/runtime"
)

func TestConsoleLevel(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want slog.Level
	}{
		{"error", slog.LevelError},
		{"assert", slog.LevelError},
		{"warning", slog.LevelWarn}, // Runtime domain spelling
		{"warn", slog.LevelWarn},
		{"debug", slog.LevelDebug},
		{"verbose", slog.LevelDebug}, // Log domain spelling
		{"trace", slog.LevelDebug},
		{"log", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"ERROR", slog.LevelError}, // case-insensitive
	} {
		if got := consoleLevel(tt.in); got != tt.want {
			t.Errorf("consoleLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestConsoleArgs(t *testing.T) {
	args := []*runtime.RemoteObject{
		{Type: "string", Value: []byte(`"challenge solved"`)}, // JSON-quoted; must be unquoted
		{Type: "number", Value: []byte(`42`)},
		{Type: "object", Description: "Error: boom"}, // no value; falls back to description
		{Type: "number", UnserializableValue: "NaN"}, // neither; falls back to the marker
		{Type: "undefined"},                          // none of the three; falls back to Type
		nil,                                          // must be skipped, not panic
	}
	if got, want := consoleArgs(args), "challenge solved 42 Error: boom NaN undefined"; got != want {
		t.Fatalf("consoleArgs = %q, want %q", got, want)
	}
	if got := consoleArgs(nil); got != "" {
		t.Fatalf("consoleArgs(nil) = %q, want empty", got)
	}
}
