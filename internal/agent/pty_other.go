//go:build !unix

package agent

import (
	"errors"
	"os"
)

// startPTY reports that this platform has no supported pty.
//
// Windows can do this through ConPTY, and the interface here is shaped so it
// could be added without touching anything else. It is not implemented because
// the feature exists for containers with no ssh, which are Linux, and shipping
// a second platform's console API to serve a case nobody has asked for is how a
// small binary stops being small.
//
// The refusal travels back to the panel as a message rather than a silence, so
// an operator who clicks the terminal button on a Windows machine reads why
// instead of watching an empty pane.
func startPTY(cols, rows uint16) (ptySession, error) {
	return nil, errors.New("这个平台暂不支持网页终端（目前只支持 Linux 和其他 Unix 系统）")
}

// isPTYClosed is unreachable here, since startPTY never returns a session. It
// exists so the platform-independent session code compiles on every target.
func isPTYClosed(err error) bool {
	return errors.Is(err, os.ErrClosed)
}
