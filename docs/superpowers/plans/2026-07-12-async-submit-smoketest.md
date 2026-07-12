# Async `SubmitSmokeTest` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an async `SubmitSmokeTest` Twirp RPC to gubald that runs the browser sweep in the background and posts the report to a GitHub PR thread itself, so CI no longer holds a long-lived request open.

**Architecture:** Keep the synchronous `SmokeTest` RPC unchanged. Extract its sweep body into a shared `runSweep` helper. Add `SubmitSmokeTest`, which validates, enforces a repo allowlist, tries the existing serialization semaphore, and — if free — returns immediately while a background goroutine (on `context.Background()` + a deadline, not the request context) runs the sweep and posts an ack comment then a result comment via a `prCommenter` interface backed by go-github v89. gubalctl gains flags to pick the async path when a repo + PR number are present.

**Tech Stack:** Go 1.26.4, Twirp + `buf` (protovalidate), `github.com/google/go-github/v89/github`, `github.com/TecharoHQ/gubal/chromesweep`, flagenv.

## Global Constraints

- Go module `github.com/TecharoHQ/gubal`, Go 1.26.4.
- **Binaries build into `./var`, never the repo root.**
- All CLIs use flagenv: kebab-case flags map to `UPPER_SNAKE_CASE` env vars. Use `log/slog` (JSON to stderr), never `log`.
- protovalidate rules compile at first validation (runtime), so proto rules must be covered by a unit test. On a `repeated` field, per-element constraints go under `repeated.items`.
- Dependency pin: don't bump `buf.build/gen/go/bufbuild/protovalidate/...`; it must stay aligned with the version `within.website/x` uses.
- Regenerate protobuf/Twirp after editing `pb/*.proto`: `buf lint && buf generate` (outputs to `gen/`).
- The per-version NetworkPolicy and sweep serialization are load-bearing; do not change sweep orchestration behavior — only add the async wrapper.
- Test commands: `go test ./...`, `go vet ./...`.

## File Structure

- `pb/techaro/lol/gubal/v1/gubal.proto` — add `SubmitSmokeTest` RPC, `SubmitSmokeTestRequest`, `GitHubTarget`, `SubmitSmokeTestResponse`. (Task 1)
- `gen/techaro/lol/gubal/v1/*` — regenerated. (Task 1)
- `cmd/gubald/svc/smoketest/github.go` — `githubCommenter`, a go-github v89 wrapper implementing the `Comment` method. (Task 2)
- `cmd/gubald/svc/smoketest/comments.go` — pure comment-body builders. (Task 3)
- `cmd/gubald/svc/smoketest/comments_test.go` — tests for the body builders. (Task 3)
- `cmd/gubald/svc/smoketest/smoketest.go` — `prCommenter` interface, `New` signature, `runSweep` extraction, `SubmitSmokeTest` handler. (Tasks 4, 5)
- `cmd/gubald/svc/smoketest/smoketest_test.go` — proto-validation + handler unit tests. (Tasks 1, 5)
- `cmd/gubald/main.go` — new flags + wiring. (Task 4)
- `cmd/gubalctl/main.go` — async dispatch + flags. (Task 6)
- `cmd/gubalctl/main_test.go` — dispatch decision test. (Task 6)

---

### Task 1: Proto — add the async RPC and messages

**Files:**
- Modify: `pb/techaro/lol/gubal/v1/gubal.proto`
- Regenerate: `gen/techaro/lol/gubal/v1/*`
- Test: `cmd/gubald/svc/smoketest/smoketest_test.go`

**Interfaces:**
- Produces (Go types generated into `gubalv1`): `SubmitSmokeTestRequest{ Test *SmokeTestRequest; Github *GitHubTarget }`, `GitHubTarget{ Repo string; PrNumber int32; CommitSha string }`, `SubmitSmokeTestResponse{ Id string; Accepted bool }`, and a `SmokeTestService` interface method `SubmitSmokeTest(context.Context, *SubmitSmokeTestRequest) (*SubmitSmokeTestResponse, error)`.

- [ ] **Step 1: Add the RPC and messages to the proto**

In `pb/techaro/lol/gubal/v1/gubal.proto`, add the new RPC to the service and the three messages. Insert the RPC line inside `service SmokeTestService`:

```proto
service SmokeTestService {
  rpc SmokeTest(SmokeTestRequest) returns (SmokeTestResult) {}
  rpc SubmitSmokeTest(SubmitSmokeTestRequest) returns (SubmitSmokeTestResponse) {}
}
```

Add these messages after `SmokeTestResult` (before `enum SweepStatus`):

```proto
// SubmitSmokeTestRequest is the async counterpart of SmokeTestRequest: the same
// sweep parameters plus the GitHub PR thread gubald posts results to.
message SubmitSmokeTestRequest {
  SmokeTestRequest test = 1 [(buf.validate.field).required = true];
  GitHubTarget github = 2 [(buf.validate.field).required = true];
}

// GitHubTarget names the PR thread to comment on.
message GitHubTarget {
  // "owner/repo" — matches the GITHUB_REPO env var and gubald's allowlist format.
  string repo = 1 [
    (buf.validate.field).required = true,
    (buf.validate.field).string.pattern = "^[^/]+/[^/]+$"
  ];
  int32 pr_number = 2 [(buf.validate.field).int32.gt = 0];
  string commit_sha = 3; // optional, shown in the ack/report for traceability
}

message SubmitSmokeTestResponse {
  string id = 1;      // echoes test.id
  bool accepted = 2;  // true; a busy server returns a ResourceExhausted error instead
}
```

- [ ] **Step 2: Lint and regenerate**

Run: `cd /home/xe/code/TecharoHQ/gubal && buf lint && buf generate`
Expected: no output from lint (success); `gen/techaro/lol/gubal/v1/gubal.pb.go` and `gubal.twirp.go` now reference `SubmitSmokeTest`.

- [ ] **Step 3: Verify generation**

Run: `grep -c "SubmitSmokeTest" gen/techaro/lol/gubal/v1/gubal.twirp.go`
Expected: a non-zero count (the twirp client/server now include the method).

- [ ] **Step 4: Write the failing proto-validation test**

Append to `cmd/gubald/svc/smoketest/smoketest_test.go` a new test that exercises the new rules (this both guards the rules and confirms the generated types exist):

```go
// TestSubmitSmokeTestRequestValidation guards the buf.validate rules on the async
// request; protovalidate compiles rules at first validation, so a malformed rule
// would only surface here.
func TestSubmitSmokeTestRequestValidation(t *testing.T) {
	t.Parallel()

	valid := &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}}

	for _, tt := range []struct {
		name    string
		req     *gubalv1.SubmitSmokeTestRequest
		wantErr bool
	}{
		{
			name: "valid",
			req:  &gubalv1.SubmitSmokeTestRequest{Test: valid, Github: &gubalv1.GitHubTarget{Repo: "TecharoHQ/anubis", PrNumber: 1741}},
		},
		{
			name:    "missing github",
			req:     &gubalv1.SubmitSmokeTestRequest{Test: valid},
			wantErr: true,
		},
		{
			name:    "repo without slash",
			req:     &gubalv1.SubmitSmokeTestRequest{Test: valid, Github: &gubalv1.GitHubTarget{Repo: "anubis", PrNumber: 1}},
			wantErr: true,
		},
		{
			name:    "pr number not positive",
			req:     &gubalv1.SubmitSmokeTestRequest{Test: valid, Github: &gubalv1.GitHubTarget{Repo: "TecharoHQ/anubis", PrNumber: 0}},
			wantErr: true,
		},
		{
			name:    "bad inner test",
			req:     &gubalv1.SubmitSmokeTestRequest{Test: &gubalv1.SmokeTestRequest{Id: "nope"}, Github: &gubalv1.GitHubTarget{Repo: "TecharoHQ/anubis", PrNumber: 1}},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := protovalidate.Validate(tt.req)
			if (err != nil) != tt.wantErr {
				t.Logf("want error: %v", tt.wantErr)
				t.Logf("got:  %v", err)
				t.Fatal("wrong validation result")
			}
		})
	}
}
```

- [ ] **Step 5: Run the validation test**

Run: `go test ./cmd/gubald/svc/smoketest/ -run TestSubmitSmokeTestRequestValidation -v`
Expected: PASS (all sub-tests). If it fails to compile, the generated types are missing — re-run Step 2.

- [ ] **Step 6: Commit**

```bash
git add pb/techaro/lol/gubal/v1/gubal.proto gen/techaro/lol/gubal/v1/ cmd/gubald/svc/smoketest/smoketest_test.go
git commit -m "feat(pb): add async SubmitSmokeTest RPC and GitHub target"
```

---

### Task 2: GitHub commenter (go-github v89 wrapper)

**Files:**
- Create: `cmd/gubald/svc/smoketest/github.go`
- Modify: `go.mod`, `go.sum`
- Test: `cmd/gubald/svc/smoketest/github_test.go`

**Interfaces:**
- Produces: `func newGitHubCommenter(token string) (*githubCommenter, error)` and method `func (g *githubCommenter) Comment(ctx context.Context, repo string, pr int, body string) error`. The `Comment` signature must exactly match the `prCommenter` interface defined in Task 4. Also produces helper `func splitRepo(repo string) (owner, name string, err error)`.

> **API note (go-github v89):** `github.NewClient` takes functional options and returns `(*Client, error)` — `func NewClient(opts ...ClientOptionsFunc) (*Client, error)`. `WithAuthToken(token string) ClientOptionsFunc` is a package-level option, **not** a method. Construct with `github.NewClient(github.WithAuthToken(token))`. Because that returns an error, `newGitHubCommenter` returns `(*githubCommenter, error)`.

- [ ] **Step 1: Add the go-github v89 dependency**

Run:
```bash
cd /home/xe/code/TecharoHQ/gubal
go get github.com/google/go-github/v89@latest
```
Expected: `go.mod` gains `github.com/google/go-github/v89`. Do not touch the protovalidate pin.

- [ ] **Step 2: Write the failing test for `splitRepo`**

Create `cmd/gubald/svc/smoketest/github_test.go`:

```go
package smoketest

import "testing"

func TestSplitRepo(t *testing.T) {
	t.Parallel()

	owner, name, err := splitRepo("TecharoHQ/anubis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "TecharoHQ" || name != "anubis" {
		t.Fatalf("got %q/%q", owner, name)
	}

	if _, _, err := splitRepo("anubis"); err == nil {
		t.Fatal("expected error for repo without a slash")
	}
	if _, _, err := splitRepo("a/b/c"); err == nil {
		t.Fatal("expected error for repo with two slashes")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/gubald/svc/smoketest/ -run TestSplitRepo`
Expected: FAIL — `undefined: splitRepo`.

- [ ] **Step 4: Write `github.go`**

Create `cmd/gubald/svc/smoketest/github.go`:

```go
package smoketest

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v89/github"
)

// githubCommenter posts PR-thread comments using a GitHub token. A pull request
// is an "issue" for comment purposes, so it uses the Issues API.
type githubCommenter struct {
	client *github.Client
}

// newGitHubCommenter builds a commenter authenticated with the given token.
func newGitHubCommenter(token string) (*githubCommenter, error) {
	client, err := github.NewClient(github.WithAuthToken(token))
	if err != nil {
		return nil, fmt.Errorf("building github client: %w", err)
	}
	return &githubCommenter{client: client}, nil
}

// Comment posts body to PR pr in "owner/repo" repo.
func (g *githubCommenter) Comment(ctx context.Context, repo string, pr int, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	_, _, err = g.client.Issues.CreateComment(ctx, owner, name, pr, &github.IssueComment{
		Body: github.Ptr(body),
	})
	if err != nil {
		return fmt.Errorf("posting comment to %s#%d: %w", repo, pr, err)
	}
	return nil
}

// splitRepo splits an "owner/repo" string into its parts.
func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo %q is not in owner/repo form", repo)
	}
	return parts[0], parts[1], nil
}
```

- [ ] **Step 5: Run the test to verify it passes and tidy modules**

Run:
```bash
go test ./cmd/gubald/svc/smoketest/ -run TestSplitRepo && go mod tidy
```
Expected: PASS. `go mod tidy` settles `go.sum`. Confirm the protovalidate pin is unchanged: `git diff go.mod | grep -i protovalidate` should print nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/gubald/svc/smoketest/github.go cmd/gubald/svc/smoketest/github_test.go go.mod go.sum
git commit -m "feat(gubald): go-github v89 PR-comment wrapper"
```

---

### Task 3: Comment-body builders

**Files:**
- Create: `cmd/gubald/svc/smoketest/comments.go`
- Test: `cmd/gubald/svc/smoketest/comments_test.go`

**Interfaces:**
- Produces:
  - `func bodyAck(sha string, versionCount int) string`
  - `func bodyBusy() string`
  - `func bodyResult(success bool, report, sha string) string`
  - `func bodyRunError(sha string, err error) string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/gubald/svc/smoketest/comments_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/gubald/svc/smoketest/ -run 'TestBody'`
Expected: FAIL — `undefined: bodyAck` (etc).

- [ ] **Step 3: Write `comments.go`**

Create `cmd/gubald/svc/smoketest/comments.go`:

```go
package smoketest

import "fmt"

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

// bodyResult is posted when the sweep finishes. success drives the header emoji;
// report is the rendered Markdown table.
func bodyResult(success bool, report, sha string) string {
	header := "✅ Gubal smoke test passed"
	if !success {
		header = "❌ Gubal smoke test found failures"
	}
	return fmt.Sprintf("%s%s\n\n%s", header, shaSuffix(sha), report)
}

// bodyRunError is posted when the sweep itself could not run (infra error).
func bodyRunError(sha string, err error) string {
	return fmt.Sprintf("❌ Gubal smoke test failed to run%s: %v", shaSuffix(sha), err)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/gubald/svc/smoketest/ -run 'TestBody'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gubald/svc/smoketest/comments.go cmd/gubald/svc/smoketest/comments_test.go
git commit -m "feat(gubald): comment-body builders for async smoke test"
```

---

### Task 4: Server refactor + main.go wiring (no new behavior)

This task changes `New`'s signature and extracts `runSweep`, then updates `main.go` so everything compiles. No async handler yet — that keeps this task's deliverable a pure refactor whose existing tests still pass.

**Files:**
- Modify: `cmd/gubald/svc/smoketest/smoketest.go`
- Modify: `cmd/gubald/main.go`

**Interfaces:**
- Consumes: `githubCommenter.Comment` (Task 2) via the `prCommenter` interface; `bodyResult`/`bodyAck`/etc. exist (Task 3) but are used in Task 5.
- Produces:
  - `type prCommenter interface { Comment(ctx context.Context, repo string, pr int, body string) error }`
  - `func New(gh prCommenter, allowedRepos []string, jobDeadline time.Duration) *Server`
  - `func (s *Server) runSweep(ctx context.Context, req *gubalv1.SmokeTestRequest) (*gubalv1.SmokeTestResult, error)` — the former body of `SmokeTest`, minus validation and semaphore handling.
  - `Server` struct fields: `gh prCommenter`, `allowed map[string]struct{}` (lower-cased repo keys), `jobDeadline time.Duration`.

- [ ] **Step 1: Refactor `smoketest.go` — struct, `New`, `runSweep`**

In `cmd/gubald/svc/smoketest/smoketest.go`:

Add imports `"strings"` and `"time"` to the import block (keep existing imports).

Replace the `Server`/`New` block and add the interface:

```go
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
```

Now extract `runSweep`. Change the existing `SmokeTest` method body so everything after the semaphore acquisition is delegated. Replace the current `SmokeTest` method with:

```go
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
```

Leave `sweepSem`, `browsersFor`, `parseInts`, `sweepStatus`, `loadClientset` exactly as they are.

- [ ] **Step 2: Update `main.go` flags and wiring**

In `cmd/gubald/main.go`, add to the `var (...)` flag block:

```go
	githubToken   = flag.String("github-token", "", "GitHub token gubald posts PR comments with")
	githubRepos   = flag.String("github-repos", "TecharoHQ/anubis", "comma-separated owner/repo allowlist for async submits")
	jobDeadline   = flag.Duration("job-deadline", 60*time.Minute, "max wall-clock for a background smoke-test sweep")
```

Add `"strings"` and `"time"` to the imports.

Replace the `smokeTest := smoketest.New()` line with:

```go
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
```

Note: `run` already has an `err` in scope from the `sigv4aclient.NewSigV4ARoundTripper` call above, so use `=` if reusing it or keep `:=` if the linter prefers — adjust to compile. `fmt` is already imported in `main.go`.

- [ ] **Step 3: Export the commenter constructor**

`main.go` is a different package, so the constructor must be exported. In `cmd/gubald/svc/smoketest/github.go`, rename `newGitHubCommenter` to `NewGitHubCommenter` (exported, still returning `(*githubCommenter, error)`) and update the doc comment's leading word to match. Returning an unexported `*githubCommenter` from an exported function is fine — `main.go` holds it via `:=` and passes it to `New` (which takes the `prCommenter` interface). (The `github_test.go` from Task 2 did not call it, so no test change is needed.)

- [ ] **Step 4: Build and run existing tests**

Run:
```bash
cd /home/xe/code/TecharoHQ/gubal
go build -o ./var/gubald ./cmd/gubald && go test ./cmd/gubald/... && go vet ./cmd/gubald/...
```
Expected: builds into `./var`; all existing smoketest tests (validation, browsersFor, splitRepo, body builders) PASS; vet clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/gubald/svc/smoketest/smoketest.go cmd/gubald/svc/smoketest/github.go cmd/gubald/main.go
git commit -m "refactor(gubald): inject prCommenter + allowlist, extract runSweep"
```

---

### Task 5: `SubmitSmokeTest` handler + unit tests

**Files:**
- Modify: `cmd/gubald/svc/smoketest/smoketest.go`
- Test: `cmd/gubald/svc/smoketest/submit_test.go`

**Interfaces:**
- Consumes: `prCommenter`, `New`, `runSweep` (Task 4); `bodyAck`/`bodyBusy`/`bodyResult`/`bodyRunError` (Task 3); `sweepSem` (existing package var).
- Produces: `func (s *Server) SubmitSmokeTest(ctx context.Context, req *gubalv1.SubmitSmokeTestRequest) (*gubalv1.SubmitSmokeTestResponse, error)`.

- [ ] **Step 1: Write the failing handler tests**

Create `cmd/gubald/svc/smoketest/submit_test.go`. The fake commenter records posts; the busy test pre-fills the package semaphore so the sweep never starts; the allowlist test asserts no sweep runs (a rejected repo returns before `runSweep`, which would otherwise try to build a kube client).

```go
package smoketest

import (
	"context"
	"sync"
	"testing"
	"time"

	gubalv1 "github.com/TecharoHQ/gubal/gen/techaro/lol/gubal/v1"
	"github.com/google/uuid"
	"github.com/twitchtv/twirp"
)

type fakeCommenter struct {
	mu     sync.Mutex
	bodies []string
}

func (f *fakeCommenter) Comment(_ context.Context, _ string, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *fakeCommenter) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func validSubmit() *gubalv1.SubmitSmokeTestRequest {
	return &gubalv1.SubmitSmokeTestRequest{
		Test:   &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}},
		Github: &gubalv1.GitHubTarget{Repo: "TecharoHQ/anubis", PrNumber: 1741},
	}
}

func TestSubmitRejectsDisallowedRepo(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, []string{"TecharoHQ/anubis"}, time.Minute)

	req := validSubmit()
	req.Github.Repo = "evil/repo"

	_, err := s.SubmitSmokeTest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for disallowed repo")
	}
	if twirp.ErrorCode(err) != twirp.PermissionDenied {
		t.Fatalf("code = %v, want permission_denied", twirp.ErrorCode(err))
	}
	if got := fc.all(); len(got) != 0 {
		t.Fatalf("no comment expected for a disallowed repo, got %v", got)
	}
}

func TestSubmitBusyPostsNote(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, []string{"TecharoHQ/anubis"}, time.Minute)

	// Occupy the sweep semaphore so the submit sees the server as busy.
	sweepSem <- struct{}{}
	defer func() { <-sweepSem }()

	_, err := s.SubmitSmokeTest(context.Background(), validSubmit())
	if err == nil {
		t.Fatal("expected busy error")
	}
	if twirp.ErrorCode(err) != twirp.ResourceExhausted {
		t.Fatalf("code = %v, want resource_exhausted", twirp.ErrorCode(err))
	}
	got := fc.all()
	if len(got) != 1 {
		t.Fatalf("want exactly one (busy) comment, got %v", got)
	}
	if !contains(got[0], "⏳") {
		t.Fatalf("busy comment missing marker: %q", got[0])
	}
}

func TestSubmitInvalidRequest(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, []string{"TecharoHQ/anubis"}, time.Minute)

	_, err := s.SubmitSmokeTest(context.Background(), &gubalv1.SubmitSmokeTestRequest{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if twirp.ErrorCode(err) != twirp.InvalidArgument {
		t.Fatalf("code = %v, want invalid_argument", twirp.ErrorCode(err))
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (stringIndex(s, sub) >= 0) }

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

Note: the busy test relies on the async handler acquiring the *package-level* `sweepSem` before spawning its goroutine, and posting the busy note on the reject path. The allowlist and invalid-request tests return before any semaphore or sweep interaction.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/gubald/svc/smoketest/ -run TestSubmit`
Expected: FAIL — `s.SubmitSmokeTest undefined`.

- [ ] **Step 3: Implement `SubmitSmokeTest`**

Append to `cmd/gubald/svc/smoketest/smoketest.go` (add `"context"` and `"log/slog"` and `"strings"` to imports if not already present — `context`, `fmt`, `os`, `strconv` already are; add `slog`, `strings`, `time` as needed):

```go
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
		if err := s.gh.Comment(bg, repo, pr, body); err != nil {
			slog.ErrorContext(bg, "posting result comment failed", "repo", repo, "pr", pr, "err", err)
		}
	}()

	return &gubalv1.SubmitSmokeTestResponse{Id: test.GetId(), Accepted: true}, nil
}
```

Ensure the import block includes `"log/slog"`, `"strings"`, and `"time"` (Task 4 already added `strings` and `time`; add `log/slog`).

- [ ] **Step 4: Run the handler tests**

Run: `go test ./cmd/gubald/svc/smoketest/ -run TestSubmit -v`
Expected: PASS. `TestSubmitRejectsDisallowedRepo` and `TestSubmitInvalidRequest` return before any goroutine; `TestSubmitBusyPostsNote` sees the pre-filled semaphore and posts the busy note.

- [ ] **Step 5: Full package test + vet + build**

Run:
```bash
go test ./cmd/gubald/... && go vet ./cmd/gubald/... && go build -o ./var/gubald ./cmd/gubald
```
Expected: all PASS; binary in `./var`.

- [ ] **Step 6: Commit**

```bash
git add cmd/gubald/svc/smoketest/smoketest.go cmd/gubald/svc/smoketest/submit_test.go
git commit -m "feat(gubald): async SubmitSmokeTest posts results to the PR thread"
```

---

### Task 6: gubalctl async dispatch

**Files:**
- Modify: `cmd/gubalctl/main.go`
- Test: `cmd/gubalctl/main_test.go`

**Interfaces:**
- Consumes: generated `SmokeTestServiceProtobufClient` (now includes `SubmitSmokeTest`), `SubmitSmokeTestRequest`, `GitHubTarget`.
- Produces: `func wantsAsync(githubRepo string, prNumber int) bool` and a branch in `run` that calls `SubmitSmokeTest` when async.

- [ ] **Step 1: Write the failing dispatch test**

Create or append to `cmd/gubalctl/main_test.go`:

```go
package main

import "testing"

func TestWantsAsync(t *testing.T) {
	t.Parallel()
	cases := []struct {
		repo string
		pr   int
		want bool
	}{
		{"TecharoHQ/anubis", 1741, true},
		{"", 1741, false},
		{"TecharoHQ/anubis", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		if got := wantsAsync(c.repo, c.pr); got != c.want {
			t.Fatalf("wantsAsync(%q,%d) = %v, want %v", c.repo, c.pr, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/gubalctl/ -run TestWantsAsync`
Expected: FAIL — `undefined: wantsAsync`.

- [ ] **Step 3: Add flags, `wantsAsync`, and the async branch**

In `cmd/gubalctl/main.go` add to the `var (...)` flag block:

```go
	githubRepo = flag.String("github-repo", "", "owner/repo of the PR to post results to (env: GITHUB_REPO); enables async mode with -pr-number")
	prNumber   = flag.Int("pr-number", 0, "PR number to post results to (env: PR_NUMBER)")
	commitSHA  = flag.String("commit-sha", "", "commit SHA under test, shown in the report (env: GITHUB_SHA)")
```

Add the helper near the bottom of the file:

```go
// wantsAsync reports whether the caller supplied enough to post to a PR thread,
// in which case gubalctl submits asynchronously instead of blocking on a sweep.
func wantsAsync(githubRepo string, prNumber int) bool {
	return githubRepo != "" && prNumber > 0
}
```

In `run`, after building `client` (the `NewSmokeTestServiceProtobufClient` line) and before the current synchronous `client.SmokeTest(...)` call, insert the async branch:

```go
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
```

Leave the existing synchronous path below unchanged.

- [ ] **Step 4: Run the dispatch test + build**

Run:
```bash
go test ./cmd/gubalctl/ && go build -o ./var/gubalctl ./cmd/gubalctl && go vet ./cmd/gubalctl/...
```
Expected: PASS; binary in `./var`; vet clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/gubalctl/main.go cmd/gubalctl/main_test.go
git commit -m "feat(gubalctl): async submit when a PR repo + number are given"
```

---

### Task 7: Full verification + CI workflow doc

**Files:**
- Modify: `docs/superpowers/plans/2026-07-12-async-submit-smoketest.md` (append the CI snippet below is already in the spec; no code change)

- [ ] **Step 1: Whole-repo test, vet, build**

Run:
```bash
cd /home/xe/code/TecharoHQ/gubal
go test ./... && go vet ./... && go build -o ./var/gubald ./cmd/gubald && go build -o ./var/gubalctl ./cmd/gubalctl
```
Expected: all PASS; both binaries in `./var`.

- [ ] **Step 2: Validate the real GitHub path against PR #1741**

This is the one live, outward-facing step. Confirm the exact comment body with the user first, then post a sample result comment to `TecharoHQ/anubis#1741` using the token in `.env` (via the `githubCommenter`, e.g. a tiny throwaway `main` or a `go test`-guarded manual check). Verify the comment appears on the PR, then stop.

- [ ] **Step 3: Note the CI change (Anubis repo, not this repo)**

The Anubis `.github/workflows/gubal.yml` "Run gubal test" step becomes a single fast `gubalctl` call with `GITHUB_REPO`/`GITHUB_SHA`/`PR_NUMBER` env set, dropping the `gh pr comment` line and the step's `GITHUB_TOKEN`. Full snippet is in the spec (`docs/superpowers/specs/2026-07-12-async-submit-smoketest-design.md`, §5). No change lands in this repo.

---

## Self-Review

- **Spec coverage:** proto (Task 1), go-github wrapper (Task 2), comment bodies (Task 3), server refactor + wiring + allowlist (Task 4), async handler + busy/allowlist behavior + background context/deadline (Task 5), gubalctl dispatch (Task 6), verification + CI doc + PR #1741 validation (Task 7). All spec sections mapped.
- **Type consistency:** `Comment(ctx, repo string, pr int, body string) error` is identical across `prCommenter` (Task 4), `githubCommenter` (Task 2), and `fakeCommenter` (Task 5). `New(prCommenter, []string, time.Duration)` matches its call sites in `main.go` (Task 4) and tests (Task 5). `runSweep(ctx, *SmokeTestRequest)` defined in Task 4, used in Task 5. Body builders defined in Task 3, used in Task 5.
- **Placeholders:** none — every step has concrete code or an exact command.
