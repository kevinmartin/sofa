//go:build linux || darwin

package agent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	acp "github.com/caelis-labs/acp-go-sdk"
	"golang.org/x/sys/unix"
)

func configureProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// openFile pins each directory descriptor and rejects symlinks atomically. The
// workspace itself is a trusted sandbox mount; an agent cannot replace its parent.
func (c *client) openParent(path string) (int, string, error) {
	rel, err := c.relative(path)
	if err != nil {
		return -1, "", ErrPermission
	}
	fd, err := unix.Open(c.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", ErrPermission
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		next, e := unix.Openat(fd, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, "", ErrPermission
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func (c *client) permissionPath(path string, write bool) bool {
	fd, name, err := c.openParent(path)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	err = unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == unix.ENOENT {
		return write
	}
	return err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1
}

func (c *client) openFile(path string, write bool) (*os.File, error) {
	fd, name, err := c.openParent(path)
	if err != nil {
		return nil, ErrPermission
	}
	defer unix.Close(fd)
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if write {
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fileFD, err := unix.Openat(fd, name, flags, 0600)
	if err != nil {
		return nil, ErrPermission
	}
	f := os.NewFile(uintptr(fileFD), "workspace-file")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, ErrPermission
	}
	// Hard links are not accepted as an alternate name for protected files.
	var stat unix.Stat_t
	if unix.Fstat(fileFD, &stat) != nil || stat.Nlink != 1 {
		f.Close()
		return nil, ErrPermission
	}
	return f, nil
}

func (c *client) validSession(id acp.SessionId) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session != "" && c.session == id
}
func (c *client) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if ctx.Err() != nil || !c.validSession(p.SessionId) {
		return acp.ReadTextFileResponse{}, ErrPermission
	}
	f, err := c.openFile(p.Path, false)
	if err != nil {
		return acp.ReadTextFileResponse{}, ErrPermission
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return acp.ReadTextFileResponse{}, ErrPermission
	}
	lines := strings.Split(string(b), "\n")
	start := 0
	if p.Line != nil {
		if *p.Line == 0 {
			return acp.ReadTextFileResponse{}, ErrPermission
		}
		start = int(*p.Line - 1)
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if p.Limit != nil && uint64(start)+uint64(*p.Limit) < uint64(end) {
		end = start + int(*p.Limit)
	}
	return acp.ReadTextFileResponse{Content: strings.Join(lines[start:end], "\n")}, nil
}
func (c *client) WriteTextFile(ctx context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if ctx.Err() != nil || !c.validSession(p.SessionId) || len(p.Content) > 1<<20 {
		return acp.WriteTextFileResponse{}, ErrPermission
	}
	f, err := c.openFile(p.Path, true)
	if err != nil {
		return acp.WriteTextFileResponse{}, ErrPermission
	}
	defer f.Close()
	if f.Truncate(0) != nil {
		return acp.WriteTextFileResponse{}, ErrPermission
	}
	if _, err = f.WriteString(p.Content); err != nil {
		return acp.WriteTextFileResponse{}, ErrPermission
	}
	return acp.WriteTextFileResponse{}, nil
}
