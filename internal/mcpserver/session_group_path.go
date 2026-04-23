package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
)

var groupSegmentSanitizePattern = regexp.MustCompile(`[^a-z0-9._@-]+`)

func sanitizeGroupSegment(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = groupSegmentSanitizePattern.ReplaceAllString(normalized, "-")
	return strings.Trim(normalized, "-")
}

func buildChildGroupPath(parentGroup, childGroupName string) (string, error) {
	parent := strings.TrimRight(strings.TrimSpace(parentGroup), "/")
	child := sanitizeGroupSegment(childGroupName)
	if parent == "" {
		return "", fmt.Errorf("cannot derive child group path without a parent group")
	}
	if child == "" {
		return "", fmt.Errorf("child group name resolves to an empty segment")
	}
	return parent + "/" + child, nil
}

func deriveGroupPathFromParentGroup(parentGroup, childGroupName string) (string, error) {
	if strings.TrimSpace(parentGroup) == "" {
		groupPath := sanitizeGroupSegment(childGroupName)
		if groupPath == "" {
			return "", fmt.Errorf("child group name resolves to an empty segment")
		}
		return groupPath, nil
	}
	return buildChildGroupPath(parentGroup, childGroupName)
}

type createSessionLaunchInput struct {
	EnsureTitle     string
	EnsureCmd       string
	Workdir         string
	ParentSessionID string
	NoParentLink    bool
	ListenerMessage string
	GroupPath       string
}

func buildCreateSessionLaunchArgs(input createSessionLaunchInput) []string {
	launchArgs := []string{
		"agent-deck", "launch", "--json",
		"--title", input.EnsureTitle,
		"--cmd", input.EnsureCmd,
	}
	if strings.TrimSpace(input.GroupPath) != "" {
		launchArgs = append(launchArgs, "--group", input.GroupPath)
	}
	if input.NoParentLink {
		launchArgs = append(launchArgs, "--no-parent")
	} else if strings.TrimSpace(input.ParentSessionID) != "" {
		launchArgs = append(launchArgs, "--parent", input.ParentSessionID)
	}
	if strings.TrimSpace(input.ListenerMessage) != "" {
		launchArgs = append(launchArgs, "--message", input.ListenerMessage)
	}
	return append(launchArgs, input.Workdir)
}
