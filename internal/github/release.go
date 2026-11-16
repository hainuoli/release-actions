package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hainuoli/release-actions/internal/model"
)

type (
	// GitObject identifies a commit or annotated tag in the Git database.
	GitObject struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}

	// GitRef is an exact branch or tag reference.
	GitRef struct {
		Ref    string    `json:"ref"`
		Object GitObject `json:"object"`
	}

	// Release contains the immutable identity and text of a GitHub Release.
	Release struct {
		ID         int64  `json:"id"`
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
)

// GetRef resolves one exact ref, never a matching prefix.
func (c *Client) GetRef(ctx context.Context, repo model.Repository, ref string) (*GitRef, error) {
	var result GitRef
	err := c.request(ctx, "GET", repoPath(repo)+"/git/ref/"+escapePath(ref), nil, &result)
	if HasStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if result.Ref != "refs/"+ref || result.Object.SHA == "" {
		return nil, errors.New("unexpected Git reference")
	}
	return &result, nil
}

// CreateBranch creates a new branch without modifying an existing reference.
func (c *Client) CreateBranch(ctx context.Context, repo model.Repository, branch, sha string) error {
	return c.request(ctx, "POST", repoPath(repo)+"/git/refs", map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}, nil)
}

// ResolveTag follows bounded annotated tag chains to the tagged commit.
func (c *Client) ResolveTag(ctx context.Context, repo model.Repository, object GitObject) (string, error) {
	for depth := 0; depth < 8; depth++ {
		if object.Type == "commit" && object.SHA != "" {
			return object.SHA, nil
		}
		if object.Type != "tag" || object.SHA == "" {
			return "", errors.New("release tag must resolve to a commit")
		}
		var tag struct {
			Object GitObject `json:"object"`
		}
		if err := c.request(ctx, "GET", repoPath(repo)+"/git/tags/"+url.PathEscape(object.SHA), nil, &tag); err != nil {
			return "", err
		}
		object = tag.Object
	}
	return "", errors.New("annotated tag nesting exceeds limit")
}

// IsAncestor verifies that the tagged commit is reachable from the default branch.
func (c *Client) IsAncestor(ctx context.Context, repo model.Repository, ancestor, descendant string) (bool, error) {
	var comparison struct {
		Status string `json:"status"`
	}
	endpoint := repoPath(repo) + "/compare/" + url.PathEscape(ancestor) + "..." + url.PathEscape(descendant)
	if err := c.request(ctx, "GET", endpoint, nil, &comparison); err != nil {
		return false, err
	}
	return comparison.Status == "ahead" || comparison.Status == "identical", nil
}

// UpdateBranchFile uses Contents API SHA checks without checking out repository code.
func (c *Client) UpdateBranchFile(ctx context.Context, repo model.Repository, branch, path, sha, title, content string) error {
	repo.DefaultBranch = branch
	return c.UpdateFile(ctx, repo, path, sha, title, content)
}

// FindReleasePullRequest finds an open PR from an exact same-repository branch.
func (c *Client) FindReleasePullRequest(ctx context.Context, repo model.Repository, branch string) (*model.PullRequest, error) {
	owner, _, _ := strings.Cut(repo.FullName, "/")
	query := url.Values{
		"state":    {"open"},
		"head":     {owner + ":" + branch},
		"base":     {repo.DefaultBranch},
		"per_page": {"2"},
	}
	var prs []model.PullRequest
	if err := c.request(ctx, "GET", repoPath(repo)+"/pulls?"+query.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	if len(prs) != 1 || prs[0].Number <= 0 || prs[0].Head.Name != branch || prs[0].Base.Name != repo.DefaultBranch {
		return nil, errors.New("unexpected release pull request response")
	}
	return &prs[0], nil
}

// CreateReleasePullRequest opens a reviewable changelog-only release proposal.
func (c *Client) CreateReleasePullRequest(ctx context.Context, repo model.Repository, branch, title, body string) (model.PullRequest, error) {
	var pr model.PullRequest
	err := c.request(ctx, "POST", repoPath(repo)+"/pulls", map[string]string{
		"title": title,
		"body":  body,
		"head":  branch,
		"base":  repo.DefaultBranch,
	}, &pr)
	if err == nil && (pr.Number <= 0 || pr.Head.Name != branch || pr.Base.Name != repo.DefaultBranch) {
		return pr, errors.New("invalid created release pull request")
	}
	return pr, err
}

// EnsureReleaseLabel creates the skip-notes label if it is absent.
func (c *Client) EnsureReleaseLabel(ctx context.Context, repo model.Repository) error {
	err := c.request(ctx, "GET", repoPath(repo)+"/labels/release", nil, nil)
	if !HasStatus(err, http.StatusNotFound) {
		return err
	}
	err = c.request(ctx, "POST", repoPath(repo)+"/labels", map[string]string{
		"name":        "release",
		"color":       "5319e7",
		"description": "Release preparation; exclude from changelog entries",
	}, nil)
	if HasStatus(err, http.StatusUnprocessableEntity) {
		// A concurrent label creator may have won the race.
		return c.request(ctx, "GET", repoPath(repo)+"/labels/release", nil, nil)
	}
	return err
}

// GetRelease returns an existing release for a tag, or nil when absent.
func (c *Client) GetRelease(ctx context.Context, repo model.Repository, tag string) (*Release, error) {
	var release Release
	err := c.request(ctx, "GET", repoPath(repo)+"/releases/tags/"+url.PathEscape(tag), nil, &release)
	if HasStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if release.ID <= 0 || release.Tag != tag {
		return nil, errors.New("unexpected release response")
	}
	return &release, nil
}

// CreateRelease publishes notes for a tag that the caller has already verified.
func (c *Client) CreateRelease(ctx context.Context, repo model.Repository, tag, sha, body string, draft, prerelease bool) (*Release, error) {
	var release Release
	err := c.request(ctx, "POST", repoPath(repo)+"/releases", map[string]any{
		"tag_name":         tag,
		"target_commitish": sha,
		"name":             tag,
		"body":             body,
		"draft":            draft,
		"prerelease":       prerelease,
	}, &release)
	if err == nil && (release.ID <= 0 || release.Tag != tag) {
		return nil, errors.New("invalid created release")
	}
	return &release, err
}
