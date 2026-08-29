package codexhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const mcpProbeTimeout = 4 * time.Second

type waypostMCPAvailability uint8

const (
	waypostMCPUnknown waypostMCPAvailability = iota
	waypostMCPUnavailable
	waypostMCPAvailable
)

type waypostMCPProbe func(context.Context) (bool, error)

func detectWaypostMCP(ctx context.Context, probe waypostMCPProbe) (waypostMCPAvailability, error) {
	available, err := probe(ctx)
	if err != nil {
		return waypostMCPUnknown, err
	}
	if available {
		return waypostMCPAvailable, nil
	}
	return waypostMCPUnavailable, nil
}

// CurrentDirectoryWaypostMCPAvailable reports whether Waypost is enabled in
// the effective configuration visible to a new Codex process started in the
// caller's current directory. Trusted project configuration may contribute to
// the result. An already-running session's profile or command-line overrides
// may differ because Codex does not expose its live MCP inventory to command
// hooks.
func CurrentDirectoryWaypostMCPAvailable(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, mcpProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "codex", "mcp", "list", "--json").Output()
	if err != nil {
		if probeErr := probeCtx.Err(); probeErr != nil {
			return false, fmt.Errorf("run `codex mcp list --json`: %w", probeErr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if detail := boundedProbeErrorDetail(exitErr.Stderr); detail != "" {
				return false, fmt.Errorf("run `codex mcp list --json`: %w: %s", err, detail)
			}
		}
		return false, fmt.Errorf("run `codex mcp list --json`: %w", err)
	}
	return parseWaypostMCPAvailable(output)
}

func boundedProbeErrorDetail(stderr []byte) string {
	detail := strings.TrimSpace(string(stderr))
	const maxRunes = 500
	runes := []rune(detail)
	if len(runes) > maxRunes {
		detail = string(runes[:maxRunes]) + "…"
	}
	return detail
}

func parseWaypostMCPAvailable(output []byte) (bool, error) {
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(output, &servers); err != nil {
		return false, fmt.Errorf("parse `codex mcp list --json`: %w", err)
	}
	for _, server := range servers {
		if server.Name == "waypost" {
			return server.Enabled, nil
		}
	}
	return false, nil
}
