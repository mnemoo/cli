//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package daemon

import (
	"os"
	"os/exec"
)

func configureDetached(*exec.Cmd) {}

func acquireDaemonLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func releaseDaemonLock(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}
