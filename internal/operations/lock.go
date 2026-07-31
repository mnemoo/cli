package operations

import (
	"fmt"
	"os"
	"path/filepath"
)

func acquireRecordLock(dir, id string) (*os.File, error) {
	path := filepath.Join(dir, "."+id+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening operation lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("locking operation: %w", err)
	}
	return file, nil
}

func releaseRecordLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unlockFile(file)
	_ = file.Close()
}
