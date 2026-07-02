package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a downloadable file in a GitHub release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Remote fetches releases from a Git forge API.
type Remote struct {
	BaseURL string
	Token   string
	dl      *Downloader
}

// NewGitHub creates a Remote pointing to a GitHub repo (e.g. "owner/repo").
func NewGitHub(repo string) *Remote {
	return &Remote{
		BaseURL: fmt.Sprintf("https://api.github.com/repos/%s", repo),
		dl:      New(),
	}
}

// LatestRelease fetches the latest published release.
func (r *Remote) LatestRelease(ctx context.Context) (*Release, error) {
	url := r.BaseURL + "/releases/latest"
	return r.fetchRelease(ctx, url)
}

// Release fetches a specific release by tag (e.g. "v0.2.0").
func (r *Remote) Release(ctx context.Context, tag string) (*Release, error) {
	url := fmt.Sprintf("%s/releases/tags/%s", r.BaseURL, tag)
	return r.fetchRelease(ctx, url)
}

// ListReleases fetches the most recent releases, limited to the given count.
func (r *Remote) ListReleases(ctx context.Context, limit int) ([]Release, error) {
	url := fmt.Sprintf("%s/releases?per_page=%d", r.BaseURL, limit)
	headers := map[string]string{"Accept": "application/json"}
	if r.Token != "" {
		headers["Authorization"] = "Bearer " + r.Token
	}
	raw, err := r.dl.GetContent(ctx, RequestOpts{
		URL: url, Timeout: 30,
		Headers:  headers,
		UseCache: true, CacheTTLSeconds: 300,
	})
	if err != nil {
		return nil, r.mapError(err)
	}
	var releases []Release
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return nil, fmt.Errorf("unmarshal releases: %w", err)
	}
	return releases, nil
}

func (r *Remote) fetchRelease(ctx context.Context, url string) (*Release, error) {
	headers := map[string]string{"Accept": "application/json"}
	if r.Token != "" {
		headers["Authorization"] = "Bearer " + r.Token
	}
	raw, err := r.dl.GetContent(ctx, RequestOpts{
		URL: url, Timeout: 30,
		Headers:  headers,
		UseCache: true, CacheTTLSeconds: 300,
	})
	if err != nil {
		return nil, r.mapError(err)
	}
	var rel Release
	if err := json.Unmarshal([]byte(raw), &rel); err != nil {
		return nil, fmt.Errorf("unmarshal release: %w", err)
	}
	return &rel, nil
}

func (r *Remote) mapError(err error) error {
	var httpErr HttpError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 403:
			return fmt.Errorf("rate limit exceeded (HTTP 403). Set GITHUB_TOKEN for higher limits")
		case 401:
			return fmt.Errorf("authentication failed (HTTP 401). Check your GITHUB_TOKEN")
		}
	}
	return err
}
