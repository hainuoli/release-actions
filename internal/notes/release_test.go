package notes

import (
	"strings"
	"testing"

	"github.com/hainuoli/release-actions/internal/model"
)

func TestCutAndExtractRelease(t *testing.T) {
	version, err := ParseVersion("v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	old := "# Changelog\r\n\r\n## Latest Changes\r\n\r\n### Features\r\n\r\n* Add routing.\r\n\r\n## 0.0.1 (2026-11-15)\r\n\r\n* Initial code.\r\n"
	want := "# Changelog\n\n## Latest Changes\n\n## 0.1.0 (2026-11-16)\n\n### Features\n\n* Add routing.\n\n## 0.0.1 (2026-11-15)\n\n* Initial code.\n"
	got, err := CutRelease(old, version, "2026-11-16")
	if err != nil || got != want {
		t.Fatalf("got %q, error %v", got, err)
	}
	body, err := ExtractRelease(got, version)
	if err != nil || body != "### Features\n\n* Add routing.\n" {
		t.Fatalf("body %q, error %v", body, err)
	}
	// The ordinary PR renderer must write above the new historical version.
	pr := model.PullRequest{
		Number: 42,
		Title:  "🐛 Fix response",
		URL:    "https://github.com/owner/repo/pull/42",
		User: model.User{
			Login: "contributor",
			URL:   "https://github.com/contributor",
		},
		Labels: []model.Label{{Name: "bug"}},
	}
	updated, err := GenerateNotes(got, pr)
	if err != nil || !strings.Contains(updated, "## 0.1.0 (2026-11-16)\n\n### Features\n\n* Add routing.") {
		t.Fatalf("historical release changed: %q, %v", updated, err)
	}
}

func TestReleaseValidation(t *testing.T) {
	version, _ := ParseVersion("v0.1.0")
	for _, tc := range []struct {
		name    string
		content string
		date    string
	}{
		{
			name:    "missing-latest",
			content: "# Changelog\n",
			date:    "2026-11-16",
		},
		{
			name:    "duplicate-latest",
			content: "## Latest Changes\n* A.\n## Latest Changes\n* B.\n",
			date:    "2026-11-16",
		},
		{
			name:    "empty",
			content: "## Latest Changes\n\n",
			date:    "2026-11-16",
		},
		{
			name:    "empty-category",
			content: "## Latest Changes\n### Features\n",
			date:    "2026-11-16",
		},
		{
			name:    "invalid-date",
			content: "## Latest Changes\n* A.\n",
			date:    "2026-02-30",
		},
		{
			name:    "duplicate-version",
			content: "## Latest Changes\n* A.\n## 0.1.0\n* B.\n",
			date:    "2026-11-16",
		},
		{
			name:    "older-version",
			content: "## Latest Changes\n* A.\n## 0.2.0\n* B.\n",
			date:    "2026-11-16",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CutRelease(tc.content, version, tc.date); err == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
	for _, content := range []string{
		"## Latest Changes\n* A.\n",
		"## 0.1.0\n\n## 0.0.1\n* A.\n",
		"## 0.1.0\n* A.\n## 0.1.0\n* B.\n",
	} {
		if _, err := ExtractRelease(content, version); err == nil {
			t.Fatal("accepted missing, empty, or duplicate version section")
		}
	}
}

func TestReleaseFencedHeadings(t *testing.T) {
	version, _ := ParseVersion("1.0.0-rc.1")
	content := "## Latest Changes\n\n* Example:\n\n```markdown\n## 99.0.0\n```\n\n## 0.9.0\n* Old.\n"
	updated, err := CutRelease(content, version, "2026-11-16")
	if err != nil {
		t.Fatal(err)
	}
	body, err := ExtractRelease(updated, version)
	if err != nil || !strings.Contains(body, "## 99.0.0\n```") || strings.Contains(body, "* Old.") {
		t.Fatalf("fenced heading affected section: %q, %v", body, err)
	}
}

func TestParseVersion(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "1.2.3", "v2.0.0-rc.1", "1.0.0-alpha.0", "1.0.0-0a"} {
		if _, err := ParseVersion(tag); err != nil {
			t.Fatalf("rejected %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"", "v1", "1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+build", "v1.2.3-", "v1.2.3\n", "1.2.3/x", "1.2.3-" + strings.Repeat("a", 50)} {
		if _, err := ParseVersion(tag); err == nil {
			t.Fatalf("accepted %q", tag)
		}
	}
}

func TestVersionOrdering(t *testing.T) {
	ordered := []string{"0.1.0-alpha", "0.1.0-alpha.1", "0.1.0-alpha.2", "0.1.0-alpha.10", "0.1.0-beta", "0.1.0-rc.1", "0.1.0", "0.2.0", "0.10.0", "1.0.0"}
	for i, a := range ordered {
		for j, b := range ordered {
			got := compareVersions(a, b)
			if (i == j && got != 0) || (i < j && got >= 0) || (i > j && got <= 0) {
				t.Fatalf("incorrect ordering: %s versus %s = %d", a, b, got)
			}
		}
	}
}
