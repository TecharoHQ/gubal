package smoketest

import (
	"context"
	"strings"
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

// errCode extracts the twirp.ErrorCode from err. This twirp version has no
// package-level twirp.ErrorCode(err) helper; codes live on the twirp.Error
// interface returned by twirp.NewError and friends.
func errCode(err error) twirp.ErrorCode {
	if twerr, ok := err.(twirp.Error); ok {
		return twerr.Code()
	}
	return ""
}

func validSubmit() *gubalv1.SubmitSmokeTestRequest {
	return &gubalv1.SubmitSmokeTestRequest{
		Test:   &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{"default-config": "bots: []"}},
		Github: &gubalv1.GitHubTarget{Repo: "TecharoHQ/anubis", PrNumber: 1741},
	}
}

func TestSubmitRejectsDisallowedRepo(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, noopUploader{}, []string{"TecharoHQ/anubis"}, time.Minute)

	req := validSubmit()
	req.Github.Repo = "evil/repo"

	_, err := s.SubmitSmokeTest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for disallowed repo")
	}
	if errCode(err) != twirp.PermissionDenied {
		t.Fatalf("code = %v, want permission_denied", errCode(err))
	}
	if got := fc.all(); len(got) != 0 {
		t.Fatalf("no comment expected for a disallowed repo, got %v", got)
	}
}

func TestSubmitBusyPostsNote(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, noopUploader{}, []string{"TecharoHQ/anubis"}, time.Minute)

	// Occupy the sweep semaphore so the submit sees the server as busy.
	sweepSem <- struct{}{}
	defer func() { <-sweepSem }()

	_, err := s.SubmitSmokeTest(context.Background(), validSubmit())
	if err == nil {
		t.Fatal("expected busy error")
	}
	if errCode(err) != twirp.ResourceExhausted {
		t.Fatalf("code = %v, want resource_exhausted", errCode(err))
	}
	got := fc.all()
	if len(got) != 1 {
		t.Fatalf("want exactly one (busy) comment, got %v", got)
	}
	if !strings.Contains(got[0], "⏳") {
		t.Fatalf("busy comment missing marker: %q", got[0])
	}
}

func TestSubmitInvalidRequest(t *testing.T) {
	fc := &fakeCommenter{}
	s := New(fc, noopUploader{}, []string{"TecharoHQ/anubis"}, time.Minute)

	_, err := s.SubmitSmokeTest(context.Background(), &gubalv1.SubmitSmokeTestRequest{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if errCode(err) != twirp.InvalidArgument {
		t.Fatalf("code = %v, want invalid_argument", errCode(err))
	}
}
