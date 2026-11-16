package release

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hainuoli/release-actions/internal/github"
	"github.com/hainuoli/release-actions/internal/model"
)

const (
	sourceNotes = "# Changelog\n\n## Latest Changes\n\n### Features\n\n* Add routing.\n"
	cutNotes    = "# Changelog\n\n## Latest Changes\n\n## 0.1.0 (2026-11-16)\n\n### Features\n\n* Add routing.\n"
	releaseBody = "### Features\n\n* Add routing.\n"
)

func repository() model.Repository {
	return model.Repository{
		ID:            42,
		FullName:      "owner/repo",
		DefaultBranch: "main",
	}
}

func encodeFile(w http.ResponseWriter, content string) {
	json.NewEncoder(w).Encode(github.File{
		Type:     "file",
		Path:     "CHANGELOG.md",
		SHA:      "blob",
		Size:     len(content),
		Encoding: "base64",
		Content:  base64.StdEncoding.EncodeToString([]byte(content)),
	})
}

func TestPrepareAndRecoverThroughGitHubAPI(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "normal"
		if interrupted {
			name = "recover-after-commit-before-pr"
		}
		t.Run(name, func(t *testing.T) {
			branchExists, prExists := false, false
			branchNotes := sourceNotes
			writes, creates, labels := 0, 0, 0
			prAttempts := 0
			pr := model.PullRequest{
				Number: 7,
				URL:    "https://github.com/owner/repo/pull/7",
				Head: model.Ref{
					Name: "release/v0.1.0",
					SHA:  "prepared",
				},
				Base: model.Ref{Name: "main"},
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer workflow-token" {
					t.Error("wrong release credential")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/owner/repo/git/ref/tags/v0.1.0":
					w.WriteHeader(404)
				case "GET /repos/owner/repo/git/ref/heads/main":
					json.NewEncoder(w).Encode(github.GitRef{
						Ref: "refs/heads/main",
						Object: github.GitObject{
							Type: "commit",
							SHA:  "base",
						},
					})
				case "GET /repos/owner/repo/git/ref/heads/release/v0.1.0":
					if !branchExists {
						w.WriteHeader(404)
						return
					}
					json.NewEncoder(w).Encode(github.GitRef{
						Ref: "refs/heads/release/v0.1.0",
						Object: github.GitObject{
							Type: "commit",
							SHA:  "prepared",
						},
					})
				case "POST /repos/owner/repo/git/refs":
					var payload map[string]string
					json.NewDecoder(r.Body).Decode(&payload)
					if payload["ref"] != "refs/heads/release/v0.1.0" || payload["sha"] != "base" {
						t.Errorf("wrong branch source: %v", payload)
					}
					branchExists = true
					creates++
					w.WriteHeader(201)
				case "GET /repos/owner/repo/contents/CHANGELOG.md":
					switch r.URL.Query().Get("ref") {
					case "base":
						encodeFile(w, sourceNotes)
					case "prepared":
						encodeFile(w, branchNotes)
					default:
						t.Error("release notes were not read at a pinned commit")
						w.WriteHeader(400)
					}
				case "PUT /repos/owner/repo/contents/CHANGELOG.md":
					var payload struct {
						Message           string
						Content           string
						Branch            string
						SHA               string
						Author, Committer struct {
							Name string
						}
					}
					json.NewDecoder(r.Body).Decode(&payload)
					decoded, err := base64.StdEncoding.DecodeString(payload.Content)
					if err != nil || payload.Branch != "release/v0.1.0" || payload.SHA != "blob" || payload.Message != "🔖 Prepare v0.1.0 release" || string(decoded) != cutNotes {
						t.Errorf("incorrect release commit: %+v, %v", payload, err)
					}
					if payload.Author.Name != "github-actions[bot]" || payload.Committer.Name != "github-actions[bot]" {
						t.Error("release commit lost bot identity")
					}
					branchNotes = string(decoded)
					writes++
					w.WriteHeader(200)
				case "GET /repos/owner/repo/pulls":
					if r.URL.Query().Get("head") != "owner:release/v0.1.0" || r.URL.Query().Get("base") != "main" || r.URL.Query().Get("state") != "open" {
						t.Error("unscoped release PR lookup")
					}
					if prExists {
						json.NewEncoder(w).Encode([]model.PullRequest{pr})
					} else {
						json.NewEncoder(w).Encode([]model.PullRequest{})
					}
				case "POST /repos/owner/repo/pulls":
					prAttempts++
					if interrupted && prAttempts == 1 {
						w.WriteHeader(403)
						return
					}
					var payload map[string]string
					json.NewDecoder(r.Body).Decode(&payload)
					if payload["title"] != "🔖 Prepare v0.1.0 release" || payload["head"] != "release/v0.1.0" || payload["base"] != "main" {
						t.Error("incorrect release PR")
					}
					prExists = true
					json.NewEncoder(w).Encode(pr)
				case "GET /repos/owner/repo/labels/release":
					w.WriteHeader(404)
				case "POST /repos/owner/repo/labels":
					w.WriteHeader(201)
				case "POST /repos/owner/repo/issues/7/labels":
					var payload struct {
						Labels []string
					}
					json.NewDecoder(r.Body).Decode(&payload)
					if len(payload.Labels) != 1 || payload.Labels[0] != "release" {
						t.Error("release PR is not excluded from changelog entries")
					}
					labels++
					w.WriteHeader(200)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(404)
				}
			}))
			defer api.Close()
			client, err := github.NewClient("workflow-token", github.WithBaseURL(api.URL))
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(client)
			opts := Options{
				Version: "v0.1.0",
				Date:    "2026-11-16",
				Path:    "CHANGELOG.md",
			}
			result, err := service.Prepare(context.Background(), repository(), opts)
			if interrupted {
				if !github.HasStatus(err, 403) {
					t.Fatalf("expected interrupted PR creation, got %v", err)
				}
				result, err = service.Prepare(context.Background(), repository(), opts)
			}
			if err != nil || result.PullRequest != 7 || result.URL != pr.URL {
				t.Fatalf("result %+v, err %v", result, err)
			}
			// A third delivery returns the same PR, without another branch/commit.
			if _, err := service.Prepare(context.Background(), repository(), opts); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || creates != 1 || labels != 2 {
				t.Fatalf("writes=%d branches=%d label writes=%d", writes, creates, labels)
			}
		})
	}
}

func TestPublishTagValidationAndIdempotency(t *testing.T) {
	for _, tc := range []struct {
		name             string
		missingTag       bool
		movedTag         bool
		wrongSHA         bool
		unreachable      bool
		missingNotes     bool
		existing         bool
		conflictingBody  bool
		conflictingDraft bool
		valid            bool
	}{
		{
			name:  "annotated-tag",
			valid: true,
		},
		{
			name:     "existing-release",
			existing: true,
			valid:    true,
		},
		{
			name:       "missing-tag",
			missingTag: true,
		},
		{
			name:     "tag-moved-before-create",
			movedTag: true,
		},
		{
			name:     "wrong-workflow-sha",
			wrongSHA: true,
		},
		{
			name:        "not-on-main",
			unreachable: true,
		},
		{
			name:         "missing-section",
			missingNotes: true,
		},
		{
			name:            "conflicting-release-body",
			existing:        true,
			conflictingBody: true,
		},
		{
			name:             "conflicting-release-draft",
			existing:         true,
			conflictingDraft: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created, tagReads := 0, 0
			published := tc.existing
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/owner/repo/git/ref/tags/v0.1.0":
					tagReads++
					if tc.missingTag {
						w.WriteHeader(404)
						return
					}
					sha := "annotated"
					if tc.movedTag && tagReads > 1 {
						sha = "changed"
					}
					json.NewEncoder(w).Encode(github.GitRef{
						Ref: "refs/tags/v0.1.0",
						Object: github.GitObject{
							Type: "tag",
							SHA:  sha,
						},
					})
				case "GET /repos/owner/repo/git/tags/annotated":
					json.NewEncoder(w).Encode(map[string]any{"object": github.GitObject{
						Type: "commit",
						SHA:  "tagged",
					}})
				case "GET /repos/owner/repo/git/ref/heads/main":
					json.NewEncoder(w).Encode(github.GitRef{
						Ref: "refs/heads/main",
						Object: github.GitObject{
							Type: "commit",
							SHA:  "latest",
						},
					})
				case "GET /repos/owner/repo/compare/tagged...latest":
					status := "ahead"
					if tc.unreachable {
						status = "diverged"
					}
					json.NewEncoder(w).Encode(map[string]string{"status": status})
				case "GET /repos/owner/repo/contents/CHANGELOG.md":
					if r.URL.Query().Get("ref") != "tagged" {
						t.Error("publication read a moving branch")
					}
					content := cutNotes
					if tc.missingNotes {
						content = sourceNotes
					}
					encodeFile(w, content)
				case "GET /repos/owner/repo/releases/tags/v0.1.0":
					if !published {
						w.WriteHeader(404)
						return
					}
					body := releaseBody
					if tc.conflictingBody {
						body = "Someone else's notes"
					}
					json.NewEncoder(w).Encode(github.Release{
						ID:    1,
						Tag:   "v0.1.0",
						URL:   "https://github.com/owner/repo/releases/tag/v0.1.0",
						Body:  body,
						Draft: tc.conflictingDraft,
					})
				case "POST /repos/owner/repo/releases":
					var payload map[string]any
					json.NewDecoder(r.Body).Decode(&payload)
					if payload["body"] != releaseBody || payload["target_commitish"] != "tagged" || payload["tag_name"] != "v0.1.0" || payload["prerelease"] != false || payload["draft"] != false {
						t.Errorf("invalid release payload: %v", payload)
					}
					published = true
					created++
					json.NewEncoder(w).Encode(github.Release{
						ID:   1,
						Tag:  "v0.1.0",
						URL:  "https://github.com/owner/repo/releases/tag/v0.1.0",
						Body: releaseBody,
					})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(404)
				}
			}))
			defer api.Close()
			client, err := github.NewClient("workflow-token", github.WithBaseURL(api.URL))
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(client)
			opts := Options{
				Version: "v0.1.0",
				SHA:     "tagged",
				Path:    "CHANGELOG.md",
			}
			if tc.wrongSHA {
				opts.SHA = "old-workflow-sha"
			}
			result, err := service.Publish(context.Background(), repository(), opts)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v result=%+v err=%v", tc.valid, result, err)
			}
			if !tc.valid && created != 0 {
				t.Fatal("invalid release was published")
			}
			if tc.valid {
				if !strings.HasSuffix(result.URL, "/v0.1.0") {
					t.Fatal("missing release output")
				}
				if _, err := service.Publish(context.Background(), repository(), opts); err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.existing {
					want = 0
				}
				if created != want {
					t.Fatalf("release created %d times", created)
				}
			}
		})
	}
}
