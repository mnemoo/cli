package upload

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".svn":         true,
	".hg":          true,
}

var suspiciousDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".svn":         true,
	".hg":          true,
	".idea":        true,
	".vscode":      true,
}

var skipFiles = map[string]bool{
	".DS_Store": true,
	"Thumbs.db": true,
}

type LocalFile struct {
	RelPath    string // relative to scanned root, e.g. "math/index.json"
	AbsPath    string
	Size       int64
	ETagResult *ETagResult
}

// ScanDirectory walks the directory tree and collects files with their ETags.
// uploadType is "math" or "front" -- used as the prefix for remote paths.
func ScanDirectory(rootPath, uploadType string) ([]LocalFile, error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolving path: %w", err)
	}

	var files []LocalFile

	err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()

		if d.IsDir() {
			if skipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		if skipFiles[name] || strings.HasPrefix(name, ".") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		if info.Size() == 0 {
			return nil
		}

		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			return fmt.Errorf("relative path: %w", err)
		}

		remotePath := "/" + uploadType + "/" + filepath.ToSlash(rel)

		etag, err := ComputeETag(path, info.Size())
		if err != nil {
			return fmt.Errorf("computing etag for %s: %w", rel, err)
		}

		files = append(files, LocalFile{
			RelPath:    remotePath,
			AbsPath:    path,
			Size:       info.Size(),
			ETagResult: etag,
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("scanning directory: %w", err)
	}

	return files, nil
}
