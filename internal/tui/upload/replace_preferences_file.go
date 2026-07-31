//go:build !windows

package upload

import "os"

func replacePreferencesFile(source, target string) error {
	return os.Rename(source, target)
}
