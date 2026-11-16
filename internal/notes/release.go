package notes

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Version contains a SemVer tag and its unprefixed changelog heading.
// Build metadata is deliberately unsupported because Go module tags omit it.
type Version struct {
	Tag        string
	Number     string
	Prerelease bool
}

// ParseVersion accepts X.Y.Z or vX.Y.Z, including SemVer prereleases.
func ParseVersion(tag string) (Version, error) {
	match := versionPattern.FindStringSubmatch(tag)
	if match == nil || len(tag) > 48 {
		return Version{}, errors.New("version must be X.Y.Z or vX.Y.Z, optionally with a prerelease suffix (at most 48 bytes)")
	}
	for _, part := range strings.Split(match[4], ".") {
		if numeric(part) && len(part) > 1 && part[0] == '0' {
			return Version{}, errors.New("numeric prerelease identifiers must not have leading zeroes")
		}
	}
	return Version{
		Tag:        tag,
		Number:     strings.TrimPrefix(tag, "v"),
		Prerelease: match[4] != "",
	}, nil
}

// CutRelease archives Latest Changes and preserves the heading for future PRs.
func CutRelease(content string, version Version, date string) (string, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", errors.New("release-date must be a valid YYYY-MM-DD date")
	}
	lines := strings.Split(normalize(content), "\n")
	headings := documentHeadings(lines)
	start, end := -1, len(lines)
	for _, index := range headings {
		line := lines[index]
		if line == "## Latest Changes" {
			if start >= 0 {
				return "", errors.New("duplicate Latest Changes heading")
			}
			start = index
		} else if start >= 0 && end == len(lines) {
			end = index
		}
		if old, err := headingVersion(line); err == nil && compareVersions(version.Number, old.Number) <= 0 {
			return "", fmt.Errorf("version must be newer than existing release %s", old.Number)
		}
	}
	if start < 0 {
		return "", errors.New("release notes have no Latest Changes heading")
	}
	body := trimBlank(lines[start+1 : end])
	if !hasReleaseText(body) {
		return "", errors.New("Latest Changes has no release entries")
	}
	result := append([]string{}, lines[:start+1]...)
	result = append(result, "", "## "+version.Number+" ("+date+")", "")
	result = append(result, body...)
	if end < len(lines) {
		result = append(result, "")
		result = append(result, lines[end:]...)
	}
	return strings.TrimRight(strings.Join(result, "\n"), "\n") + "\n", nil
}

// ExtractRelease reads exactly one version section, excluding Latest Changes.
func ExtractRelease(content string, version Version) (string, error) {
	lines := strings.Split(normalize(content), "\n")
	start, end := -1, len(lines)
	for _, index := range documentHeadings(lines) {
		if start >= 0 && end == len(lines) {
			end = index
		}
		if found, err := headingVersion(lines[index]); err == nil && found.Number == version.Number {
			if start >= 0 {
				return "", errors.New("duplicate release heading")
			}
			start = index
		}
	}
	if start < 0 || !hasReleaseText(lines[start+1:end]) {
		return "", fmt.Errorf("release notes have no nonempty section for %s", version.Number)
	}
	return strings.TrimSpace(strings.Join(lines[start+1:end], "\n")) + "\n", nil
}

func normalize(content string) string {
	return strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
}

func headingVersion(line string) (Version, error) {
	if !strings.HasPrefix(line, "## ") {
		return Version{}, errors.New("not a version heading")
	}
	number := strings.TrimPrefix(line, "## ")
	if before, after, ok := strings.Cut(number, " ("); ok {
		if !strings.HasSuffix(after, ")") {
			return Version{}, errors.New("invalid version heading")
		}
		if _, err := time.Parse("2006-01-02", strings.TrimSuffix(after, ")")); err != nil {
			return Version{}, err
		}
		number = before
	}
	return ParseVersion(number)
}

func documentHeadings(lines []string) []int {
	var result []int
	var fence byte
	fenceLength := 0
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			length := len(trimmed) - len(strings.TrimLeft(trimmed, string(trimmed[0])))
			if length >= 3 {
				if fence == 0 {
					fence, fenceLength = trimmed[0], length
				} else if trimmed[0] == fence && length >= fenceLength && strings.TrimSpace(trimmed[length:]) == "" {
					fence = 0
				}
				continue
			}
		}
		if fence == 0 && strings.HasPrefix(line, "## ") {
			result = append(result, i)
		}
	}
	return result
}

func hasReleaseText(lines []string) bool {
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return true
		}
	}
	return false
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func compareVersions(a, b string) int {
	aCore, aPre, _ := strings.Cut(a, "-")
	bCore, bPre, _ := strings.Cut(b, "-")
	aParts, bParts := strings.Split(aCore, "."), strings.Split(bCore, ".")
	for i := range aParts {
		if n := compareNumber(aParts[i], bParts[i]); n != 0 {
			return n
		}
	}
	if aPre == bPre {
		return 0
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	aParts, bParts = strings.Split(aPre, "."), strings.Split(bPre, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		x, y := aParts[i], bParts[i]
		if x == y {
			continue
		}
		if numeric(x) && numeric(y) {
			return compareNumber(x, y)
		}
		if numeric(x) {
			return -1
		}
		if numeric(y) {
			return 1
		}
		return strings.Compare(x, y)
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	return 1
}
