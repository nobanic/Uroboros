package stackupdate

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type updateJob struct {
	c                                      *Controller
	status                                 Status
	self                                   containerInfo
	hostState, hostInstall                 string
	previous, candidate                    model
	oldContainers                          map[string]containerInfo
	previousPath, candidatePath, backupDir string
	maintenance, stopped, snapshotReady    bool
}

func (c *Controller) Run(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, time.Hour)
	defer cancel()
	job := &updateJob{c: c, status: c.status()}
	if !job.status.Running || !revisionPattern.MatchString(job.status.Target) || !projectPattern.MatchString(c.Config.Project) || strings.ContainsAny(job.status.Job, `/\\`) {
		return errors.New("there is no valid pending update")
	}
	defer func() {
		if !job.status.RecoveryRequired {
			_ = os.Remove(c.path("job.lock"))
		}
	}()
	err := job.run(ctx)
	if err != nil {
		job.status.FailedAt = job.status.Phase
		// Recovery has an independent deadline: a canceled build/deployment must
		// not prevent restarting the previously captured installation.
		recovery, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		if job.stopped {
			job.phase("recovering", "Restoring the previous installation and its data.")
			if restoreErr := job.recover(recovery); restoreErr != nil {
				err = fmt.Errorf("%w; automatic recovery failed: %v; the private backup is retained", err, restoreErr)
				job.status.RecoveryRequired = true
			}
		} else if job.maintenance {
			_ = job.guard(recovery, false)
		}
		job.status.Phase, job.status.Message, job.status.Error, job.status.Running = "failed", "Update failed; inspect the recovery result before retrying.", err.Error(), false
		if job.status.RecoveryRequired {
			job.status.Message = "Automatic recovery needs operator attention. The backup and update lock were retained."
		} else if job.stopped {
			job.status.Message = "Update failed. The previous installation and its data were restored."
		} else {
			job.status.Message = "Update failed before replacement. The current installation remains available."
		}
		_ = c.writeStatus(job.status)
		return err
	}
	job.status.Phase, job.status.Message, job.status.Running = "complete", "The selected source revision is healthy. Your installation data was retained.", false
	return c.writeStatus(job.status)
}

func (j *updateJob) phase(phase, message string) {
	j.status.Phase, j.status.Message = phase, message
	_ = j.c.writeStatus(j.status)
}

func (j *updateJob) run(ctx context.Context) error {
	c := j.c
	var err error
	j.self, j.hostState, j.hostInstall, err = c.self(ctx)
	if err != nil {
		return err
	}
	j.phase("downloading", "Fetching the selected commit from main.")
	root, err := j.source(ctx)
	if err != nil {
		return err
	}
	j.phase("verifying", "Checking the source, runtime archives and installation boundaries.")
	if _, err := c.command(ctx, "bash", []string{filepath.Join(root, "clean", "verify-language.sh"), "--root", root}, commandOptions{}); err != nil {
		return err
	}
	j.candidate, err = c.render(ctx, root, j.status.Target, j.hostState+"/releases/"+j.status.Target+"/"+j.status.Job, j.hostState, j.hostInstall)
	if err != nil {
		return err
	}
	var installed baseline
	if readJSON(c.path("baseline-"+c.Config.Seed+".json"), &installed) != nil || installed.Compose == nil {
		return errors.New("the installed source baseline is missing")
	}
	j.previous = installed.Compose
	var active Active
	if readJSON(c.path("active.json"), &active) == nil && active.Seed == c.Config.Seed {
		if !regexp.MustCompile(`^candidate-[a-z0-9_.-]+\.json$`).MatchString(active.Compose) {
			return errors.New("invalid active Compose snapshot pointer")
		}
		if err := readJSON(c.path(active.Compose), &j.previous); err != nil {
			return errors.New("the current managed Compose snapshot is missing")
		}
	}
	if err := ensureCompatible(j.previous, j.candidate); err != nil {
		return err
	}
	j.oldContainers, err = c.capture(ctx, j.previous)
	if err != nil {
		return err
	}
	var databaseImages []struct {
		ID string `json:"Id"`
	}
	imageInfo, err := c.command(ctx, "docker", []string{"image", "inspect", textValue(object(object(j.candidate["services"])["postgres"])["image"])}, commandOptions{})
	if err != nil || json.Unmarshal(imageInfo, &databaseImages) != nil || len(databaseImages) != 1 || databaseImages[0].ID != j.oldContainers["postgres"].Image {
		return errors.New("PostgreSQL image upgrades require a separately reviewed database migration")
	}
	j.backupDir = c.path("backups", j.status.Job)
	j.previousPath, j.candidatePath = filepath.Join(j.backupDir, "previous-compose.json"), c.path("candidate-"+j.status.Job+".json")
	if err := writeJSON(j.previousPath, j.previous); err != nil {
		return err
	}
	if err := writeJSON(j.candidatePath, j.candidate); err != nil {
		return err
	}
	if err := j.archives(ctx, root); err != nil {
		return err
	}
	j.phase("building", "Building the application, frontend and tool services. The current console remains available.")
	if _, err := c.compose(ctx, j.candidatePath, "build", "uroboros", "arsenal", "api-lab", "tor-research", "updater"); err != nil {
		return err
	}
	j.phase("backing-up", "Waiting for idle operations, then saving a private recovery snapshot.")
	for _, before := range j.oldContainers {
		after, err := c.inspect(ctx, before.ID)
		if err != nil || after.ID != before.ID || after.Image != before.Image || after.Config.Labels["com.docker.compose.project"] != c.Config.Project {
			return errors.New("the installation changed during the build; check again before updating")
		}
	}
	if err := j.guard(ctx, true); err != nil {
		return err
	}
	j.maintenance = true
	if err := j.sidecarsIdle(ctx); err != nil {
		return err
	}
	// Stop only this installation's writers. PostgreSQL remains online for a
	// consistent pg_dump; the update controller/worker remain available.
	j.stopped = true
	if _, err := c.compose(ctx, j.previousPath, "stop", "--timeout", "45", "uroboros", "arsenal", "api-lab", "binary-worker", "binary-broker", "tor-research"); err != nil {
		return err
	}
	if err := j.backup(ctx); err != nil {
		return err
	}
	j.snapshotReady = true
	j.phase("restarting", "Installing the prepared containers and retaining the existing database and volumes.")
	if _, err := c.compose(ctx, j.candidatePath, "up", "--detach", "--no-build", "--pull", "never"); err != nil {
		return err
	}
	j.phase("checking", "Checking the new console and its source revision.")
	if err := j.healthy(ctx, j.status.Target); err != nil {
		return err
	}
	if err := writeJSON(c.path("active.json"), Active{Compose: filepath.Base(j.candidatePath), Revision: j.status.Target, Seed: c.Config.Seed, Source: "releases/" + j.status.Target + "/" + j.status.Job}); err != nil {
		return err
	}
	return nil
}

func (j *updateJob) source(ctx context.Context) (string, error) {
	c := j.c
	gitDir := c.path("repository.git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if _, err := c.command(ctx, "git", []string{"init", "--bare", gitDir}, commandOptions{}); err != nil {
			return "", err
		}
	}
	if _, err := c.command(ctx, "git", []string{"--git-dir", gitDir, "fetch", "--no-tags", c.sourceURL, "refs/heads/" + Branch}, commandOptions{}); err != nil {
		return "", err
	}
	output, err := c.command(ctx, "git", []string{"--git-dir", gitDir, "rev-parse", "FETCH_HEAD"}, commandOptions{})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(output)) != j.status.Target {
		return "", errors.New("main moved after the update was selected; check again")
	}
	root := c.path("releases", j.status.Target, j.status.Job)
	// A partially extracted candidate is never reused as verified source.
	if _, err := os.Stat(root); err == nil {
		return "", errors.New("this update worker already has an extracted candidate; inspect its retained attempt")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	archive, err := os.CreateTemp(c.root, ".source-*.tar")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	_, err = c.command(ctx, "git", []string{"--git-dir", gitDir, "archive", j.status.Target}, commandOptions{Stdout: archive})
	if err == nil {
		_, err = archive.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = extractTar(archive, root, false)
	}
	_ = archive.Close()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "clean", "updater", "Dockerfile")); err != nil {
		return "", errors.New("selected source does not support console-based Docker updates")
	}
	return root, nil
}

func (j *updateJob) archives(ctx context.Context, root string) error {
	archives := []struct{ File, Hash, Image string }{
		{"binary-broker-b05.tar", "f12520c790e04e3cddb0766ab8cc93213563c78c61a28f5874b61eeb6513ef6f", "uroboros-b0/binary-broker@sha256:0fc4e1bfe1c398f2371aa7b0f18c8b6a2fdbf3e3544b6ea3c3576fc734f58173"},
		{"binary-worker-b05.tar", "5cc2a489c15b2a85f8a65acaede4fc087040539840f330aa6617191dbb060123", "uroboros-b0/binary-worker@sha256:142bdcf03aa22e6a27fead6dedf89391155a46210375f007871bd09726315514"},
	}
	for _, archive := range archives {
		path := filepath.Join(root, "clean", "runtime-images", archive.File)
		actual, err := fileHash(path)
		if err != nil || actual != archive.Hash {
			return fmt.Errorf("accepted runtime archive is missing or changed: %s", archive.File)
		}
		if _, err := j.c.command(ctx, "docker", []string{"image", "inspect", archive.Image}, commandOptions{}); err == nil {
			continue
		}
		if _, err := j.c.command(ctx, "docker", []string{"image", "load", "--input", path}, commandOptions{}); err != nil {
			return err
		}
		if _, err := j.c.command(ctx, "docker", []string{"image", "inspect", archive.Image}, commandOptions{}); err != nil {
			return errors.New("runtime archive did not provide its pinned image")
		}
	}
	return nil
}

func (j *updateJob) guard(ctx context.Context, prepare bool) error {
	body := `{"prepare":false}`
	if prepare {
		body = `{"prepare":true}`
	}
	request, _ := http.NewRequestWithContext(ctx, "POST", "http://uroboros:8787/api/update/maintenance", strings.NewReader(body))
	request.Header.Set("X-Uroboros-Updater", j.c.Config.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("could not prepare the console for update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var detail struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&detail)
		return fmt.Errorf("console is not ready for update: %s", detail.Error)
	}
	return nil
}

func (j *updateJob) healthy(ctx context.Context, revision string) error {
	wait := j.c.healthyTimeout
	if wait == 0 {
		wait = 4 * time.Minute
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		request, _ := http.NewRequestWithContext(ctx, "GET", "http://uroboros:8787/api/health", nil)
		response, err := client.Do(request)
		if err == nil {
			var health struct {
				OK           bool   `json:"ok"`
				AuthDisabled bool   `json:"auth_disabled"`
				Revision     string `json:"source_revision"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&health)
			response.Body.Close()
			if err == nil && response.StatusCode == 200 && health.OK && !health.AuthDisabled && health.Revision == revision {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("new console did not become healthy with the expected source revision")
		case <-ticker.C:
		}
	}
}

func (j *updateJob) backup(ctx context.Context) error {
	postgres := j.oldContainers["postgres"]
	path := filepath.Join(j.backupDir, "postgres.dump")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = j.c.command(ctx, "docker", []string{"exec", postgres.ID, "pg_dump", "-U", "uroboros", "-d", "uroboros", "-Fc", "--create"}, commandOptions{Stdout: file})
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	file, err = os.Open(path)
	if err != nil {
		return err
	}
	_, err = j.c.command(ctx, "docker", []string{"exec", "-i", postgres.ID, "pg_restore", "--list"}, commandOptions{Stdin: file})
	file.Close()
	if err != nil {
		return errors.New("database backup could not be verified")
	}
	hash, err := fileHash(path)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(j.backupDir, "database.json"), map[string]string{"sha256": hash}); err != nil {
		return err
	}
	return j.volumeCommand(ctx, false)
}

func (j *updateJob) sidecarsIdle(ctx context.Context) error {
	// Read the existing store directly, without constructing Jobs/SourceScans:
	// their startup recovery changes active records to interrupted.
	const query = `import os,sqlite3,json
p=os.environ.get("API_LAB_DB","/var/lib/api-lab/jobs.sqlite3")
n=0
if os.path.isfile(p):
 c=sqlite3.connect("file:"+p+"?mode=ro",uri=True,timeout=5)
 tables={r[0] for r in c.execute("SELECT name FROM sqlite_master WHERE type='table'")}
 for table,active in (("jobs",("running","cancelling")),("source_scans",("snapshotting","scanning","cancelling"))):
  if table in tables:
   n+=sum(json.loads(r[0]).get("status") in active for r in c.execute("SELECT document FROM "+table))
 c.close()
print(n)`
	output, err := j.c.command(ctx, "docker", []string{"exec", j.oldContainers["api-lab"].ID, "python", "-c", query}, commandOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify API Lab jobs before updating: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || count < 0 {
		return errors.New("invalid API Lab job readiness response")
	}
	if count > 0 {
		return errors.New("finish or cancel active API Lab experiments and source scans before updating")
	}
	return nil
}

func (j *updateJob) volumeCommand(ctx context.Context, restore bool) error {
	mode := "snapshot"
	if restore {
		mode = "restore"
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges:true",
		"--mount", bindArgument(j.hostState+"/backups/"+j.status.Job, "/backup", false)}
	for role, raw := range object(j.previous["volumes"]) {
		if role == "postgres_data" {
			continue
		}
		name := textValue(object(raw)["name"])
		if name != j.c.Config.Project+"_"+role {
			return errors.New("volume ownership changed during backup")
		}
		mount := "type=volume,src=" + name + ",dst=/volumes/" + role
		if !restore {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount)
	}
	args = append(args, j.self.Image, mode)
	_, err := j.c.command(ctx, "docker", args, commandOptions{})
	return err
}

func (j *updateJob) recover(ctx context.Context) error {
	if j.snapshotReady {
		if _, err := j.c.compose(ctx, j.candidatePath, "stop", "--timeout", "30", "uroboros", "arsenal", "api-lab", "binary-worker", "binary-broker", "tor-research"); err != nil {
			return err
		}
		var meta struct {
			SHA256 string `json:"sha256"`
		}
		path := filepath.Join(j.backupDir, "postgres.dump")
		if readJSON(filepath.Join(j.backupDir, "database.json"), &meta) != nil {
			return errors.New("database backup manifest is missing")
		}
		hash, err := fileHash(path)
		if err != nil || hash != meta.SHA256 {
			return errors.New("database backup checksum changed")
		}
		if err := j.volumeCommand(ctx, true); err != nil {
			return err
		}
		if _, err := j.c.compose(ctx, j.previousPath, "up", "--detach", "--no-build", "--pull", "never", "--no-deps", "postgres"); err != nil {
			return err
		}
		ids, err := j.c.command(ctx, "docker", []string{"ps", "-a", "--filter", "label=com.docker.compose.project=" + j.c.Config.Project, "--filter", "label=com.docker.compose.service=postgres", "--format", "{{.ID}}"}, commandOptions{})
		if err != nil || len(strings.Fields(string(ids))) != 1 {
			return errors.New("cannot identify the recovery database container")
		}
		postgresID := strings.TrimSpace(string(ids))
		ready := false
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
			if _, err := j.c.command(ctx, "docker", []string{"exec", postgresID, "pg_isready", "-U", "uroboros", "-d", "postgres"}, commandOptions{}); err == nil {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		if !ready {
			return errors.New("the recovery database did not become ready")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		// Recreate the database from its verified --create dump. Cleaning only
		// objects listed in the old dump would retain tables added by a bad upgrade.
		if _, err := j.c.command(ctx, "docker", []string{"exec", postgresID, "dropdb", "-U", "uroboros", "--if-exists", "--force", "uroboros"}, commandOptions{}); err != nil {
			return err
		}
		if _, err := j.c.command(ctx, "docker", []string{"exec", "-i", postgresID, "pg_restore", "-U", "uroboros", "-d", "postgres", "--create", "--exit-on-error"}, commandOptions{Stdin: file}); err != nil {
			return err
		}
	}
	if _, err := j.c.compose(ctx, j.previousPath, "up", "--detach", "--no-build", "--pull", "never"); err != nil {
		return err
	}
	if err := j.healthy(ctx, j.status.Current); err != nil {
		return err
	}
	return j.guard(ctx, false)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractTar rejects path traversal and symlink parents before creating files.
// Source exports never write outside the private candidate directory.
func extractTar(reader io.Reader, root string, ownership bool) error {
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." {
			continue
		}
		if strings.HasPrefix(header.Name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.Contains(header.Name, `\`) {
			return errors.New("archive contains an unsafe path")
		}
		target := filepath.Join(root, name)
		parent := filepath.Dir(target)
		for p := parent; p != root; p = filepath.Dir(p) {
			if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("archive contains a symlink parent")
			}
		}
		if err := os.MkdirAll(parent, 0755); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tarReader)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			link := filepath.Clean(filepath.Join(filepath.Dir(name), filepath.FromSlash(header.Linkname)))
			if !ownership && (filepath.IsAbs(header.Linkname) || link == ".." || strings.HasPrefix(link, ".."+string(filepath.Separator))) {
				return errors.New("archive symlink escapes its volume")
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return errors.New("archive contains an unsupported file type")
		}
		if ownership {
			if err := os.Lchown(target, header.Uid, header.Gid); err != nil {
				return err
			}
		}
		if header.Typeflag != tar.TypeSymlink {
			if err := os.Chmod(target, header.FileInfo().Mode()); err != nil {
				return err
			}
		}
	}
}
