package launchpath

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StableExecutableEnv lets the Windows launcher tell the versioned child which
// executable path remains valid across upgrades.
const StableExecutableEnv = "WAYPOST_STABLE_EXECUTABLE"

// CurrentExecutable returns the stable launcher path when the process was
// started through one, and otherwise returns the current executable path.
func CurrentExecutable() (string, error) {
	stable := os.Getenv(StableExecutableEnv)
	if strings.TrimSpace(stable) != "" {
		return resolveExecutable(stable, "", runtime.GOOS)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	return resolveExecutable("", executable, runtime.GOOS)
}

func resolveExecutable(stable, executable, goos string) (string, error) {
	if stable = strings.TrimSpace(stable); stable != "" {
		if !isAbsolutePath(stable, goos) {
			return "", fmt.Errorf("%s must contain an absolute path", StableExecutableEnv)
		}
		return cleanPath(stable, goos), nil
	}
	if goos == "windows" && isManagedVersionExecutable(executable) {
		return "", fmt.Errorf("current executable %q is a versioned Waypost child and is not stable across upgrades; stop running Waypost and Codex processes, rerun `make.ps1 install` to replace the launcher, then retry", executable)
	}
	return executable, nil
}

func isManagedVersionExecutable(executable string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(executable, `\`, "/"))
	parts := strings.Split(strings.TrimRight(normalized, "/"), "/")
	return len(parts) >= 5 &&
		parts[len(parts)-5] == "lib" &&
		parts[len(parts)-4] == "waypost" &&
		parts[len(parts)-3] == "versions" &&
		parts[len(parts)-2] != "" &&
		parts[len(parts)-1] != ""
}

func isAbsolutePath(path, goos string) bool {
	if goos == runtime.GOOS {
		return filepath.IsAbs(path)
	}
	if goos != "windows" {
		return filepath.IsAbs(path)
	}
	path = strings.ReplaceAll(path, `/`, `\`)
	return strings.HasPrefix(path, `\\`) ||
		(len(path) >= 3 && path[1] == ':' && path[2] == '\\' && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')))
}

func cleanPath(path, goos string) string {
	if goos == runtime.GOOS {
		return filepath.Clean(path)
	}
	if goos == "windows" {
		return strings.ReplaceAll(path, `/`, `\`)
	}
	return filepath.Clean(path)
}
