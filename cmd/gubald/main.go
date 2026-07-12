package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TecharoHQ/gubal/cmd/gubald/svc/smoketest"
	gubalv1 "github.com/TecharoHQ/gubal/gen/techaro/lol/gubal/v1"
	"github.com/facebookgo/flagenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twitchtv/twirp"
	"golang.org/x/sync/errgroup"
	"within.website/x/twirp/twirpslog"
	"within.website/x/web/middleware/sigv4a/iamsts"
	"within.website/x/web/middleware/sigv4a/sigv4aclient"

	_ "github.com/joho/godotenv/autoload"
)

var (
	bind                 = flag.String("bind", ":9080", "HTTP bind address")
	githubToken          = flag.String("github-token", "", "GitHub token gubald posts PR comments with")
	githubRepos          = flag.String("github-repos", "TecharoHQ/anubis", "comma-separated owner/repo allowlist for async submits")
	jobDeadline          = flag.Duration("job-deadline", 60*time.Minute, "max wall-clock for a background smoke-test sweep")
	maxBodySize          = flag.Int64("max-body-size", 1<<20, "max request body bytes hashed for SigV4A verification")
	metricsBind          = flag.String("metrics-bind", ":9081", "Prometheus bind address")
	region               = flag.String("region", "yow", "SigV4a region all clients must sign for")
	service              = flag.String("service", "gubal", "SigV4a service name")
	xIAMDURL             = flag.String("x-iamd-url", "", "iamd url")
	xIAMDRegion          = flag.String("x-iamd-region", "yow", "iamd region")
	xIAMDAccessKeyID     = flag.String("x-iamd-access-key-id", "", "iamd access key ID")
	xIAMDSecretAccessKey = flag.String("x-iamd-secret-access-key", "", "iamd secret access key")
)

func main() {
	flagenv.Parse()
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	var lvl slog.Level
	lvlString := flag.Lookup("slog-level").Value.String()
	if err := lvl.UnmarshalText([]byte(lvlString)); err != nil {
		return fmt.Errorf("invalid -slog-level %q: %w", lvlString, err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	http.Handle("/metrics", promhttp.Handler())

	rt, err := sigv4aclient.NewSigV4ARoundTripper(&sigv4aclient.Config{
		Region:      *xIAMDRegion,
		AccessKey:   *xIAMDAccessKeyID, // your service's own IAM credential
		SecretKey:   *xIAMDSecretAccessKey,
		ServiceName: "iam", // credential-scope service for the iamd call
	}, nil) // nil -> http.DefaultTransport
	if err != nil {
		return err
	}
	iamHTTP := &http.Client{Transport: rt}

	verifier := iamsts.New(iamsts.Config{
		BaseURL:     *xIAMDURL,
		HTTPClient:  iamHTTP,
		Region:      *region,
		Service:     *service,
		MaxBodySize: *maxBodySize,
	})

	lg := slog.With("program", "gubald")

	mux := http.NewServeMux()

	// Unauthenticated liveness/readiness probe on the main port (not wrapped by the
	// sigv4a verifier, so probes never block on iamd).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	commenter, err := smoketest.NewGitHubCommenter(*githubToken)
	if err != nil {
		return fmt.Errorf("building github commenter: %w", err)
	}
	var allowed []string
	for _, r := range strings.Split(*githubRepos, ",") {
		if r = strings.TrimSpace(r); r != "" {
			allowed = append(allowed, r)
		}
	}
	smokeTest := smoketest.New(commenter, allowed, *jobDeadline)
	var smokeTestH http.Handler = gubalv1.NewSmokeTestServiceServer(smokeTest, twirp.WithServerInterceptors(twirpslog.Interceptor(lg)))
	smokeTestH = verifier.Middleware(smokeTestH)
	mux.Handle(gubalv1.SmokeTestServicePathPrefix, smokeTestH)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		lg.InfoContext(ctx, "Listening for metrics", "metrics-bind", *metricsBind)
		return http.ListenAndServe(*metricsBind, nil)
	})

	g.Go(func() error {
		slog.InfoContext(ctx, "starting server", "bind", *bind)
		return http.ListenAndServe(*bind, mux)
	})

	g.Go(func() error {
		<-ctx.Done()
		return ctx.Err()
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("error running group: %w", err)
	}

	return nil
}
