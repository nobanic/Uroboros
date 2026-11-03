package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// releaseSource is the common allowlisted request boundary for metadata,
// manifests and archives. Callers retain their distinct HTTP/error contracts.
type releaseSource struct {
	context context.Context
	client  *http.Client
}

type releaseResponse struct {
	response         *http.Response
	failure          error
	transportFailure bool
}

func (source releaseSource) request(endpoint, accept string) releaseResponse {
	request, failure := http.NewRequestWithContext(source.context, http.MethodGet, endpoint, nil)
	if failure == nil {
		failure = checkURL(request.URL)
	}
	if failure != nil {
		return releaseResponse{failure: failure}
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	request.Header.Set("User-Agent", "uroboros-selfupdate")
	response, failure := source.client.Do(request)
	return releaseResponse{response, failure, failure != nil}
}

func (source releaseSource) latest() (*Release, error) {
	result := source.request(latestURL, "application/vnd.github+json")
	if result.transportFailure {
		return nil, fmt.Errorf("failed to reach GitHub (you can configure a global proxy in system settings): %w", result.failure)
	}
	if result.failure != nil {
		return nil, result.failure
	}
	defer result.response.Body.Close()
	status := result.response.StatusCode
	switch status {
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API rate limited (60 per hour), please try again later")
	case http.StatusNotFound:
		return nil, fmt.Errorf("repository %s is unavailable or has no stable release (private repositories require authentication)", Repo)
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("GitHub returned %d", status)
	}
	var release Release
	if failure := json.NewDecoder(result.response.Body).Decode(&release); failure != nil {
		return nil, fmt.Errorf("failed to parse Release: %w", failure)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("Release is missing a tag")
	}
	return &release, nil
}

func (source releaseSource) body(endpoint string) (io.ReadCloser, error) {
	result := source.request(endpoint, "")
	if result.failure != nil {
		return nil, result.failure
	}
	if result.response.StatusCode == http.StatusOK {
		return result.response.Body, nil
	}
	result.response.Body.Close()
	return nil, fmt.Errorf("HTTP %d", result.response.StatusCode)
}

func (source releaseSource) sums(release *Release) (map[string]string, error) {
	manifest, found := release.FindAsset(sumsAsset)
	if !found {
		return nil, fmt.Errorf("this Release has no %s, integrity cannot be verified, refusing to upgrade", sumsAsset)
	}
	reader, failure := source.body(manifest.URL)
	if failure != nil {
		return nil, fmt.Errorf("download %s: %w", sumsAsset, failure)
	}
	defer reader.Close()
	bytes, failure := io.ReadAll(io.LimitReader(reader, 1<<20))
	if failure != nil {
		return nil, fmt.Errorf("read %s: %w", sumsAsset, failure)
	}
	entries := parseSums(string(bytes))
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s is empty or its format is unrecognizable", sumsAsset)
	}
	return entries, nil
}

// releaseStage owns one acquisition attempt. Its temporary files stay beside
// the target executable and are removed before returning on every path.
type releaseStage struct {
	source          releaseSource
	release         *Release
	previous        string
	progress        Progress
	paths           Paths
	asset           Asset
	name, expected  string
	archive, binary string
}

func (stage *releaseStage) prepare() error {
	if stage.progress == nil {
		stage.progress = func(Phase, int, string) {}
	}
	paths, failure := ResolvePaths()
	if failure != nil {
		return failure
	}
	stage.paths = paths
	if failure = checkWritable(paths.Dir); failure != nil {
		return failure
	}
	stage.name = AssetName(stage.release.TagName, runtime.GOOS, runtime.GOARCH)
	var found bool
	stage.asset, found = stage.release.FindAsset(stage.name)
	if !found {
		return fmt.Errorf("this version does not provide a release package for %s/%s (missing %s)", runtime.GOOS, runtime.GOARCH, stage.name)
	}
	stage.progress(PhaseDownload, 0, "fetching the checksum manifest…")
	manifest, failure := stage.source.sums(stage.release)
	if failure != nil {
		return failure
	}
	stage.expected, found = manifest[stage.name]
	if !found {
		return fmt.Errorf("%s does not list %s, refusing to install an unverified binary", sumsAsset, stage.name)
	}
	stage.archive, stage.binary = paths.New+".zip.part", paths.New+".part"
	return nil
}

func (stage *releaseStage) acquire() error {
	stage.progress(PhaseDownload, 0, fmt.Sprintf("downloading %s (%s)…", stage.name, humanSize(stage.asset.Size)))
	actual, failure := stage.source.download(stage.asset, stage.archive, stage.progress)
	if failure != nil {
		return failure
	}
	stage.progress(PhaseVerify, -1, "verifying SHA256…")
	if !strings.EqualFold(actual, stage.expected) {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s (download corrupted or tampered with)", short(stage.expected), short(actual))
	}
	stage.progress(PhaseExtract, -1, "extracting and smoke-testing…")
	if failure = extractUpdateArtifact(stage.archive, stage.binary); failure != nil {
		return failure
	}
	if failure = smokeTest(stage.binary); failure != nil {
		return fmt.Errorf("the new version cannot run on the current system: %w", failure)
	}
	return nil
}

func (stage *releaseStage) publish() error {
	digest, failure := fileSHA256(stage.binary)
	if failure != nil {
		return fmt.Errorf("compute the new binary's checksum: %w", failure)
	}
	if failure = os.WriteFile(stage.paths.Sum, []byte(digest), 0o644); failure != nil {
		return fmt.Errorf("write checksum: %w", failure)
	}
	if failure = os.Rename(stage.binary, stage.paths.New); failure != nil {
		_ = os.Remove(stage.paths.Sum)
		return fmt.Errorf("stage the new version: %w", failure)
	}
	state := marker{From: stage.previous, To: strings.TrimPrefix(stage.release.TagName, "v"), StagedAt: time.Now().Unix()}
	if failure = writeMarker(stage.paths.Marker, state); failure != nil {
		stage.progress(PhaseStaged, -1, "warning: failed to write the upgrade marker; this upgrade will have no automatic-rollback protection")
	}
	stage.progress(PhaseStaged, 100, "the new version is ready, restarting…")
	return nil
}

func (stage releaseStage) run() error {
	if failure := stage.prepare(); failure != nil {
		return failure
	}
	defer func() {
		_ = os.Remove(stage.archive)
		_ = os.Remove(stage.binary)
	}()
	if failure := stage.acquire(); failure != nil {
		return failure
	}
	return stage.publish()
}
