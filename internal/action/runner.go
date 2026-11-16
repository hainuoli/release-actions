package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/hainuoli/release-actions/internal/app"
	"github.com/hainuoli/release-actions/internal/config"
	"github.com/hainuoli/release-actions/internal/github"
	"github.com/hainuoli/release-actions/internal/model"
	"github.com/hainuoli/release-actions/internal/release"
)

const maxEventSize = 25_000_000

// Runner processes one Actions event using trusted repository metadata.
type Runner struct {
	client      *github.Client
	notesClient *github.Client
}

// NewRunner creates a processor with separate workflow and release-note clients.
func NewRunner(client, notesClient *github.Client) *Runner {
	return &Runner{
		client:      client,
		notesClient: notesClient,
	}
}

// Run loads the event, verifies its repository, and handles the current PR state.
func (r *Runner) Run(ctx context.Context, cfg config.Config) error {
	event, err := readEvent(cfg.EventPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(event.Repository.FullName, cfg.Repository) {
		return errors.New("event does not match GITHUB_REPOSITORY")
	}

	repo, err := r.client.GetRepository(ctx, cfg.Repository)
	if err != nil {
		return err
	}
	if repo.ID != event.Repository.ID {
		return errors.New("event repository ID does not match the API")
	}
	event.Repository = repo
	if cfg.Command == "prepare-release" || cfg.Command == "publish-release" {
		return r.runRelease(ctx, repo, cfg)
	}
	if cfg.EventName == "workflow_dispatch" {
		pr, err := r.client.GetPullRequest(ctx, repo, cfg.PullRequest)
		if err != nil {
			return err
		}
		event.PullRequest = pr
		event.Action = "synchronize"
		if pr.State == "closed" || pr.Merged {
			event.Action = "closed"
		}
	}

	service := app.NewService(r.client, r.notesClient)
	service.SetNotesPath(cfg.NotesFile)
	if err := service.ProcessEvent(ctx, event); err != nil {
		return err
	}
	slog.Info("Latest Changes completed", "repository", repo.FullName,
		"pull_request", event.PullRequest.Number, "action", event.Action)
	return nil
}

func (r *Runner) runRelease(ctx context.Context, repo model.Repository, cfg config.Config) error {
	if cfg.EventName == "workflow_dispatch" && cfg.Ref != "refs/heads/"+repo.DefaultBranch {
		return errors.New("run release commands from the repository default branch")
	}
	opts := release.Options{
		Version: cfg.Version,
		Date:    cfg.Date,
		Path:    cfg.NotesFile,
		Draft:   cfg.Draft,
	}
	service := release.NewService(r.client)
	var result release.Result
	var err error
	switch cfg.Command {
	case "prepare-release":
		if cfg.EventName != "workflow_dispatch" {
			return errors.New("prepare-release requires workflow_dispatch")
		}
		result, err = service.Prepare(ctx, repo, opts)
	case "publish-release":
		if cfg.EventName == "push" {
			if cfg.Ref != "refs/tags/"+cfg.Version || cfg.SHA == "" {
				return errors.New("publish-release requires the matching tag push")
			}
			opts.SHA = cfg.SHA
		} else if cfg.EventName != "workflow_dispatch" {
			return errors.New("publish-release requires a tag push or workflow_dispatch")
		}
		result, err = service.Publish(ctx, repo, opts)
	}
	if err != nil {
		return err
	}
	if err := writeOutputs(cfg.OutputPath, result); err != nil {
		return err
	}
	slog.Info("Release Actions completed", "command", cfg.Command, "tag", result.Tag, "url", result.URL)
	return nil
}

func writeOutputs(path string, result release.Result) error {
	if path == "" {
		return nil
	}
	for _, value := range []string{result.Version, result.Tag, result.URL} {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid multiline action output")
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("could not open GITHUB_OUTPUT")
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "version=%s\ntag=%s\npull-request=%d\nurl=%s\n", result.Version, result.Tag, result.PullRequest, result.URL)
	return err
}

func readEvent(path string) (model.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.Event{}, errors.New("could not open GITHUB_EVENT_PATH")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxEventSize+1))
	if err != nil || len(data) > maxEventSize {
		return model.Event{}, errors.New("invalid or oversized Actions event")
	}
	var event model.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return event, errors.New("invalid Actions event JSON")
	}
	return event, nil
}
