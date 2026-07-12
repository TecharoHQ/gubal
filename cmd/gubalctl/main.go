// Command gubalctl submits a smoke-test build to a gubald instance at an
// arbitrary URL. It signs the request with SigV4A using the IAM credential read
// from -access-key-id / -secret-access-key (which flagenv fills from the
// ACCESS_KEY_ID / SECRET_ACCESS_KEY environment variables) and prints the
// Markdown report gubald returns.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	gubalv1 "github.com/TecharoHQ/gubal/gen/techaro/lol/gubal/v1"
	"github.com/facebookgo/flagenv"
	"github.com/google/uuid"
	"within.website/x/web/middleware/sigv4a/sigv4aclient"

	_ "github.com/joho/godotenv/autoload"
)

var (
	baseURL         = flag.String("url", "", "base URL of the gubald instance, e.g. https://gubald.xeserv.us")
	accessKeyID     = flag.String("access-key-id", "", "IAM access key ID to sign with (env: ACCESS_KEY_ID)")
	secretAccessKey = flag.String("secret-access-key", "", "IAM secret access key to sign with (env: SECRET_ACCESS_KEY)")
	region          = flag.String("region", "yow", "SigV4A region gubald verifies")
	service         = flag.String("service", "gubal", "SigV4A service name gubald verifies")
	anubisImage     = flag.String("anubis-image", "", "Anubis image to test against")
	chromeVersions  = flag.String("chrome-versions", "75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150", "comma-separated Chrome major versions to test")
	firefoxVersions = flag.String("firefox-versions", "146,147,148,149,150,151,152", "comma-separated Firefox major versions to test")
	id              = flag.String("id", "", "request id (UUID); generated when empty")

	githubRepo = flag.String("github-repo", "", "owner/repo of the PR to post results to (env: GITHUB_REPO); enables async mode with -pr-number")
	prNumber   = flag.Int("pr-number", 0, "PR number to post results to (env: PR_NUMBER)")
	commitSHA  = flag.String("github-sha", "", "commit SHA under test, shown in the report (env: GITHUB_SHA)")
)

func main() {
	flagenv.Parse()
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := run(context.Background()); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	switch {
	case *baseURL == "":
		return fmt.Errorf("-url is required")
	case *anubisImage == "":
		return fmt.Errorf("-anubis-image is required")
	case *accessKeyID == "" || *secretAccessKey == "":
		return fmt.Errorf("both -access-key-id and -secret-access-key are required (env ACCESS_KEY_ID / SECRET_ACCESS_KEY)")
	}

	chromeVs, err := parseVersions(*chromeVersions, "chrome")
	if err != nil {
		return err
	}
	firefoxVs, err := parseVersions(*firefoxVersions, "firefox")
	if err != nil {
		return err
	}

	reqID := *id
	if reqID == "" {
		reqID = uuid.NewString()
	}

	rt, err := sigv4aclient.NewSigV4ARoundTripper(&sigv4aclient.Config{
		Region:      *region,
		AccessKey:   *accessKeyID,
		SecretKey:   *secretAccessKey,
		ServiceName: *service,
	}, nil)
	if err != nil {
		return fmt.Errorf("creating signing transport: %w", err)
	}

	client := gubalv1.NewSmokeTestServiceProtobufClient(*baseURL, &http.Client{Transport: rt})

	if wantsAsync(*githubRepo, *prNumber) {
		slog.InfoContext(ctx, "submitting async smoke test", "url", *baseURL, "id", reqID, "repo", *githubRepo, "pr", *prNumber)
		resp, err := client.SubmitSmokeTest(ctx, &gubalv1.SubmitSmokeTestRequest{
			Test: &gubalv1.SmokeTestRequest{
				Id:              reqID,
				AnubisImage:     *anubisImage,
				ChromeVersions:  chromeVs,
				FirefoxVersions: firefoxVs,
			},
			Github: &gubalv1.GitHubTarget{
				Repo:      *githubRepo,
				PrNumber:  int32(*prNumber),
				CommitSha: *commitSHA,
			},
		})
		if err != nil {
			return fmt.Errorf("submitting smoke test: %w", err)
		}
		slog.InfoContext(ctx, "smoke test accepted; results will be posted to the PR", "id", resp.GetId())
		return nil
	}

	slog.InfoContext(ctx, "submitting smoke test", "url", *baseURL, "id", reqID, "anubis_image", *anubisImage, "chrome_versions", chromeVs, "firefox_versions", firefoxVs)
	res, err := client.SmokeTest(ctx, &gubalv1.SmokeTestRequest{
		Id:              reqID,
		AnubisImage:     *anubisImage,
		ChromeVersions:  chromeVs,
		FirefoxVersions: firefoxVs,
	})
	if err != nil {
		return fmt.Errorf("smoke test request failed: %w", err)
	}

	fmt.Print(res.GetReport())
	if !res.GetSuccess() {
		return fmt.Errorf("smoke test reported failure; see report above")
	}
	return nil
}

// wantsAsync reports whether the caller supplied enough to post to a PR thread,
// in which case gubalctl submits asynchronously instead of blocking on a sweep.
func wantsAsync(githubRepo string, prNumber int) bool {
	return githubRepo != "" && prNumber > 0
}

// parseVersions turns a comma-separated list of majors into int32s.
func parseVersions(s, browser string) ([]int32, error) {
	fields := strings.Split(s, ",")
	out := make([]int32, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("invalid %s version %q: %w", browser, f, err)
		}
		out = append(out, int32(n))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-%s-versions is required (comma-separated)", browser)
	}
	return out, nil
}
