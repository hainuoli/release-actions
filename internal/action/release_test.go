package action

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hainuoli/release-actions/internal/config"
	"github.com/hainuoli/release-actions/internal/github"
	"github.com/hainuoli/release-actions/internal/release"
)

func TestReleaseEventRequiresTrustedDefaultBranch(t *testing.T) {
	event := fixture()
	apiCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if r.URL.Path != "/repos/owner/repo" {
			t.Error("untrusted branch reached release endpoints")
		}
		json.NewEncoder(w).Encode(event.Repository)
	}))
	defer api.Close()
	client, err := github.NewClient("workflow-token", github.WithBaseURL(api.URL))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"prepare-release", "publish-release"} {
		cfg := config.Config{
			Command:    command,
			Version:    "v0.1.0",
			EventName:  "workflow_dispatch",
			EventPath:  path,
			Repository: "owner/repo",
			Ref:        "refs/heads/untrusted",
		}
		if err := NewRunner(client, client).Run(context.Background(), cfg); err == nil {
			t.Fatal("accepted release command from nondefault branch")
		}
	}
	if apiCalls != 2 {
		t.Fatalf("unexpected API calls: %d", apiCalls)
	}
}

func TestReleaseOutputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result := release.Result{
		Version:     "0.1.0",
		Tag:         "v0.1.0",
		PullRequest: 7,
		URL:         "https://github.com/owner/repo/pull/7",
	}
	if err := writeOutputs(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "pull-request=7\n") || !strings.Contains(string(data), "tag=v0.1.0\n") {
		t.Fatalf("incorrect action outputs: %q, %v", data, err)
	}
	result.URL += "\ninjected=value"
	if err := writeOutputs(path, result); err == nil {
		t.Fatal("accepted action output injection")
	}
}
