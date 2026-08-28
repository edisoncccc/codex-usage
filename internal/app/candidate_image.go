package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/zJay26/codex-usage/internal/platform"
)

type boundCandidateImage struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	info   os.FileInfo
	digest string
}

func bindCurrentCandidateImage(path string) (*boundCandidateImage, error) {
	switch runtime.GOOS {
	case "linux":
		return bindCandidateImage(path, func(string) (*os.File, error) {
			return os.Open("/proc/self/exe")
		})
	case "windows":
		return bindCandidateImage(path, platform.OpenForRenameValidation)
	default:
		return nil, fmt.Errorf("unsupported candidate binding platform %s", runtime.GOOS)
	}
}

func bindCandidateImageAtPath(path string) (*boundCandidateImage, error) {
	return bindCandidateImage(path, platform.OpenForRenameValidation)
}

func bindCandidateImage(
	path string,
	openImage func(string) (*os.File, error),
) (*boundCandidateImage, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("candidate path must be absolute and clean: %q", path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect candidate before binding %s: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("candidate is not a safe regular file: %s", path)
	}
	file, err := openImage(path)
	if err != nil {
		return nil, fmt.Errorf("open candidate image %s: %w", path, err)
	}
	owned := true
	defer func() {
		if owned {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect bound candidate image %s: %w", path, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("candidate path does not identify the running image: %s", path)
	}
	if err := validateBoundCandidatePath(path, opened); err != nil {
		return nil, err
	}
	digest, err := hashBoundCandidateFile(file, path)
	if err != nil {
		return nil, err
	}
	if err := validateBoundCandidatePath(path, opened); err != nil {
		return nil, err
	}
	owned = false
	return &boundCandidateImage{path: path, file: file, info: opened, digest: digest}, nil
}

func validateBoundCandidatePath(path string, opened os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect candidate path %s: %w", path, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("candidate path changed while binding the running image: %s", path)
	}
	return nil
}

func hashBoundCandidateFile(file *os.File, path string) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind candidate image %s: %w", path, err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	_, rewindErr := file.Seek(0, io.SeekStart)
	if copyErr != nil {
		return "", errors.Join(fmt.Errorf("hash candidate image %s: %w", path, copyErr), rewindErr)
	}
	if rewindErr != nil {
		return "", fmt.Errorf("rewind hashed candidate image %s: %w", path, rewindErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (image *boundCandidateImage) digestForPath(path string) (string, error) {
	if image == nil {
		return "", errors.New("candidate image is not bound")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.file == nil {
		return "", errors.New("candidate image binding is closed")
	}
	if !sameCandidatePath(image.path, path) {
		return "", fmt.Errorf("candidate path %q does not match bound image %q", path, image.path)
	}
	current, err := image.file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect bound candidate image %s: %w", image.path, err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(image.info, current) {
		return "", fmt.Errorf("bound candidate image identity changed: %s", image.path)
	}
	return image.digest, nil
}

func (image *boundCandidateImage) copyVerified(target io.Writer) (string, error) {
	if image == nil {
		return "", errors.New("candidate image is not bound")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.file == nil {
		return "", errors.New("candidate image binding is closed")
	}
	if _, err := image.file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind bound candidate image %s: %w", image.path, err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(target, hash), image.file)
	_, rewindErr := image.file.Seek(0, io.SeekStart)
	if copyErr != nil {
		return "", errors.Join(fmt.Errorf("copy bound candidate image %s: %w", image.path, copyErr), rewindErr)
	}
	if rewindErr != nil {
		return "", fmt.Errorf("rewind copied candidate image %s: %w", image.path, rewindErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != image.digest {
		return "", errors.New("bound candidate image changed after confirmation")
	}
	return digest, nil
}

func (image *boundCandidateImage) Close() error {
	if image == nil {
		return nil
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.file == nil {
		return nil
	}
	err := image.file.Close()
	image.file = nil
	return err
}

func sameCandidatePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
