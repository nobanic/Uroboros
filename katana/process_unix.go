//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package katana

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcessTree puts Katana and any Chromium descendants in their own
// process group. Canceling a task must stop the whole tree, not leave a browser
// behind after os/exec kills only the immediate crawler process.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
}
