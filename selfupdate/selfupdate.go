// Package selfupdate implements Uroboros's in-app one-click update: pulling the new binary from a
// GitHub Release, verifying it, staging it, and atomically swapping it in on the next startup.
//
// Division of labor (see start.sh / start.bat):
//
//	the startup script = a dumb supervisor loop, responsible only for "deciding whether to relaunch after the process exits based on the exit code"
//	this package       = all the error-prone logic (download / SHA256 verification / smoke test / swap / rollback on failure)
//
// The reason the swap is in Go rather than the script is that SHA256 verification and the smoke test
// would have to be written twice for sh and bat (sha256sum / shasum / certutil), and this is precisely
// the one part that must not go wrong — swap in a binary that cannot run and the supervisor will
// faithfully relaunch it over and over, leaving the user to rescue it by hand on the machine.
//
// One full upgrade goes through three process startups:
//
//	① the old server receives /api/update/apply → download and verify → stage uroboros.new → exit 75
//	② the script relaunches the old version → Bootstrap finds uroboros.new → verify + smoke test → swap → exit 75
//	③ the script relaunches, now the new version → Bootstrap records one attempt → clears the marker after a successful start
//
// Any failing step reverts to the old version: ② on a failed verification, delete the staged file and keep running the old version;
// ③ failing to survive to clearing the marker 3 times in a row (crashing on start) automatically swaps uroboros.old back.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the exit code for "please, supervisor, relaunch me" (EX_TEMPFAIL). When the startup script sees it,
// it reruns immediately and does not count it toward crash backoff. 0 means the user stopped normally (the script exits the loop); everything else is treated as a crash.
const ExitRestart = 75

// maxAttempts is the number of startup attempts allowed after a swap. The new version increments the count by 1 on every
// startup, and surviving settleDelay clears the marker; crashing maxAttempts times in a row means the new version simply cannot start, so it is rolled back automatically.
const maxAttempts = 3

// Paths is all the files involved in one upgrade, all kept under the **directory of the executable**.
// CWD is deliberately not used: when run as a service the working directory may be / or any path, and using CWD would make the staged file land
// elsewhere, breaking the swap logic outright.
type Paths struct {
	Dir     string // the directory of the executable
	Current string // the currently running binary        uroboros      / uroboros.exe
	New     string // the staged new version               uroboros.new  / uroboros.new.exe
	Sum     string // the new version's sha256 (hex)        uroboros.new.sha256 / uroboros.new.exe.sha256
	Old     string // the old version backed up before the swap   uroboros.old  / uroboros.old.exe
	Marker  string // the upgrade-state marker              uroboros.upgrade.json
}

// ResolvePaths derives all upgrade paths from the current executable.
//
// On Windows the .new/.old must also carry the .exe suffix, or both the smoke test and the post-swap execution fail,
// so the suffix is stripped first and then re-appended, making the naming symmetric across both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("locate the executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // ".exe" on Windows, usually empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker records the progress of one swap, used to trigger an automatic rollback when the new version cannot start.
type marker struct {
	From     string `json:"from"`     // the version before the upgrade
	To       string `json:"to"`       // the target version
	Attempts int    `json:"attempts"` // the number of startup attempts since the swap
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged removes the staged file. A successful swap, a failed verification, and a user cancel all go through it, to avoid a leftover
// uroboros.new being retried on the next startup.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions compares two version numbers, returning -1/0/1 (a<b / a==b / a>b).
// ok=false means at least one side is not a comparable version number (for example a local development build's "dev" or
// a git describe output like "0.3.7-2-gabc1234-dirty"), in which case the caller should disable the one-click update,
// otherwise it would "upgrade" an in-development build to a stable release and overwrite uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses a version number of the form "v0.3.7" / "0.3.7" into a [3]int.
//
// It accepts only a clean three-part form: on a non-tag build, build.sh uses git describe to produce
// versions with a suffix like "0.3.7-2-gabc1234", which must be judged non-comparable rather than treated as
// 0.3.7 — otherwise a development build would be mistaken for "already the latest" or overwritten by a stable release.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether the process is running inside a container. Under Docker the swap writes to the container's writable layer,
// and `docker compose up -d` rebuilding the container reverts to the version bundled in the image — this is expected behavior
// (at that point the user is pulling a new image anyway), but the frontend needs to be able to state this clearly.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
