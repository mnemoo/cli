package upload

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type SafetyWarning struct {
	Level   string // "error" or "warning"
	Message string
}

var blacklistedNames = []string{
	".env", ".env.local", ".env.production",
	"credentials.json", "serviceAccountKey.json",
}

var blacklistedExtensions = []string{
	".pem", ".key", ".p12", ".pfx", ".jks",
}

func ValidatePath(dirPath string) []SafetyWarning {
	var warnings []SafetyWarning

	abs, err := filepath.Abs(dirPath)
	if err != nil {
		return []SafetyWarning{{Level: "error", Message: fmt.Sprintf("Cannot resolve path: %v", err)}}
	}
	abs = filepath.Clean(abs)

	info, err := os.Stat(abs)
	if err != nil {
		return []SafetyWarning{{Level: "error", Message: fmt.Sprintf("Path does not exist: %s", abs)}}
	}
	if !info.IsDir() {
		return []SafetyWarning{{Level: "error", Message: fmt.Sprintf("Path is not a directory: %s", abs)}}
	}

	if isForbiddenDir(abs) {
		warnings = append(warnings, SafetyWarning{
			Level:   "error",
			Message: fmt.Sprintf("Uploading from system/root directory is forbidden: %s", abs),
		})
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		warnings = append(warnings, SafetyWarning{
			Level:   "error",
			Message: fmt.Sprintf("Cannot read directory: %v", err),
		})
		return warnings
	}

	hasFiles := false
	for _, e := range entries {
		if !e.IsDir() && !skipFiles[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			hasFiles = true
			break
		}
	}
	if !hasFiles {
		hasSubdirs := false
		for _, e := range entries {
			if e.IsDir() && !skipDirs[e.Name()] {
				hasSubdirs = true
				break
			}
		}
		if !hasSubdirs {
			warnings = append(warnings, SafetyWarning{
				Level:   "error",
				Message: "Directory appears empty (no uploadable files found)",
			})
		}
	}

	var foundSuspicious []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && suspiciousDirs[name] {
			foundSuspicious = append(foundSuspicious, name+"/")
		}
		if !e.IsDir() {
			for _, b := range blacklistedNames {
				if name == b {
					foundSuspicious = append(foundSuspicious, name)
					break
				}
			}
			ext := strings.ToLower(filepath.Ext(name))
			for _, b := range blacklistedExtensions {
				if ext == b {
					foundSuspicious = append(foundSuspicious, name)
					break
				}
			}
		}
	}
	if len(foundSuspicious) > 0 {
		warnings = append(warnings, SafetyWarning{
			Level:   "warning",
			Message: fmt.Sprintf("Directory contains: %s -- these will be skipped, but verify this is the right folder", strings.Join(foundSuspicious, ", ")),
		})
	}

	return warnings
}

func HasErrors(warnings []SafetyWarning) bool {
	for _, w := range warnings {
		if w.Level == "error" {
			return true
		}
	}
	return false
}

func isForbiddenDir(abs string) bool {
	forbidden := []string{"/", "/usr", "/etc", "/var", "/tmp", "/bin", "/sbin", "/lib", "/opt"}

	if runtime.GOOS == "darwin" {
		forbidden = append(forbidden, "/System", "/Applications", "/Library", "/Volumes")
	}

	home, err := os.UserHomeDir()
	if err == nil {
		forbidden = append(forbidden, home)
	}

	for _, f := range forbidden {
		if abs == f {
			return true
		}
	}
	return false
}
