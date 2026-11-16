// Package release prepares reviewed changelog releases and publishes tagged notes.
package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hainuoli/release-actions/internal/github"
	"github.com/hainuoli/release-actions/internal/labels"
	"github.com/hainuoli/release-actions/internal/model"
	"github.com/hainuoli/release-actions/internal/notes"
)

const maxNotesSize = 1_000_000

type (
	githubClient interface {
		GetRef(context.Context, model.Repository, string) (*github.GitRef, error)
		CreateBranch(context.Context, model.Repository, string, string) error
		GetFile(context.Context, model.Repository, string, string) (*github.File, error)
		UpdateBranchFile(context.Context, model.Repository, string, string, string, string, string) error
		FindReleasePullRequest(context.Context, model.Repository, string) (*model.PullRequest, error)
		CreateReleasePullRequest(context.Context, model.Repository, string, string, string) (model.PullRequest, error)
		EnsureReleaseLabel(context.Context, model.Repository) error
		AddLabel(context.Context, model.Repository, int, string) error
		ResolveTag(context.Context, model.Repository, github.GitObject) (string, error)
		IsAncestor(context.Context, model.Repository, string, string) (bool, error)
		GetRelease(context.Context, model.Repository, string) (*github.Release, error)
		CreateRelease(context.Context, model.Repository, string, string, string, bool, bool) (*github.Release, error)
	}

	// Service performs release operations exclusively through the GitHub API.
	Service struct {
		client githubClient
	}

	// Options selects the document, version, date, and publication policy.
	Options struct {
		Version string
		Date    string
		Path    string
		SHA     string
		Draft   bool
	}

	// Result contains single-line GitHub Actions outputs.
	Result struct {
		Version     string
		Tag         string
		PullRequest int
		URL         string
	}
)

// NewService creates a release coordinator with repository-scoped credentials.
func NewService(client githubClient) *Service {
	return &Service{client: client}
}

// Prepare creates a changelog-only PR without tagging or publishing code.
// Existing PRs and partially completed branch writes can be retried safely.
func (s *Service) Prepare(ctx context.Context, repo model.Repository, opts Options) (Result, error) {
	version, err := notes.ParseVersion(opts.Version)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Version: version.Number,
		Tag:     version.Tag,
	}
	date := opts.Date
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return result, errors.New("release-date must be a valid YYYY-MM-DD date")
	}
	if tag, err := s.client.GetRef(ctx, repo, "tags/"+version.Tag); err != nil {
		return result, err
	} else if tag != nil {
		return result, errors.New("release tag already exists; choose a new version")
	}

	branch := "release/" + version.Tag
	title := "🔖 Prepare " + version.Tag + " release"
	if err := labels.ValidateCommitTitle(title); err != nil {
		return result, err
	}
	existing, err := s.client.FindReleasePullRequest(ctx, repo, branch)
	if err != nil {
		return result, err
	}
	if existing != nil {
		_, content, err := s.readNotes(ctx, repo, opts.Path, existing.Head.SHA)
		if err != nil {
			return result, err
		}
		if _, err := notes.ExtractRelease(content, version); err != nil {
			return result, errors.New("existing release PR has no matching version section; review it manually")
		}
		if err := s.labelRelease(ctx, repo, existing.Number); err != nil {
			return result, err
		}
		result.PullRequest, result.URL = existing.Number, existing.URL
		return result, nil
	}

	base, err := s.client.GetRef(ctx, repo, "heads/"+repo.DefaultBranch)
	if err != nil {
		return result, err
	}
	if base == nil || base.Object.Type != "commit" {
		return result, errors.New("default branch does not resolve to a commit")
	}
	file, content, err := s.readNotes(ctx, repo, opts.Path, base.Object.SHA)
	if err != nil {
		return result, err
	}
	updated, err := notes.CutRelease(content, version, date)
	if err != nil {
		return result, err
	}
	if len(updated) > maxNotesSize {
		return result, errors.New("updated release notes exceed size limit")
	}
	ref, err := s.client.GetRef(ctx, repo, "heads/"+branch)
	if err != nil {
		return result, err
	}
	if ref == nil {
		if err := s.client.CreateBranch(ctx, repo, branch, base.Object.SHA); err != nil {
			return result, err
		}
	} else {
		branchFile, branchContent, err := s.readNotes(ctx, repo, file.Path, ref.Object.SHA)
		if err != nil {
			return result, err
		}
		switch branchContent {
		case updated:
			// A previous run committed the document but failed before opening its PR.
			content = updated
		case content:
			file = branchFile
		default:
			return result, errors.New("release branch contains different notes; review it instead of overwriting it")
		}
	}
	if content != updated {
		if err := s.client.UpdateBranchFile(ctx, repo, branch, file.Path, file.SHA, title, updated); err != nil {
			return result, err
		}
	}
	// Create the label before the PR so a rerun can finish labeling a partial run.
	if err := s.client.EnsureReleaseLabel(ctx, repo); err != nil {
		return result, err
	}
	body := fmt.Sprintf("Prepare release %s.\n\n- Archive Latest Changes in `%s` under `%s (%s)`.\n- Preserve Latest Changes for future pull requests.\n\nReview the diff and wait for CI before merging. Tag the reviewed default-branch commit only after its checks pass.\n", version.Tag, file.Path, version.Number, date)
	pr, err := s.client.CreateReleasePullRequest(ctx, repo, branch, title, body)
	if err != nil {
		return result, err
	}
	if err := s.client.AddLabel(ctx, repo, pr.Number, "release"); err != nil {
		return result, err
	}
	result.PullRequest, result.URL = pr.Number, pr.URL
	return result, nil
}

// Publish reads notes from the verified tagged commit, never from a moving branch.
// It never changes tags or overwrites an existing Release, including drafts.
func (s *Service) Publish(ctx context.Context, repo model.Repository, opts Options) (Result, error) {
	version, err := notes.ParseVersion(opts.Version)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Version: version.Number,
		Tag:     version.Tag,
	}
	tag, err := s.client.GetRef(ctx, repo, "tags/"+version.Tag)
	if err != nil {
		return result, err
	}
	if tag == nil {
		return result, errors.New("release tag does not exist; push a reviewed tag first")
	}
	sha, err := s.client.ResolveTag(ctx, repo, tag.Object)
	if err != nil {
		return result, err
	}
	if opts.SHA != "" && opts.SHA != sha {
		return result, errors.New("release tag no longer matches the workflow commit")
	}
	base, err := s.client.GetRef(ctx, repo, "heads/"+repo.DefaultBranch)
	if err != nil {
		return result, err
	}
	if base == nil || base.Object.Type != "commit" {
		return result, errors.New("default branch does not resolve to a commit")
	}
	reachable, err := s.client.IsAncestor(ctx, repo, sha, base.Object.SHA)
	if err != nil {
		return result, err
	}
	if !reachable {
		return result, errors.New("release commit is not reachable from the default branch")
	}
	_, content, err := s.readNotes(ctx, repo, opts.Path, sha)
	if err != nil {
		return result, err
	}
	body, err := notes.ExtractRelease(content, version)
	if err != nil {
		return result, err
	}
	release, err := s.client.GetRelease(ctx, repo, version.Tag)
	if err != nil {
		return result, err
	}
	if release != nil {
		if strings.TrimSpace(release.Body) != strings.TrimSpace(body) || release.Prerelease != version.Prerelease || release.Draft != opts.Draft {
			return result, errors.New("existing Release differs from the requested notes or publication settings; it was not modified")
		}
		result.URL = release.URL
		return result, nil
	}
	// Verify again immediately before creation to detect moved/deleted tags.
	current, err := s.client.GetRef(ctx, repo, "tags/"+version.Tag)
	if err != nil {
		return result, err
	}
	if current == nil || current.Object != tag.Object {
		return result, errors.New("release tag changed during publication")
	}
	release, err = s.client.CreateRelease(ctx, repo, version.Tag, sha, body, opts.Draft, version.Prerelease)
	if err != nil {
		return result, err
	}
	result.URL = release.URL
	return result, nil
}

func (s *Service) labelRelease(ctx context.Context, repo model.Repository, number int) error {
	if err := s.client.EnsureReleaseLabel(ctx, repo); err != nil {
		return err
	}
	return s.client.AddLabel(ctx, repo, number, "release")
}

func (s *Service) readNotes(ctx context.Context, repo model.Repository, path, ref string) (*github.File, string, error) {
	paths := []string{path}
	if path == "" {
		paths = []string{"release-notes.md", "docs/release-notes.md", "docs/en/docs/release-notes.md", "CHANGELOG.md"}
	}
	for _, candidate := range paths {
		file, err := s.client.GetFile(ctx, repo, candidate, ref)
		if err != nil {
			return nil, "", err
		}
		if file == nil {
			continue
		}
		data, err := file.Decode(maxNotesSize)
		return file, string(data), err
	}
	return nil, "", errors.New("release notes file does not exist")
}
