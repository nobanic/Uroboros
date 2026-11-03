package stackupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeExecutor struct {
	calls  []string
	result []byte
	fail   error
}

func (fake *fakeExecutor) execute(_ context.Context, name string, args []string, opts commandOptions) ([]byte, error) {
	fake.calls = append(fake.calls, name+" "+strings.Join(args, " "))
	return fake.result, fake.fail
}

func testController(t *testing.T) (*Controller, *fakeExecutor) {
	t.Helper()
	fake := &fakeExecutor{result: []byte(strings.Repeat("b", 40) + "\trefs/heads/main\n")}
	c := New(Config{Project: "owned-update-test", Token: strings.Repeat("a", 64), Revision: strings.Repeat("c", 40), Seed: strings.Repeat("c", 40), Port: "18080"})
	c.root, c.run = t.TempDir(), fake
	return c, fake
}

func TestUpdateHTTPAuthenticationAndFrozenSelection(t *testing.T) {
	for _, test := range []struct {
		name, path, token, body string
		code, calls             int
	}{
		{"unauthenticated check", "/check", "", "", 401, 0},
		{"wrong token", "/apply", "wrong", `{}`, 401, 0},
		{"arbitrary command rejected", "/apply", "valid", `{"target":"` + strings.Repeat("b", 40) + `","command":"docker ps"}`, 400, 0},
		{"moving ref rejected", "/apply", "valid", `{"target":"main"}`, 400, 0},
		{"multiple json bodies rejected", "/apply", "valid", `{"target":"` + strings.Repeat("b", 40) + `"}{}`, 400, 0},
		{"stale selection rejected", "/apply", "valid", `{"target":"` + strings.Repeat("d", 40) + `"}`, 409, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, fake := testController(t)
			method := "GET"
			if test.body != "" {
				method = "POST"
			}
			request := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			token := test.token
			if token == "valid" {
				token = c.Config.Token
			}
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			writer := httptest.NewRecorder()
			c.Handler().ServeHTTP(writer, request)
			if writer.Code != test.code || len(fake.calls) != test.calls {
				t.Fatalf("code=%d calls=%v body=%s", writer.Code, fake.calls, writer.Body.String())
			}
		})
	}
}

func TestSourceChecksUseCommitIdentityAndCache(t *testing.T) {
	c, fake := testController(t)
	for _, path := range []string{"/check", "/check", "/check?force=1"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Authorization", "Bearer "+c.Config.Token)
		writer := httptest.NewRecorder()
		c.Handler().ServeHTTP(writer, request)
		var body map[string]any
		_ = json.Unmarshal(writer.Body.Bytes(), &body)
		if body["has_update"] != true || body["latest_revision"] != strings.Repeat("b", 40) || body["mode"] != "docker-source" {
			t.Fatal(body)
		}
	}
	if len(fake.calls) != 2 {
		t.Fatal(fake.calls)
	}
	fake.result = []byte("bad branch data")
	if _, err := c.latestRevision(context.Background(), true); err == nil {
		t.Fatal("invalid remote revision accepted")
	}
}

func TestDuplicateUpdateDoesNotCreateAnotherWorker(t *testing.T) {
	c, fake := testController(t)
	if err := os.Mkdir(c.path("job.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/apply", strings.NewReader(`{"target":"`+strings.Repeat("b", 40)+`"}`))
	r.Header.Set("Authorization", "Bearer "+c.Config.Token)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, r)
	if w.Code != 409 || len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "git ls-remote ") {
		t.Fatalf("%d %v", w.Code, fake.calls)
	}
}

func TestCommandErrorsRedactInternalCredentials(t *testing.T) {
	c, fake := testController(t)
	t.Setenv("POSTGRES_PASSWORD", "owned-private-password")
	fake.fail = errors.New("failure owned-private-password")
	_, err := c.command(context.Background(), "docker", nil, commandOptions{})
	if err == nil || strings.Contains(err.Error(), "owned-private-password") {
		t.Fatal(err)
	}
	fake.fail = errors.New(strings.Repeat("build progress\n", 300) + "owned-private-password: final build failure")
	_, err = c.command(context.Background(), "docker", nil, commandOptions{})
	if err == nil || len(err.Error()) > 1600 || strings.Contains(err.Error(), "owned-private-password") || !strings.HasSuffix(err.Error(), "final build failure") {
		t.Fatal("long errors must retain their final cause while redacting credentials", err)
	}
}

func TestPrivateGitHubCredentialIsStoredWithoutDisclosureAndCanBeCleared(t *testing.T) {
	c, fake := testController(t)
	token := "owned-private-github-read-token"
	request := httptest.NewRequest("POST", "/credentials", strings.NewReader(`{"token":"`+token+`"}`))
	request.Header.Set("Authorization", "Bearer "+c.Config.Token)
	writer := httptest.NewRecorder()
	c.Handler().ServeHTTP(writer, request)
	if writer.Code != 200 || strings.Contains(writer.Body.String(), token) || c.githubToken() != token {
		t.Fatal("credential was not stored privately")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	fake.fail = errors.New("private transport " + token + " " + encoded)
	request = httptest.NewRequest("GET", "/check?force=1", nil)
	request.Header.Set("Authorization", "Bearer "+c.Config.Token)
	writer = httptest.NewRecorder()
	c.Handler().ServeHTTP(writer, request)
	if strings.Contains(writer.Body.String(), token) || strings.Contains(writer.Body.String(), encoded) || !strings.Contains(writer.Body.String(), `"github_token_configured":true`) {
		t.Fatal("GitHub credential leaked in update status")
	}
	t.Setenv("UROBOROS_UPDATE_GITHUB_TOKEN", "owned-legacy-token")
	request = httptest.NewRequest("POST", "/credentials", strings.NewReader(`{"token":""}`))
	request.Header.Set("Authorization", "Bearer "+c.Config.Token)
	writer = httptest.NewRecorder()
	c.Handler().ServeHTTP(writer, request)
	if writer.Code != 200 || c.githubToken() != "" {
		t.Fatal("clear did not select anonymous public access")
	}
}

func TestActiveAPILabJobBlocksReplacement(t *testing.T) {
	c, fake := testController(t)
	job := &updateJob{c: c, oldContainers: map[string]containerInfo{"api-lab": {ID: "owned-api-lab"}}}
	for _, result := range []string{"1\n", "invalid"} {
		fake.result = []byte(result)
		if err := job.sidecarsIdle(context.Background()); err == nil {
			t.Fatal("unsafe API Lab response admitted")
		}
	}
	fake.result = []byte("0\n")
	if err := job.sidecarsIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDockerBindPathsUseDaemonMountSources(t *testing.T) {
	if path := daemonPath(`C:\Users\owner\Private State`); path != "/run/desktop/mnt/host/c/Users/owner/Private State" {
		t.Fatal(path)
	}
	if path := daemonPath("/srv/private/state"); path != "/srv/private/state" {
		t.Fatal(path)
	}
	for _, test := range []struct{ source, want string }{
		{"/state/releases/revision/clean/bootstrap.py", "/run/desktop/mnt/host/c/Private State/updates/releases/revision/clean/bootstrap.py"},
		{"/install/clean/api-lab-identities.json", "/run/desktop/mnt/host/c/Release/clean/api-lab-identities.json"},
	} {
		got, err := mapBind(test.source, "/state/releases/revision", "/run/desktop/mnt/host/c/Private State/updates/releases/revision", "/run/desktop/mnt/host/c/Private State/updates", "/run/desktop/mnt/host/c/Release")
		if err != nil || got != test.want {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := mapBind("/etc/passwd", "/candidate", "/host/candidate", "/host/state", "/host/install"); err == nil {
		t.Fatal("foreign bind allowed")
	}
}

func tarBytes(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestSourceArchiveCannotEscapeItsCandidate(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `folder\escape`} {
		t.Run(name, func(t *testing.T) {
			data := tarBytes(t, []*tar.Header{{Name: name, Mode: 0600, Typeflag: tar.TypeReg}})
			if err := extractTar(bytes.NewReader(data), t.TempDir(), false); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	if runtime.GOOS != "windows" {
		data := tarBytes(t, []*tar.Header{{Name: "escape", Linkname: "..", Typeflag: tar.TypeSymlink}})
		if err := extractTar(bytes.NewReader(data), t.TempDir(), false); err == nil {
			t.Fatal("escaping symlink accepted")
		}
	}
}

func TestActualGitArchiveWithGlobalMetadataExtracts(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.Command(git, append([]string{"-c", "safe.directory=" + root, "-c", "core.autocrlf=false", "-c", "core.eol=lf", "-c", "user.name=Owned fixture", "-c", "user.email=fixture@local.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, output)
		}
		return output
	}
	run("init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("owned source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "--", "README.md")
	run("commit", "--quiet", "-m", "Owned source export")
	archive := run("archive", "HEAD")
	destination := t.TempDir()
	if err := extractTar(bytes.NewReader(archive), destination, false); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(destination, "README.md"))
	if err != nil || string(bytes) != "owned source\n" {
		t.Fatalf("Git archive was not restored: %q %v", bytes, err)
	}
}

func TestVolumeSnapshotRestoresFilesAndRejectsCorruptionBeforeDeleting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("volume ownership is tested inside the Linux updater image")
	}
	backup, volumes := t.TempDir(), t.TempDir()
	root := filepath.Join(volumes, "uroboros_data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "settings.txt")
	if err := os.WriteFile(file, []byte("original private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "shared")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0770|os.ModeSetgid|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("settings.txt", filepath.Join(root, "settings-link")); err != nil {
		t.Fatal(err)
	}
	if err := VolumeSnapshot(false, backup, volumes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed by failed upgrade"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VolumeSnapshot(true, backup, volumes); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(file)
	if err != nil || string(bytes) != "original private fixture" {
		t.Fatal(string(bytes), err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != 0770|os.ModeSetgid|os.ModeSticky {
		t.Fatal("directory permissions were not restored", info, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "settings-link")); err != nil || target != "settings.txt" {
		t.Fatal("symlink was not restored", target, err)
	}
	archive := filepath.Join(backup, "uroboros_data.tar.gz")
	f, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "corrupt")
	_ = f.Close()
	if err := VolumeSnapshot(true, backup, volumes); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	bytes, err = os.ReadFile(file)
	if err != nil || string(bytes) != "original private fixture" {
		t.Fatal("corruption test modified live data")
	}
}

var _ http.Handler = (*http.ServeMux)(nil)
