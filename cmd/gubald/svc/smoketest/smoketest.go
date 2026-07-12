package smoketest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/TecharoHQ/gubal/chromesweep"
	gubalv1 "github.com/TecharoHQ/gubal/gen/techaro/lol/gubal/v1"
	"github.com/twitchtv/twirp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// sweepSem is a global semaphore that serializes smoke-test sweeps: a sweep
// mutates shared per-version cluster resources (chrome-<tag> Deployment,
// Service, NetworkPolicy and the chrome-smoke-<tag> Job), so two running at
// once would collide. Buffered to 1 so only a single sweep runs at a time.
var sweepSem = make(chan struct{}, 1)

// prCommenter posts a comment to a GitHub PR thread. Backed by githubCommenter in
// production; a fake in tests.
type prCommenter interface {
	Comment(ctx context.Context, repo string, pr int, body string) error
}

type Server struct {
	gubalv1.UnimplementedSmokeTestServiceServer

	gh          prCommenter
	uploader    bundleUploader
	allowed     map[string]struct{}
	jobDeadline time.Duration
}

// New builds a Server. gh posts async results to GitHub; up uploads report
// bundles (a noopUploader disables uploads); allowedRepos is the "owner/repo"
// allowlist SubmitSmokeTest enforces (matched case-insensitively); jobDeadline
// bounds a background sweep.
func New(gh prCommenter, up bundleUploader, allowedRepos []string, jobDeadline time.Duration) *Server {
	allowed := make(map[string]struct{}, len(allowedRepos))
	for _, r := range allowedRepos {
		r = strings.TrimSpace(r)
		if r != "" {
			allowed[strings.ToLower(r)] = struct{}{}
		}
	}
	return &Server{gh: gh, uploader: up, allowed: allowed, jobDeadline: jobDeadline}
}

func (s *Server) SmokeTest(ctx context.Context, req *gubalv1.SmokeTestRequest) (*gubalv1.SmokeTestResult, error) {
	if err := protovalidate.Validate(req); err != nil {
		return nil, twirp.NewError(twirp.InvalidArgument, err.Error())
	}

	// Serialize sweeps: only one may touch the shared cluster resources at a
	// time. Reject immediately if a sweep is already running rather than
	// queueing behind it.
	select {
	case sweepSem <- struct{}{}:
		defer func() { <-sweepSem }()
	default:
		return nil, twirp.NewError(twirp.ResourceExhausted, "a smoke test is already running; try again later")
	}

	return s.runSweep(ctx, req)
}

// executeSweep runs the browser sweep and returns the raw Report plus the frames
// dir; the caller owns removing the dir. Assumes req is validated and the sweep
// semaphore is held.
func (s *Server) executeSweep(ctx context.Context, req *gubalv1.SmokeTestRequest) (chromesweep.Report, string, error) {
	browsers, err := browsersFor(req)
	if err != nil {
		return chromesweep.Report{}, "", twirp.NewError(twirp.InvalidArgument, err.Error())
	}
	cs, err := loadClientset()
	if err != nil {
		return chromesweep.Report{}, "", twirp.InternalErrorWith(fmt.Errorf("building kube client: %w", err))
	}
	cfg := chromesweep.DefaultConfig()
	cfg.AnubisImage = req.GetAnubisImage()
	cfg.Browsers = browsers

	framesDir, err := os.MkdirTemp("", "smoketest-frames-")
	if err != nil {
		return chromesweep.Report{}, "", twirp.InternalErrorWith(err)
	}
	rep, err := chromesweep.Run(ctx, chromesweep.NewCluster(cs, cfg.Namespace), cfg, framesDir)
	if err != nil {
		os.RemoveAll(framesDir)
		return chromesweep.Report{}, "", twirp.InternalErrorWith(err)
	}
	return rep, framesDir, nil
}

// runSweep runs the browser sweep for req and maps it to a SmokeTestResult. It
// assumes the caller already validated req and holds the sweep semaphore.
func (s *Server) runSweep(ctx context.Context, req *gubalv1.SmokeTestRequest) (*gubalv1.SmokeTestResult, error) {
	rep, framesDir, err := s.executeSweep(ctx, req)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(framesDir)
	return reportToResult(rep), nil
}

// reportToResult maps a chromesweep.Report to the proto SmokeTestResult shape.
func reportToResult(rep chromesweep.Report) *gubalv1.SmokeTestResult {
	result := &gubalv1.SmokeTestResult{
		Success: rep.AllPassed(),
		Report:  chromesweep.RenderMarkdown(rep),
		Results: make([]*gubalv1.ChromeVersionResult, 0, len(rep.Results)),
	}
	for _, r := range rep.Results {
		result.Results = append(result.Results, &gubalv1.ChromeVersionResult{
			Browser:        r.Browser,
			Tag:            r.Tag,
			Status:         sweepStatus(r.Status),
			BrowserVersion: r.BrowserVersion,
			ReportedUa:     r.ReportedUA,
			Detail:         r.Detail,
			Policy:         r.Policy,
		})
	}
	return result
}

// uploadBundle builds the report bundle and uploads it, returning its public URL
// or "" (best-effort; failures are logged, never fatal).
func (s *Server) uploadBundle(ctx context.Context, rep chromesweep.Report, framesDir, md string, pr int, id string) string {
	js, err := chromesweep.RenderJSON(rep)
	if err != nil {
		slog.ErrorContext(ctx, "rendering bundle json failed", "err", err)
		return ""
	}
	path := filepath.Join(framesDir, "report.zip")
	if err := chromesweep.WriteBundle(path, js, []byte(md), rep.Results); err != nil {
		slog.ErrorContext(ctx, "writing bundle failed", "err", err)
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.ErrorContext(ctx, "reading bundle failed", "err", err)
		return ""
	}
	url, err := s.uploader.Upload(ctx, bundleKey(pr, id), data, "application/zip")
	if err != nil {
		slog.ErrorContext(ctx, "uploading bundle failed", "pr", pr, "err", err)
		return ""
	}
	return url
}

// SubmitSmokeTest accepts a sweep, returns immediately, and runs it in the
// background, posting an ack comment on accept and a result comment on
// completion to the request's GitHub PR thread. A sweep already in progress is
// rejected (ResourceExhausted) with a best-effort busy note posted to the PR.
func (s *Server) SubmitSmokeTest(ctx context.Context, req *gubalv1.SubmitSmokeTestRequest) (*gubalv1.SubmitSmokeTestResponse, error) {
	if err := protovalidate.Validate(req); err != nil {
		return nil, twirp.NewError(twirp.InvalidArgument, err.Error())
	}

	gh := req.GetGithub()
	if _, ok := s.allowed[strings.ToLower(gh.GetRepo())]; !ok {
		return nil, twirp.NewError(twirp.PermissionDenied, "repo not in gubald's allowlist")
	}

	repo := gh.GetRepo()
	pr := int(gh.GetPrNumber())
	sha := gh.GetCommitSha()
	test := req.GetTest()

	// Try to become the single active sweep. If busy, post a best-effort note
	// and reject rather than queueing.
	select {
	case sweepSem <- struct{}{}:
		// acquired; released by the background goroutine below.
	default:
		if err := s.gh.Comment(ctx, repo, pr, bodyBusy()); err != nil {
			slog.WarnContext(ctx, "posting busy note failed", "repo", repo, "pr", pr, "err", err)
		}
		return nil, twirp.NewError(twirp.ResourceExhausted, "a smoke test is already running; try again later")
	}

	versionCount := len(test.GetChromeVersions()) + len(test.GetFirefoxVersions())

	go func() {
		defer func() { <-sweepSem }()

		bg, cancel := context.WithTimeout(context.Background(), s.jobDeadline)
		defer cancel()

		if err := s.gh.Comment(bg, repo, pr, bodyAck(sha, versionCount)); err != nil {
			slog.WarnContext(bg, "posting ack comment failed", "repo", repo, "pr", pr, "err", err)
		}

		rep, framesDir, err := s.executeSweep(bg, test)

		var body string
		if err != nil {
			slog.ErrorContext(bg, "background sweep failed", "repo", repo, "pr", pr, "err", err)
			body = bodyRunError(sha, err)
		} else {
			defer os.RemoveAll(framesDir)
			md := chromesweep.RenderMarkdown(rep)
			// Upload gets its own budget; a slow upload must not eat into the
			// time we need to post the result comment below.
			upCtx, upCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			bundleURL := s.uploadBundle(upCtx, rep, framesDir, md, pr, test.GetId())
			upCancel()
			body = bodyResult(rep.AllPassed(), md, sha, bundleURL)
		}
		// Fresh, independent context so the closure comment always posts regardless
		// of how long the sweep or upload took.
		postCtx, postCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer postCancel()
		if err := s.gh.Comment(postCtx, repo, pr, body); err != nil {
			slog.ErrorContext(postCtx, "posting result comment failed", "repo", repo, "pr", pr, "err", err)
		}
	}()

	return &gubalv1.SubmitSmokeTestResponse{Id: test.GetId(), Accepted: true}, nil
}

// browsersFor builds the browser targets for a request: Chrome + Firefox presets
// carrying the requested versions. Validation guarantees both lists are non-empty,
// but a browser with no versions is simply omitted so this degrades safely.
func browsersFor(req *gubalv1.SmokeTestRequest) ([]chromesweep.Browser, error) {
	var browsers []chromesweep.Browser
	if len(req.GetChromeVersions()) > 0 {
		vs, err := parseInts(req.GetChromeVersions())
		if err != nil {
			return nil, err
		}
		b := chromesweep.ChromeBrowser()
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(req.GetFirefoxVersions()) > 0 {
		vs, err := parseInts(req.GetFirefoxVersions())
		if err != nil {
			return nil, err
		}
		b := chromesweep.FirefoxBrowser()
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(browsers) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	return browsers, nil
}

// parseInts renders int32 majors as strings and runs them through ParseVersions
// (trim/dedupe/non-empty).
func parseInts(vs []int32) ([]string, error) {
	raw := make([]string, len(vs))
	for i, v := range vs {
		raw[i] = strconv.Itoa(int(v))
	}
	return chromesweep.ParseVersions(raw)
}

// sweepStatus maps a chromesweep status to its proto enum.
func sweepStatus(s chromesweep.Status) gubalv1.SweepStatus {
	switch s {
	case chromesweep.StatusPass:
		return gubalv1.SweepStatus_SWEEP_STATUS_PASS
	case chromesweep.StatusFail:
		return gubalv1.SweepStatus_SWEEP_STATUS_FAIL
	case chromesweep.StatusNotReady:
		return gubalv1.SweepStatus_SWEEP_STATUS_NOT_READY
	case chromesweep.StatusError:
		return gubalv1.SweepStatus_SWEEP_STATUS_ERROR
	default:
		return gubalv1.SweepStatus_SWEEP_STATUS_UNSPECIFIED
	}
}

// loadClientset builds a Kubernetes clientset, preferring in-cluster config and
// falling back to the ambient kubeconfig for local development.
func loadClientset() (kubernetes.Interface, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(restCfg)
}
