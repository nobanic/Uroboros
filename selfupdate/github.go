package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is the release source. It is hard-coded rather than made configurable: a configurable
// update source would hand anyone who can edit the config a remote-code-execution channel, and
// for a penetration testing platform that is a hole that must not be opened.
// The fork's release repository is private. Authenticated release/asset downloads
// are not wired yet; anonymous update checks may return 404 until they are.
const Repo = "nobanic/UroborOS"

// latestURL is GitHub's "latest stable release" endpoint. It automatically skips prereleases and drafts.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts limits which domains the upgrade chain may reach. Together with checkRedirect below,
// any hop redirected to a host not on the list fails outright — this is the first gate against DNS
// poisoning / a man-in-the-middle swapping the binary; the second is the SHA256SUMS comparison.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // the object storage where release assets actually land
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release is the set of fields we care about in a GitHub Release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a file attached to a Release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient builds an HTTP client that recognizes only GitHub domains. An empty proxy means a direct connection.
//
// It deliberately does not reuse the default Transport: the upgrade chain must force TLS and verify the
// certificate, and must not be affected by an InsecureSkipVerify or the like set elsewhere.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // downloading the whole package, so it must not be killed by a per-request timeout
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL enforces https + the domain allowlist.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("rejecting non-HTTPS address: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("rejecting non-GitHub domain: %s", u.Hostname())
	}
	return nil
}

// FetchLatest queries the latest stable release.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	return (releaseSource{ctx, c}).latest()
}

// AssetName returns the release-package name for the current platform, consistent with build.sh's package_binary:
// uroboros-<version>-<os>-<arch>.zip (the version has no v prefix).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("uroboros-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset finds an asset in the Release by name.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
