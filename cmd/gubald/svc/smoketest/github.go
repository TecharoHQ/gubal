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
