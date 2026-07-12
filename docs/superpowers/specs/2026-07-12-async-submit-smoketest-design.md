# Async `SubmitSmokeTest` that posts results to a GitHub PR thread

Date: 2026-07-12

## Problem

CI in the Anubis repo (`.github/workflows/gubal.yml`) reacts to a `/gubaltest`
PR comment by running `gubalctl`, which makes a **synchronous** `SmokeTest`
Twirp call to `gubald.xeserv.us` and waits for the full sweep to finish before
piping the returned Markdown into `gh pr comment`. A sweep runs the whole
policy × browser × version matrix on a real cluster and takes many minutes, so
the long-lived HTTP request times out in CI.

## Goal

Let gubald run the sweep **in the background** and post the report to the PR
thread **itself**, so the client call returns immediately and nothing has to
hold a request open for the duration.

## Decisions (settled during brainstorming)

- **Keep the synchronous `SmokeTest` RPC** for manual/CLI use. Add a new async
  `SubmitSmokeTest` RPC that returns immediately and posts to GitHub.
- **gubald holds its own GitHub token** (from its environment) and posts as
  itself. CI no longer needs to run `gh pr comment`.
- **Two comments per run:** an ack comment when the job is accepted, and a
  result comment when the sweep finishes (including on failure, so the PR always
  gets closure).
- **When busy** (a sweep already holds the serialization semaphore): reject the
  submit with a `ResourceExhausted` error **and** best-effort post a note comment
  to the PR (`⏳ busy, re-run /gubaltest later`).
- **Repo allowlist**, configurable, **defaulting to `TecharoHQ/anubis`**. Submits
  for any other repo are rejected before the sweep starts.
- **GitHub client:** `github.com/google/go-github/v89/github`.

## Design

### 1. Proto (`pb/techaro/lol/gubal/v1/gubal.proto`)

`SmokeTest` and its request/response are unchanged. Add:

```proto
service SmokeTestService {
  rpc SmokeTest(SmokeTestRequest) returns (SmokeTestResult) {}                      // unchanged, sync
  rpc SubmitSmokeTest(SubmitSmokeTestRequest) returns (SubmitSmokeTestResponse) {}  // async
}

message SubmitSmokeTestRequest {
  SmokeTestRequest test = 1 [(buf.validate.field).required = true];
  GitHubTarget github = 2 [(buf.validate.field).required = true];
}

message GitHubTarget {
  // "owner/repo" — matches the GITHUB_REPO env var and the allowlist format.
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

Regenerate with `buf lint && buf generate` (outputs to `gen/`).

Composing the existing `SmokeTestRequest` inside `SubmitSmokeTestRequest` keeps
one source of truth for the sweep parameters and lets the GitHub target carry
its own `required` rules without touching the sync request's validation.

### 2. gubald server (`cmd/gubald/svc/smoketest/`)

**Refactor first:** extract the current body of `SmokeTest` (everything after
validation: `browsersFor` → `loadClientset` → build `Config` → `chromesweep.Run`
→ map to `SmokeTestResult`) into a shared helper:

```go
func (s *Server) runSweep(ctx context.Context, req *gubalv1.SmokeTestRequest) (*gubalv1.SmokeTestResult, error)
```

`SmokeTest` becomes: validate → acquire semaphore (as today) → `runSweep` →
return. Behavior is unchanged.

**Server dependencies.** `Server` gains an injected GitHub commenter and an
allowlist, set via `New`:

```go
type prCommenter interface {
    Comment(ctx context.Context, repo string, pr int, body string) error
}

func New(gh prCommenter, allowedRepos []string, jobDeadline time.Duration) *Server
```

The real `prCommenter` wraps `go-github/v89`
(`client.Issues.CreateComment(ctx, owner, repo, pr, &github.IssueComment{Body: &body})`;
a PR is an issue for comment purposes). Splitting `"owner/repo"` on `/` happens
in the wrapper. The interface makes the handler unit-testable with a fake.

**`SubmitSmokeTest` handler:**

1. `protovalidate.Validate(req)` → `InvalidArgument` on failure.
2. Reject if `req.Github.Repo` is not in the allowlist → `PermissionDenied`
   (case-insensitive exact match on `owner/repo`). No sweep, no comment.
3. **Try-acquire the sweep semaphore, non-blocking.** If held: best-effort
   `Comment(... busy note ...)` (a failed post is logged, not fatal), then return
   `ResourceExhausted`.
4. Acquired: launch a background goroutine and return `{id: test.id, accepted: true}`
   immediately. The goroutine:
   - runs on `context.Background()` with a `jobDeadline` timeout — **not** the
     request context, which is cancelled when the response is sent;
   - `defer`s releasing the semaphore;
   - posts the `🧪 Running…` ack comment (best-effort);
   - calls `runSweep`;
   - posts the result comment: on success the rendered Markdown report
     (`chromesweep.RenderMarkdown`, same as the sync path), on error a failure
     comment carrying the error detail.

**Comment bodies** are built by small pure helpers (ack / busy / result-success
/ result-failure) so they can be unit-tested without a cluster or network. The
optional `commit_sha` is included in the ack/result header when present.

### 3. Wiring (`cmd/gubald/main.go`)

New flagenv flags (kebab-case → UPPER_SNAKE env, per repo convention):

- `-github-token` (env `GITHUB_TOKEN`) — token gubald posts with.
- `-github-repos` (env `GITHUB_REPOS`, default `TecharoHQ/anubis`) —
  comma-separated allowlist.
- `-job-deadline` (default `60m`) — background sweep timeout.

`main.go` builds the `go-github` client from the token, constructs the real
`prCommenter`, parses the allowlist, and passes them to `smoketest.New`.

### 4. gubalctl (`cmd/gubalctl/main.go`)

Add flags, all already exported by the CI job:

- `-github-repo` (env `GITHUB_REPO`)
- `-pr-number` (env `PR_NUMBER`)
- `-commit-sha` (env `GITHUB_SHA`)

Dispatch: if **both** `-github-repo` and `-pr-number` are set, call
`SubmitSmokeTest`, print the returned job id, and exit immediately. Otherwise
fall back to today's synchronous `SmokeTest` (prints the report, exits non-zero
on failure). Existing manual invocations are unaffected.

### 5. CI workflow (in the Anubis repo — documented here, not edited in this repo)

The run step collapses to a single fast call; the `gh pr comment` line and the
step's `GITHUB_TOKEN` are removed (gubald posts now):

```yaml
- name: Run gubal test
  run: |
    gubalctl --url https://gubald.xeserv.us \
      --chrome-versions 75,80,...,150 \
      --anubis-image ttl.sh/techaro/pr-${PR_NUMBER}/anubis:24h
  env:
    GITHUB_REPO: ${{ github.repository }}
    GITHUB_SHA:  ${{ github.event.pull_request.head.sha || github.sha }}
    PR_NUMBER:   ${{ github.event.issue.number || github.event.pull_request.number }}
    ACCESS_KEY_ID: ${{ secrets.GUBALD_ACCESS_KEY_ID }}
    SECRET_ACCESS_KEY: ${{ secrets.GUBALD_SECRET_ACCESS_KEY }}
```

No long-lived request → no timeout. Results arrive as PR comments from gubald.

### 6. Testing

- **Unit (no cluster), with a fake `prCommenter`:**
  - allowlist rejection: non-allowed repo → error, sweep never runs, no comment;
  - busy path: semaphore held → note comment posted + `ResourceExhausted`;
  - comment-body helpers: correct ack / busy / success / failure text, and
    `commit_sha` included when present.
- **Proto validation:** extend the existing runtime-compilation test to cover
  `SubmitSmokeTestRequest` / `GitHubTarget` (per CLAUDE.md, protovalidate rules
  compile at first validation).
- The sweep itself (`chromesweep.Run`) remains cluster-only / integration, as
  today.

### 7. Dependency

Add `github.com/google/go-github/v89` as a direct dependency (`go get`), then
`go mod tidy`. The graph currently vendors `v81` transitively; `v89` is added
explicitly and used only in gubald's GitHub wrapper.

## Known limitation

Job state is in-memory. If gubald restarts mid-sweep, the run is lost and its
ack comment stays stuck on "running". Acceptable for a smoke-test tool; no
durable job store (YAGNI).

## Validation

After implementation, exercise the real GitHub path by posting a sample result
comment to `TecharoHQ/anubis#1741` using the token in `.env` (via go-github),
confirming the exact body before posting anything public.
