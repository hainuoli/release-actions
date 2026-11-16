package config

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/hainuoli/release-actions/internal/model"
)

// Config contains the GitHub Actions environment for a single run.
type Config struct {
	Command     string
	Version     string
	Date        string
	NotesFile   string
	Ref         string
	SHA         string
	Draft       bool
	OutputPath  string
	Token       string
	NotesToken  string
	EventPath   string
	EventName   string
	Repository  string
	PullRequest int
}

// Load reads the runner environment and optional manual recovery input.
func Load() (Config, error) {
	c := Config{
		Command:    os.Getenv("RELEASE_ACTIONS_COMMAND"),
		Version:    os.Getenv("RELEASE_ACTIONS_VERSION"),
		Date:       os.Getenv("RELEASE_ACTIONS_DATE"),
		NotesFile:  os.Getenv("RELEASE_ACTIONS_NOTES_FILE"),
		Ref:        os.Getenv("GITHUB_REF"),
		SHA:        os.Getenv("GITHUB_SHA"),
		OutputPath: os.Getenv("GITHUB_OUTPUT"),
		Token:      os.Getenv("GITHUB_TOKEN"),
		NotesToken: os.Getenv("LATEST_CHANGES_NOTES_TOKEN"),
		EventPath:  os.Getenv("GITHUB_EVENT_PATH"),
		EventName:  os.Getenv("GITHUB_EVENT_NAME"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
	}
	if c.Command == "" {
		c.Command = "latest-changes"
	}
	if c.Token == "" || c.EventPath == "" || !model.IsRepositoryName(c.Repository) {
		return c, errors.New("GITHUB_TOKEN, GITHUB_EVENT_PATH, and a valid GITHUB_REPOSITORY are required")
	}
	if c.NotesFile != "" {
		if err := ValidateNotesPath(c.NotesFile); err != nil {
			return c, err
		}
	}
	if draft := os.Getenv("RELEASE_ACTIONS_DRAFT"); draft != "" {
		if draft != "true" && draft != "false" {
			return c, errors.New("draft must be true or false")
		}
		c.Draft = draft == "true"
	}
	if c.Command != "latest-changes" {
		switch c.Command {
		case "prepare-release":
			if c.EventName != "workflow_dispatch" || c.Version == "" {
				return c, errors.New("prepare-release requires workflow_dispatch and a version")
			}
		case "publish-release":
			if c.EventName == "push" && strings.HasPrefix(c.Ref, "refs/tags/") {
				tag := strings.TrimPrefix(c.Ref, "refs/tags/")
				if c.Version != "" && c.Version != tag {
					return c, errors.New("version must match the pushed tag")
				}
				c.Version = tag
			} else if c.EventName != "workflow_dispatch" || c.Version == "" {
				return c, errors.New("publish-release requires a tag push or workflow_dispatch with a version")
			}
		default:
			return c, errors.New("unknown command")
		}
		return c, nil
	}

	switch c.EventName {
	case "pull_request", "pull_request_target":
		if os.Getenv("LATEST_CHANGES_PULL_REQUEST") != "" {
			return c, errors.New("pull-request input is only supported for workflow_dispatch")
		}
	case "workflow_dispatch":
		number, err := strconv.Atoi(os.Getenv("LATEST_CHANGES_PULL_REQUEST"))
		if err != nil || number <= 0 {
			return c, errors.New("workflow_dispatch requires a positive pull-request input")
		}
		c.PullRequest = number
	default:
		return c, errors.New("expected pull_request, pull_request_target, or workflow_dispatch event")
	}

	return c, nil
}

// ValidateNotesPath restricts inputs to repository-relative POSIX file paths.
func ValidateNotesPath(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\r\n\x00") {
		return errors.New("notes-file must be a repository-relative file path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("invalid notes-file path component")
		}
	}
	return nil
}
