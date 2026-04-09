// Package updater queries GitHub for newer stakecli releases and installs
// them on demand. Checks are cached locally (24h) and can be disabled via
// the STAKE_NO_UPDATE_CHECK environment variable.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mnemoo/cli/internal/version"
)

// GitHubRepo is the owner/name path of the upstream repository. Keep in sync
// with the install snippets in .goreleaser.yaml.
const GitHubRepo = "mnemoo/cli"

const (
	githubAPIURL    = "https://api.github.com/repos/" + GitHubRepo + "/releases/latest"
	cacheTTL        = 24 * time.Hour
	checkTimeout    = 5 * time.Second
	envDisableCheck = "STAKE_NO_UPDATE_CHECK"
	userAgent       = "stakecli-updater"
)

// Release mirrors the subset of GitHub's release JSON that we care about.
type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	HTMLURL    string  `json:"html_url"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is a downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckResult summarises a freshness comparison.
type CheckResult struct {
	Current   string   // current running version, e.g. "v1.2.3"
	Latest    string   // latest published version, e.g. "v1.3.0"
	HTMLURL   string   // GitHub release URL
	HasUpdate bool     // true if Latest > Current
	FromCache bool     // true if the latest info came from the local cache
	Release   *Release // raw release, used by Install
}

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Release   *Release  `json:"release"`
}

// Check returns a CheckResult using the local cache when it is still fresh.
// Returns (nil, nil) when the check is disabled, the build is a dev build,
// or there is no published release. Network errors are returned to the
// caller, which may choose to ignore them.
func Check(ctx context.Context) (*CheckResult, error) {
	return check(ctx, false)
}

// CheckFresh forces a network fetch, bypassing the local cache. Used by the
// `stakecli update` command so users always see authoritative results.
func CheckFresh(ctx context.Context) (*CheckResult, error) {
	return check(ctx, true)
}

func check(ctx context.Context, forceFresh bool) (*CheckResult, error) {
	if disabled() {
		return nil, nil
	}
	current := version.Get().Version
	if !isRealSemver(current) {
		return nil, nil
	}

	var (
		rel       *Release
		fromCache bool
	)
	if !forceFresh {
		rel, fromCache, _ = readCache()
	}
	if rel == nil {
		fetched, err := fetchLatest(ctx)
		if err != nil {
			return nil, err
		}
		rel = fetched
		_ = writeCache(rel)
	}
	if rel == nil || rel.Draft || rel.Prerelease {
		return nil, nil
	}

	return &CheckResult{
		Current:   current,
		Latest:    rel.TagName,
		HTMLURL:   rel.HTMLURL,
		HasUpdate: compareSemver(rel.TagName, current) > 0,
		FromCache: fromCache,
		Release:   rel,
	}, nil
}

// Notice returns a one-line update announcement, or "" when no update is
// available. Safe to call on a nil receiver.
func (r *CheckResult) Notice() string {
	if r == nil || !r.HasUpdate {
		return ""
	}
	return fmt.Sprintf(
		"A new stakecli release is available: %s -> %s. Run `stakecli update` to upgrade. (%s)",
		r.Current, r.Latest, r.HTMLURL,
	)
}

func disabled() bool {
	return os.Getenv(envDisableCheck) != ""
}

// isRealSemver accepts v1.2.3 / 1.2.3 with optional -prerelease suffix.
// Rejects the debug.BuildInfo fallback (v0.0.0-<timestamp>-<commit>[+dirty])
// and the literal "dev" default.
func isRealSemver(v string) bool {
	if v == "" || v == "dev" {
		return false
	}
	if strings.Contains(v, "+") {
		return false // "dirty" suffix from VCS
	}
	v = strings.TrimPrefix(v, "v")
	// Reject pseudo-versions like 0.0.0-20260327210438-e28a5e04e87c
	if strings.HasPrefix(v, "0.0.0-") {
		return false
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for i, p := range parts {
		// For the last segment, strip any pre-release suffix before parsing.
		if i == 2 {
			if idx := strings.IndexAny(p, "-+"); idx >= 0 {
				p = p[:idx]
			}
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// compareSemver returns -1 / 0 / 1 comparing a to b. Both may include a
// leading "v" and a "-prerelease" suffix. Release versions rank above any
// pre-release version with the same core.
func compareSemver(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aCore, aPre := splitPrerelease(a)
	bCore, bPre := splitPrerelease(b)

	aParts := parseCore(aCore)
	bParts := parseCore(bCore)
	for i := range 3 {
		switch {
		case aParts[i] > bParts[i]:
			return 1
		case aParts[i] < bParts[i]:
			return -1
		}
	}
	// Cores equal: a release beats any prerelease with the same core.
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	case aPre > bPre:
		return 1
	case aPre < bPre:
		return -1
	}
	return 0
}

func splitPrerelease(v string) (core, pre string) {
	if before, after, ok := strings.Cut(v, "-"); ok {
		return before, after
	}
	return v, ""
}

func parseCore(s string) [3]int {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func fetchLatest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent+"/"+version.Get().Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Repo exists but has no releases yet. Not an error for our purposes.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github api: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "stakecli", "update-check.json"), nil
}

func readCache() (*Release, bool, error) {
	p, err := cachePath()
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		// Corrupt cache — drop it.
		_ = os.Remove(p)
		return nil, false, err
	}
	if time.Since(c.CheckedAt) > cacheTTL {
		return nil, false, nil
	}
	return c.Release, true, nil
}

func writeCache(r *Release) error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheFile{CheckedAt: time.Now(), Release: r})
	if err != nil {
		return err
	}
	// Atomic write: tmp file + rename.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".update-check-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		// Best-effort cleanup on error paths.
		if _, err := os.Stat(tmpPath); err == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, p)
}
