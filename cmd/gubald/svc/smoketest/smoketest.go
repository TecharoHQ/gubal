package smoketest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
	allowed     map[string]struct{}
	jobDeadline time.Duration
}

// New builds a Server. gh posts async results to GitHub; allowedRepos is the
// "owner/repo" allowlist SubmitSmokeTest enforces (matched case-insensitively);
// jobDeadline bounds a background sweep.
func New(gh prCommenter, allowedRepos []string, jobDeadline time.Duration) *Server {
	allowed := make(map[string]struct{}, len(allowedRepos))
	for _, r := range allowedRepos {
		r = strings.TrimSpace(r)
		if r != "" {
			allowed[strings.ToLower(r)] = struct{}{}
		}
	}
	return &Server{gh: gh, allowed: allowed, jobDeadline: jobDeadline}
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

// runSweep runs the browser sweep for req and maps it to a SmokeTestResult. It
// assumes the caller already validated req and holds the sweep semaphore.
func (s *Server) runSweep(ctx context.Context, req *gubalv1.SmokeTestRequest) (*gubalv1.SmokeTestResult, error) {
	browsers, err := browsersFor(req)
	if err != nil {
		return nil, twirp.NewError(twirp.InvalidArgument, err.Error())
	}

	cs, err := loadClientset()
	if err != nil {
		return nil, twirp.InternalErrorWith(fmt.Errorf("building kube client: %w", err))
	}

	cfg := chromesweep.DefaultConfig()
	cfg.AnubisImage = req.GetAnubisImage()
	cfg.Browsers = browsers

	framesDir, err := os.MkdirTemp("", "smoketest-frames-")
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	defer os.RemoveAll(framesDir)

	rep, err := chromesweep.Run(ctx, chromesweep.NewCluster(cs, cfg.Namespace), cfg, framesDir)
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}

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
	return result, nil
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

		result, err := s.runSweep(bg, test)
		var body string
		if err != nil {
			slog.ErrorContext(bg, "background sweep failed", "repo", repo, "pr", pr, "err", err)
			body = bodyRunError(sha, err)
		} else {
			body = bodyResult(result.GetSuccess(), result.GetReport(), sha)
		}
		// Post the closure comment on a fresh context: bg may already be expired
		// if the sweep ran to jobDeadline, and the PR must still get a result.
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
