package config

import "testing"

func TestLoadReleaseCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		event   string
		ref     string
		version string
		draft   string
		valid   bool
	}{
		{
			name:    "prepare",
			command: "prepare-release",
			event:   "workflow_dispatch",
			version: "v0.1.0",
			valid:   true,
		},
		{
			name:    "prepare-pr-rejected",
			command: "prepare-release",
			event:   "pull_request_target",
			version: "v0.1.0",
		},
		{
			name:    "prepare-missing-version",
			command: "prepare-release",
			event:   "workflow_dispatch",
		},
		{
			name:    "publish-tag",
			command: "publish-release",
			event:   "push",
			ref:     "refs/tags/v0.1.0",
			valid:   true,
		},
		{
			name:    "publish-manual",
			command: "publish-release",
			event:   "workflow_dispatch",
			version: "v0.1.0",
			draft:   "true",
			valid:   true,
		},
		{
			name:    "publish-branch-rejected",
			command: "publish-release",
			event:   "push",
			ref:     "refs/heads/main",
		},
		{
			name:    "publish-mismatch",
			command: "publish-release",
			event:   "push",
			ref:     "refs/tags/v0.1.0",
			version: "v0.2.0",
		},
		{
			name:    "invalid-draft",
			command: "publish-release",
			event:   "workflow_dispatch",
			version: "v0.1.0",
			draft:   "yes",
		},
		{
			name:    "unknown-command",
			command: "delete-release",
			event:   "workflow_dispatch",
			version: "v0.1.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "workflow-token")
			t.Setenv("GITHUB_REPOSITORY", "owner/repo")
			t.Setenv("GITHUB_EVENT_PATH", "/tmp/event.json")
			t.Setenv("GITHUB_EVENT_NAME", tc.event)
			t.Setenv("GITHUB_REF", tc.ref)
			t.Setenv("RELEASE_ACTIONS_COMMAND", tc.command)
			t.Setenv("RELEASE_ACTIONS_VERSION", tc.version)
			t.Setenv("RELEASE_ACTIONS_DRAFT", tc.draft)
			cfg, err := Load()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && tc.ref == "refs/tags/v0.1.0" && cfg.Version != "v0.1.0" {
				t.Fatal("tag version was not inferred")
			}
		})
	}
}

func TestValidateNotesPath(t *testing.T) {
	for _, path := range []string{"CHANGELOG.md", "docs/en/docs/release-notes.md", "release notes.md"} {
		if err := ValidateNotesPath(path); err != nil {
			t.Fatalf("rejected %q", path)
		}
	}
	for _, path := range []string{"", "/tmp/file", "../CHANGELOG.md", "docs/../file", ".git/config", "docs\\file", "docs//file", "file\noutput=evil"} {
		if err := ValidateNotesPath(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
