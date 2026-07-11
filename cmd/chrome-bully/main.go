// Command chrome-bully drives an already-running headless Chrome over the
// DevTools protocol: it loads a target URL (by default the httpdebug sidecar on
// localhost:8080), screenshots the rendered frame, and writes it to disk as a
// PNG. Point --out-dir at a mounted PVC to keep the frames.
//
// It is meant to run alongside Chrome in the same pod, so it can reach CDP at
// localhost:9222 (where the Host header is "localhost" and DevTools' anti
// DNS-rebinding check is satisfied) and reach the sidecar at localhost:8080.
//
// Pass -header "Name: Value" (repeatable) to send extra headers with every
// request, and -expect-text to wait until the loaded page contains a string
// before screenshotting — together they prove a header round-tripped through a
// reflector like httpdebug (set the header, expect its value back).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/facebookgo/flagenv"
)

var (
	cdpURL     = flag.String("cdp-url", "http://localhost:9222", "base URL of the Chrome DevTools endpoint to drive")
	targetURL  = flag.String("target-url", "http://localhost:8080", "URL to load in the browser before capturing")
	outDir     = flag.String("out-dir", "/data", "directory to write captured PNG frames into (mount a PVC here)")
	interval   = flag.Duration("interval", 0, "capture repeatedly on this interval; 0 captures once and exits")
	timeout    = flag.Duration("capture-timeout", 30*time.Second, "maximum time for a single navigate+wait+screenshot")
	width      = flag.Int("width", 1280, "viewport width in pixels (0 leaves Chrome's default)")
	height     = flag.Int("height", 720, "viewport height in pixels (0 leaves Chrome's default)")
	expectText = flag.String("expect-text", "", "if set, wait until the loaded page's text contains this substring before screenshotting (fails on timeout)")
	slogLevel  = flag.String("slog-level", "info", "log level: debug, info, warn, error")
)

// headerFlags collects repeated -header "Name: Value" flags; parsed into
// extraHeaders (the shape CDP wants) once in run().
var (
	headerFlags  headerList
	extraHeaders network.Headers
)

// headerList is a repeatable string flag.
type headerList []string

func (h *headerList) String() string { return strings.Join(*h, ", ") }
func (h *headerList) Set(v string) error {
	*h = append(*h, v)
	return nil
}

func main() {
	flag.Var(&headerFlags, "header", "extra HTTP header to send with every request, as 'Name: Value' (repeatable)")
	flagenv.Parse()
	flag.Parse()

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*slogLevel)); err != nil {
		return fmt.Errorf("invalid -slog-level %q: %w", *slogLevel, err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	// Parse -header flags into the map CDP wants. Do it up front so a malformed
	// header fails fast rather than on the first capture.
	extraHeaders = network.Headers{}
	for _, h := range headerFlags {
		name, val, ok := strings.Cut(h, ":")
		if !ok {
			return fmt.Errorf("invalid -header %q: want 'Name: Value'", h)
		}
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		if name == "" {
			return fmt.Errorf("invalid -header %q: empty name", h)
		}
		extraHeaders[name] = val
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("cannot create out-dir %q: %w", *outDir, err)
	}

	// Connect once to the already-running Chrome; each capture opens a fresh tab.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, *cdpURL)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	// Fetch the browser version up front: it names the frames, and running an
	// action also forces the connection so we fail fast, with a clear message,
	// when Chrome isn't reachable. The version is fixed for the life of the
	// server, so one lookup is enough.
	var product string
	if err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, prod, _, _, _, err := browser.GetVersion().Do(ctx)
		if err != nil {
			return err
		}
		product = prod
		return nil
	})); err != nil {
		return fmt.Errorf("cannot reach chrome at %s: %w", *cdpURL, err)
	}

	version := chromeVersion(product)

	slog.Info("connected to chrome",
		"cdp-url", *cdpURL,
		"product", product,
		"chrome-version", version,
		"target-url", *targetURL,
		"out-dir", *outDir,
		"interval", interval.String(),
		"headers", len(extraHeaders),
		"expect-text", *expectText,
	)

	if *interval <= 0 {
		path, err := capture(browserCtx, version)
		if err != nil {
			return err
		}
		slog.Info("captured", "path", path)
		return nil
	}

	tick := time.NewTicker(*interval)
	defer tick.Stop()

	// Capture immediately, then on every tick, until we're asked to stop. A single
	// failed capture is logged and retried on the next tick rather than fatal.
	for {
		path, err := capture(browserCtx, version)
		switch {
		case errors.Is(err, context.Canceled):
			slog.Info("shutting down")
			return nil
		case err != nil:
			slog.Error("capture failed", "err", err)
		default:
			slog.Info("captured", "path", path)
		}

		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return nil
		case <-tick.C:
		}
	}
}

// capture opens a fresh tab, loads the target URL, screenshots the frame, and
// writes it to out-dir as a PNG named for the detected Chrome version. It returns
// the path written. With -header set, every request carries the extra headers;
// with -expect-text set, it waits for that text to appear before screenshotting.
func capture(parent context.Context, version string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()

	tabCtx, cancelTab := chromedp.NewContext(ctx)
	defer cancelTab()

	// Set extra headers (needs the Network domain enabled) and viewport, then load.
	setup := make([]chromedp.Action, 0, 4)
	if len(extraHeaders) > 0 {
		setup = append(setup, network.Enable(), network.SetExtraHTTPHeaders(extraHeaders))
	}
	if *width > 0 && *height > 0 {
		setup = append(setup, chromedp.EmulateViewport(int64(*width), int64(*height)))
	}
	setup = append(setup, chromedp.Navigate(*targetURL))
	if err := chromedp.Run(tabCtx, setup...); err != nil {
		return "", fmt.Errorf("loading %s: %w", *targetURL, err)
	}

	// Optionally wait for the backend to render the expected text (e.g. a header
	// echoed by httpdebug). This also rides out an interstitial like Anubis, which
	// navigates to the real backend only after its challenge is solved.
	if *expectText != "" {
		if err := waitForText(tabCtx, *expectText); err != nil {
			return "", err
		}
	}

	var buf []byte
	if err := chromedp.Run(tabCtx, chromedp.FullScreenshot(&buf, 100)); err != nil {
		return "", fmt.Errorf("screenshotting %s: %w", *targetURL, err)
	}

	// The version leads the name; a timestamp keeps interval runs from clobbering
	// earlier frames of the same version.
	name := fmt.Sprintf("chrome-%s-%s.png", sanitize(version), time.Now().UTC().Format("20060102T150405.000Z"))
	path := filepath.Join(*outDir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// waitForText blocks until the tab's rendered text contains want, or ctx is
// done. It re-evaluates each tick rather than injecting a one-shot observer, so
// it survives the page navigating out from under it (e.g. an interstitial handing
// off to the real backend). A transient evaluation error mid-navigation is
// treated as "not yet" and retried.
func waitForText(ctx context.Context, want string) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		var txt string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.innerText`, &txt)); err == nil {
			if strings.Contains(txt, want) {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %q in %s: %w", timeout.String(), want, *targetURL, ctx.Err())
		case <-tick.C:
		}
	}
}

// chromeVersion pulls the version number out of a CDP product string such as
// "Chrome/120.0.6099.109" (or "HeadlessChrome/120.0.6099.109"), falling back to
// the raw product when it isn't shaped that way.
func chromeVersion(product string) string {
	if i := strings.LastIndex(product, "/"); i >= 0 && i+1 < len(product) {
		return product[i+1:]
	}
	if product == "" {
		return "unknown"
	}
	return product
}

// sanitize maps anything outside [A-Za-z0-9._-] to '-' so the value is safe to
// drop into a filename.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}
