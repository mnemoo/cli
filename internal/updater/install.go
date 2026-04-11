package updater

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const downloadTimeout = 5 * time.Minute

// Installer downloads and installs a release for the current platform.
// Progress is optional; when set it receives one line per phase.
type Installer struct {
	Progress func(msg string)
}

// Install fetches the matching asset for this runtime, verifies its sha256
// against the published checksums file, extracts the binary, and replaces
// the current executable. Caller is responsible for confirming with the
// user before invoking this.
func (i *Installer) Install(ctx context.Context, rel *Release) error {
	if rel == nil {
		return errors.New("nil release")
	}

	assetName := platformAssetName(rel.TagName)
	if assetName == "" {
		return fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	asset := findAsset(rel.Assets, assetName)
	if asset == nil {
		return fmt.Errorf("no asset %q in release %s", assetName, rel.TagName)
	}

	// Download checksums file (best effort — we abort if it exists but mismatches).
	expectedSum, haveSum := fetchChecksum(ctx, rel.Assets, assetName)

	i.progress(fmt.Sprintf("Downloading %s (%s)", asset.Name, humanSize(asset.Size)))
	archiveData, err := download(ctx, asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}

	if haveSum {
		actual := sha256.Sum256(archiveData)
		if !strings.EqualFold(hex.EncodeToString(actual[:]), expectedSum) {
			return fmt.Errorf("checksum mismatch for %s", asset.Name)
		}
		i.progress("Checksum verified (sha256).")
	} else {
		i.progress("WARNING: no checksum file in release — proceeding without verification.")
	}

	i.progress("Extracting binary...")
	binaryData, err := extractBinary(archiveData, asset.Name)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(currentExe); err == nil {
		currentExe = resolved
	}

	i.progress(fmt.Sprintf("Installing to %s", currentExe))
	if err := replaceExecutable(currentExe, binaryData); err != nil {
		return err
	}
	return nil
}

// CleanupOldBinary removes the stakecli.exe.old file left by a previous
// Windows update. No-op on other platforms.
func CleanupOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = os.Remove(exe + ".old")
}

func (i *Installer) progress(msg string) {
	if i.Progress != nil {
		i.Progress(msg)
	}
}

// platformAssetName derives the goreleaser archive name for this runtime.
// Mirrors the name_template in .goreleaser.yaml:
//
//	stakecli_{Version}_{Os}_{Arch}.{ext}
func platformAssetName(tag string) string {
	ver := strings.TrimPrefix(tag, "v")

	// darwin amd64 is explicitly ignored in .goreleaser.yaml.
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		return ""
	}

	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("stakecli_%s_%s_%s.%s", ver, runtime.GOOS, runtime.GOARCH, ext)
}

func findAsset(assets []Asset, name string) *Asset {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

func findChecksumAsset(assets []Asset) *Asset {
	for i := range assets {
		if strings.HasSuffix(assets[i].Name, "_checksums.txt") {
			return &assets[i]
		}
	}
	return nil
}

func fetchChecksum(ctx context.Context, assets []Asset, assetName string) (sum string, ok bool) {
	sumAsset := findChecksumAsset(assets)
	if sumAsset == nil {
		return "", false
	}
	data, err := download(ctx, sumAsset.BrowserDownloadURL)
	if err != nil {
		return "", false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		// Line format: "<sha256>  <filename>"
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == assetName {
			return fields[0], true
		}
	}
	return "", false
}

func download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s for %s", resp.Status, url)
	}
	return io.ReadAll(resp.Body)
}

func extractBinary(archiveData []byte, assetName string) ([]byte, error) {
	switch {
	case strings.HasSuffix(assetName, ".zip"):
		return extractFromZip(archiveData, "stakecli.exe")
	case strings.HasSuffix(assetName, ".tar.gz"):
		return extractFromTarGz(archiveData, "stakecli")
	default:
		return nil, fmt.Errorf("unknown archive format: %s", assetName)
	}
}

func extractFromTarGz(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%q not found in archive", name)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			// Cap to 200 MiB to guard against unexpected archive bombs.
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

func extractFromZip(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(io.LimitReader(rc, 200<<20))
	}
	return nil, fmt.Errorf("%q not found in archive", name)
}

// replaceExecutable writes newData over path atomically. On Windows, it
// first renames the running binary to path + ".old" (which Windows permits
// while still allowing execution), then moves the new binary into place;
// the .old file is cleaned up by CleanupOldBinary on the next startup.
func replaceExecutable(path string, newData []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".stakecli-update-*")
	if err != nil {
		return fmt.Errorf("cannot stage update next to %s: %w (is the binary installed by a package manager? update via your package manager instead)", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(newData); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return err
	}

	if runtime.GOOS == "windows" {
		oldPath := path + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(path, oldPath); err != nil {
			cleanup()
			return fmt.Errorf("rename current binary: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			// Best-effort rollback.
			_ = os.Rename(oldPath, path)
			return fmt.Errorf("move new binary into place: %w", err)
		}
		return nil
	}

	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
