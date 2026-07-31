//go:build !windows

package conversations

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
