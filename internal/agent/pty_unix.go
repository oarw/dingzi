//go:build unix

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/creack/pty"
)

// unixPTY is a shell running on a pseudo-terminal.
type unixPTY struct {
	f     *os.File
	cmd   *exec.Cmd
	shell string
}

// startPTY starts an interactive shell attached to a new pty.
//
// The shell runs as whatever user the agent runs as. Monitoring agents are
// commonly run as root so they can read every mount and the sensors, and in
// that case this is a root shell. That is not hidden behind a flag name: the
// agent logs it at startup and the README says it plainly, because an operator
// enabling this on a container needs to know what they are opening.
func startPTY(cols, rows uint16) (ptySession, error) {
	shell, args, err := findShell()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(shell, args...)
	cmd.Env = shellEnv()
	cmd.Dir = shellDir()

	// StartWithSize sets the pty up as the child's controlling terminal in a new
	// session. That is what makes job control work — without a controlling
	// terminal, Ctrl-C reaches nothing and the shell looks broken in a way that
	// is hard to diagnose from the browser.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", shell, err)
	}
	return &unixPTY{f: f, cmd: cmd, shell: shell}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPTY) Shell() string               { return p.shell }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close ends the session and reaps the child.
//
// All three steps matter. Closing the master alone leaves a shell that ignores
// SIGHUP running forever; killing the process alone leaves anything it started
// behind; and skipping the wait leaves a zombie per session, so a long-lived
// agent slowly fills the process table.
func (p *unixPTY) Close() error {
	err := p.f.Close()

	if p.cmd.Process != nil {
		// Negative pid signals the whole process group, which exists because the
		// child was made a session leader. This catches the shell's children —
		// the editor someone left open — not just the shell.
		if kerr := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); kerr != nil {
			// Already gone, or never in its own group. Fall back to the process.
			_ = p.cmd.Process.Kill()
		}
		_, _ = p.cmd.Process.Wait()
	}
	return err
}

// isPTYClosed reports whether a read error means the shell exited rather than
// something going wrong.
//
// Reading a pty master whose slave has closed gives EIO on Linux, not EOF. A
// session that treated that as a fault would log an error every single time a
// shell exited normally, which is most of them.
func isPTYClosed(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

// candidateShells is the search order.
//
// bash first where it exists because it is the better interactive shell, then
// the POSIX shell every image has, then Alpine's ash, then busybox — which is
// the case that matters for the containers this feature exists for, where none
// of the others are present.
var candidateShells = []struct {
	path string
	args []string
}{
	{path: "/bin/bash"},
	{path: "/usr/bin/bash"},
	{path: "/bin/sh"},
	{path: "/bin/ash"},
	{path: "/bin/dash"},
	{path: "/busybox", args: []string{"sh"}},
	{path: "/bin/busybox", args: []string{"sh"}},
}

// findShell picks an interpreter that exists and is executable.
func findShell() (string, []string, error) {
	// $SHELL first: an operator who set it meant it.
	if s := os.Getenv("SHELL"); s != "" && executable(s) {
		return s, nil, nil
	}
	for _, c := range candidateShells {
		if executable(c.path) {
			return c.path, c.args, nil
		}
	}
	// A distroless or scratch image genuinely has no shell. Saying so is more
	// useful than a generic failure, because there is nothing to fix on the
	// panel side.
	return "", nil, errors.New(
		"这台机器上找不到可用的 shell（镜像里可能没有 /bin/sh）")
}

func executable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

// shellEnv builds the child environment.
func shellEnv() []string {
	env := os.Environ()

	// TERM must be set or every full-screen tool (top, vi, less) fails or
	// renders garbage. The agent's own environment usually has no TERM at all,
	// because it was started by an init system.
	env = append(env, "TERM=xterm-256color")

	if os.Getenv("HOME") == "" {
		env = append(env, "HOME=/root")
	}
	// A UTF-8 locale only if none is configured, so Chinese filenames and output
	// survive the trip. Overriding a configured locale would be worse than
	// leaving it alone.
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" {
		env = append(env, "LANG=C.UTF-8")
	}
	return env
}

// shellDir picks a working directory that exists.
func shellDir() string {
	if home := os.Getenv("HOME"); home != "" {
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			return home
		}
	}
	return string(filepath.Separator)
}
