//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package conversations

import "os"

func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
