package mcpserver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxWaypostSendBodyFileBytes int64 = 10 << 20

func readWaypostSendBodyFile(bodyFile, defaultWorkdir string) ([]byte, string, error) {
	defaultWorkdir = strings.TrimSpace(defaultWorkdir)
	if defaultWorkdir == "" {
		return nil, "", errors.New("waypost_send body_file requires a bound default_workdir")
	}
	if !filepath.IsAbs(defaultWorkdir) {
		return nil, "", fmt.Errorf("waypost_send default_workdir %q must be absolute", defaultWorkdir)
	}

	absoluteWorkdir, err := filepath.Abs(defaultWorkdir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve waypost_send default_workdir %q: %w", defaultWorkdir, err)
	}
	resolvedWorkdir, err := canonicalizeExistingPath(absoluteWorkdir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve waypost_send default_workdir %q: %w", defaultWorkdir, err)
	}

	requestedPath := bodyFile
	if !filepath.IsAbs(requestedPath) {
		requestedPath = filepath.Join(absoluteWorkdir, requestedPath)
	}
	requestedPath, err = filepath.Abs(requestedPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve waypost_send body_file %q: %w", bodyFile, err)
	}
	if runtime.GOOS == "windows" && windowsPathHasAlternateDataStream(requestedPath) {
		return nil, "", fmt.Errorf("waypost_send body_file %q uses a Windows alternate data stream", bodyFile)
	}
	inside, err := pathWithinDirectory(absoluteWorkdir, requestedPath)
	if err != nil {
		return nil, "", fmt.Errorf("compare waypost_send body_file %q with default_workdir %q: %w", bodyFile, defaultWorkdir, err)
	}
	if !inside {
		return nil, "", fmt.Errorf("waypost_send body_file %q is outside bound default_workdir %q", bodyFile, defaultWorkdir)
	}

	resolvedBodyFile, err := filepath.EvalSymlinks(requestedPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve waypost_send body_file %q: %w", bodyFile, err)
	}
	inside, err = pathWithinDirectory(resolvedWorkdir, resolvedBodyFile)
	if err != nil {
		return nil, "", fmt.Errorf("compare resolved waypost_send body_file %q with default_workdir %q: %w", bodyFile, defaultWorkdir, err)
	}
	if !inside {
		return nil, "", fmt.Errorf("waypost_send body_file %q resolves outside bound default_workdir %q", bodyFile, defaultWorkdir)
	}

	file, err := os.Open(resolvedBodyFile)
	if err != nil {
		return nil, "", fmt.Errorf("open waypost_send body_file %q: %w", bodyFile, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("inspect waypost_send body_file %q: %w", bodyFile, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("waypost_send body_file %q is not a regular file", bodyFile)
	}
	if info.Size() > maxWaypostSendBodyFileBytes {
		return nil, "", fmt.Errorf("waypost_send body_file %q exceeds the %d-byte limit", bodyFile, maxWaypostSendBodyFileBytes)
	}

	latestResolvedBodyFile, err := filepath.EvalSymlinks(requestedPath)
	if err != nil {
		return nil, "", fmt.Errorf("recheck waypost_send body_file %q: %w", bodyFile, err)
	}
	inside, err = pathWithinDirectory(resolvedWorkdir, latestResolvedBodyFile)
	if err != nil || !inside {
		return nil, "", fmt.Errorf("waypost_send body_file %q changed outside bound default_workdir while opening", bodyFile)
	}
	latestInfo, err := os.Stat(latestResolvedBodyFile)
	if err != nil || !os.SameFile(info, latestInfo) {
		return nil, "", fmt.Errorf("waypost_send body_file %q changed while opening", bodyFile)
	}

	body, err := io.ReadAll(io.LimitReader(file, maxWaypostSendBodyFileBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read waypost_send body_file %q: %w", bodyFile, err)
	}
	if int64(len(body)) > maxWaypostSendBodyFileBytes {
		return nil, "", fmt.Errorf("waypost_send body_file %q exceeds the %d-byte limit", bodyFile, maxWaypostSendBodyFileBytes)
	}
	return body, resolvedBodyFile, nil
}

func pathWithinDirectory(directory, path string) (bool, error) {
	if !strings.EqualFold(filepath.VolumeName(directory), filepath.VolumeName(path)) {
		return false, nil
	}
	relativePath, err := filepath.Rel(directory, path)
	if err != nil {
		return false, err
	}
	return filepath.IsLocal(relativePath), nil
}

func windowsPathHasAlternateDataStream(path string) bool {
	pathWithoutVolume := strings.TrimPrefix(path, filepath.VolumeName(path))
	return strings.Contains(pathWithoutVolume, ":")
}
