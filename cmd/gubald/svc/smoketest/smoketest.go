package smoketest

import (
	"context"
	"fmt"
	"os"
	"strconv"

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

type Server struct {
	gubalv1.UnimplementedSmokeTestServiceServer
}

func New() *Server {
	result := &Server{}

	return result
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
		})
	}

	return result, nil
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
