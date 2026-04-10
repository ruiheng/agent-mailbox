package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	taskHeaderPattern           = regexp.MustCompile(`(?m)^Task:\s*(.+)$`)
	actionHeaderPattern         = regexp.MustCompile(`(?m)^Action:\s*(.+)$`)
	taskBranchPattern           = regexp.MustCompile(`(?m)^- Task branch:\s*(.+)$`)
	integrationBranchPattern    = regexp.MustCompile(`(?m)^- Integration branch:\s*(.+)$`)
	coderSessionRefPattern      = regexp.MustCompile(`(?m)^- Coder session ref:\s*(.+)$`)
	groupSegmentSanitizePattern = regexp.MustCompile(`[^a-z0-9._@-]+`)
	writeLockFile               = os.WriteFile
)

type workflowEnvelope struct {
	TaskID string
	Action string
}

type delegateLockMetadata struct {
	TaskID            string `json:"task_id,omitempty"`
	Action            string `json:"action,omitempty"`
	PlannerSessionID  string `json:"planner_session_id,omitempty"`
	FromAddress       string `json:"from_address,omitempty"`
	ToAddress         string `json:"to_address,omitempty"`
	Subject           string `json:"subject,omitempty"`
	TaskBranch        string `json:"task_branch,omitempty"`
	IntegrationBranch string `json:"integration_branch,omitempty"`
	CoderSessionRef   string `json:"coder_session_ref,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

type activeTaskLockPaths struct {
	ArtifactRoot string
	LockDir      string
	LockFile     string
}

func matchTrimmed(text string, pattern *regexp.Regexp) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func parseWorkflowEnvelope(body string) workflowEnvelope {
	headerBlock := strings.ReplaceAll(body, "\r\n", "\n")
	if idx := strings.Index(headerBlock, "\n\n"); idx >= 0 {
		headerBlock = headerBlock[:idx]
	}
	return workflowEnvelope{
		TaskID: matchTrimmed(headerBlock, taskHeaderPattern),
		Action: matchTrimmed(headerBlock, actionHeaderPattern),
	}
}

func parseDelegateLockMetadata(body string, defaults delegateLockMetadata) delegateLockMetadata {
	metadata := defaults
	metadata.TaskBranch = matchTrimmed(body, taskBranchPattern)
	metadata.IntegrationBranch = matchTrimmed(body, integrationBranchPattern)
	metadata.CoderSessionRef = matchTrimmed(body, coderSessionRefPattern)
	metadata.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return metadata
}

func validateDelegateLockMetadata(metadata delegateLockMetadata) error {
	if strings.TrimSpace(metadata.IntegrationBranch) == "" {
		return fmt.Errorf("delegate dispatch requires '- Integration branch:' before mailbox_send")
	}
	return nil
}

func activeTaskPaths(workdir string) activeTaskLockPaths {
	artifactRoot := filepath.Join(workdir, ".agent-artifacts")
	lockDir := filepath.Join(artifactRoot, "active-task.lock")
	return activeTaskLockPaths{
		ArtifactRoot: artifactRoot,
		LockDir:      lockDir,
		LockFile:     filepath.Join(lockDir, "lock.json"),
	}
}

func readActiveTaskLock(lockFile string) *delegateLockMetadata {
	raw, err := os.ReadFile(lockFile)
	if err != nil {
		return nil
	}
	var metadata delegateLockMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil
	}
	return &metadata
}

func acquireActiveTaskLock(workdir string, metadata delegateLockMetadata) (string, error) {
	paths := activeTaskPaths(workdir)
	if err := os.MkdirAll(paths.ArtifactRoot, 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(paths.LockDir, 0o755); err != nil {
		if os.IsExist(err) {
			existing := readActiveTaskLock(paths.LockFile)
			existingTaskID := "<unknown>"
			if existing != nil && strings.TrimSpace(existing.TaskID) != "" {
				existingTaskID = strings.TrimSpace(existing.TaskID)
			}
			return "", fmt.Errorf(
				"active task lock exists: %s :: task_id=%s :: delete this directory manually after verifying the prior task is finished",
				paths.LockDir,
				existingTaskID,
			)
		}
		return "", err
	}

	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		_ = os.RemoveAll(paths.LockDir)
		return "", err
	}
	if err := writeLockFile(paths.LockFile, append(encoded, '\n'), 0o644); err != nil {
		_ = os.RemoveAll(paths.LockDir)
		return "", err
	}
	return paths.LockDir, nil
}

func releaseActiveTaskLock(lockDir string) error {
	if strings.TrimSpace(lockDir) == "" {
		return nil
	}
	return os.RemoveAll(lockDir)
}

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

type ensureSessionLaunchInput struct {
	EnsureTitle     string
	EnsureCmd       string
	Workdir         string
	ParentSessionID string
	NoParentLink    bool
	ListenerMessage string
	GroupPath       string
}

func buildEnsureSessionLaunchArgs(input ensureSessionLaunchInput) []string {
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
