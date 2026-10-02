//go:build linux || darwin

package distribution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// buildCommand isolates tool descendants so cancellation stops compilers too,
// rather than killing only their parent Go process and leaving work running.
func buildCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return command
}
