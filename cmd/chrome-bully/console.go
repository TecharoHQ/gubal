package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// consoleMsg is the slog message every devtools-console line shares. It is
// deliberately distinct from "captured" and "fatal", the two messages
// chromesweep's log parser keys on, so console output is never mistaken for a
// result marker.
const consoleMsg = "browser console"

// consoleActions enables the CDP domains listenConsole draws from: Runtime
// carries console API calls and uncaught exceptions, Log carries browser-level
// entries (network failures, CSP/security violations, deprecations). Run these
// before navigating.
func consoleActions() []chromedp.Action {
	return []chromedp.Action{runtime.Enable(), cdplog.Enable()}
}

// listenConsole forwards the tab's devtools console to slog, so a version that
// fails its Anubis challenge leaves the JS/WASM errors explaining why in the pod
// log (and therefore in the report bundle).
//
// Attach it after the target exists but before navigating, or messages from the
// first paint are lost. The callback runs on chromedp's event goroutine, so it
// must not block; slog is safe for concurrent use.
func listenConsole(ctx context.Context) {
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			slog.Log(context.Background(), consoleLevel(string(e.Type)), consoleMsg,
				"kind", "console", "type", string(e.Type), "text", consoleArgs(e.Args))
		case *runtime.EventExceptionThrown:
			d := e.ExceptionDetails
			if d == nil {
				return
			}
			slog.Error(consoleMsg,
				"kind", "exception", "text", d.Text, "detail", exceptionDetail(d),
				"url", d.URL, "line", d.LineNumber)
		case *cdplog.EventEntryAdded:
			if e.Entry == nil {
				return
			}
			en := e.Entry
			slog.Log(context.Background(), consoleLevel(string(en.Level)), consoleMsg,
				"kind", "log-entry", "source", string(en.Source), "level", string(en.Level),
				"text", en.Text, "url", en.URL, "line", en.LineNumber)
		}
	})
}

// consoleLevel maps a CDP severity onto a slog level. The Runtime and Log
// domains spell these differently ("warning" vs "warn", "verbose" vs "debug"),
// so both spellings are accepted.
func consoleLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "error", "assert":
		return slog.LevelError
	case "warning", "warn":
		return slog.LevelWarn
	case "debug", "verbose", "trace":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// consoleArgs flattens a console call's arguments into one line.
func consoleArgs(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == nil {
			continue
		}
		parts = append(parts, remoteObject(a))
	}
	return strings.Join(parts, " ")
}

// remoteObject renders one console argument as text. A RemoteObject carries
// either a JSON value (primitives), a description (objects and errors), or an
// unserializable marker (NaN, Infinity); prefer them in that order.
func remoteObject(a *runtime.RemoteObject) string {
	if len(a.Value) > 0 {
		// Strings arrive JSON-quoted; unquote them so the log reads naturally.
		var s string
		if err := json.Unmarshal([]byte(a.Value), &s); err == nil {
			return s
		}
		return string(a.Value)
	}
	if a.Description != "" {
		return a.Description
	}
	if a.UnserializableValue != "" {
		return string(a.UnserializableValue)
	}
	return string(a.Type)
}

// exceptionDetail prefers the exception object's description, which carries the
// stack, over the bare summary text.
func exceptionDetail(d *runtime.ExceptionDetails) string {
	if d.Exception == nil {
		return ""
	}
	return d.Exception.Description
}
