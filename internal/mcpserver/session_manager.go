package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	activeSessionStatuses = map[string]bool{
		"running": true,
		"waiting": true,
		"idle":    true,
	}
	codexResumePattern      = regexp.MustCompile(`\bresume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)
	codexSessionFilePattern = regexp.MustCompile(`/\.codex/sessions/.*-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)
	codexCommandPattern     = regexp.MustCompile(`(^|/)codex(\s|$)`)
)

type serverState struct {
	mu                       sync.Mutex
	boundAddresses           []string
	defaultSender            string
	defaultWorkdir           string
	autoBindAttempted        bool
	detectedAgentDeckSession string
	detectedAgentSession     string
}

type stateSnapshot struct {
	BoundAddresses           []string
	DefaultSender            string
	DefaultWorkdir           string
	AutoBindAttempted        bool
	DetectedAgentDeckSession string
	DetectedAgentSession     string
}

type boundState struct {
	BoundAddresses           []string `json:"bound_addresses"`
	DefaultSender            string   `json:"default_sender"`
	DefaultWorkdir           string   `json:"default_workdir"`
	DetectedAgentDeckSession string   `json:"detected_agent_deck_session_id"`
	DetectedAgentSession     string   `json:"detected_agent_session_id"`
	Warnings                 []string `json:"warnings"`
}

type sessionData struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Group           string `json:"group"`
	Path            string `json:"path"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Success         *bool  `json:"success,omitempty"`
}

type psRow struct {
	PID  int
	PPID int
	Comm string
	Args string
}

type sessionManager struct {
	runner Runner
	state  *serverState
}

type sessionShowProbeStatus string

const (
	sessionShowProbeFound    sessionShowProbeStatus = "found"
	sessionShowProbeNotFound sessionShowProbeStatus = "not_found"
	sessionShowProbeUnknown  sessionShowProbeStatus = "unknown"
)

type sessionShowProbeResult struct {
	Status sessionShowProbeStatus
	Data   *sessionData
}

func newSessionManager(runner Runner, state *serverState) *sessionManager {
	return &sessionManager{
		runner: runner,
		state:  state,
	}
}

func (m *sessionManager) bind(ctx context.Context, input mailboxBindInput) (boundState, error) {
	boundAddresses := dedupe(input.Addresses)
	defaultSender := strings.TrimSpace(input.DefaultSender)
	if defaultSender == "" && len(boundAddresses) > 0 {
		defaultSender = boundAddresses[0]
	}

	m.state.mu.Lock()
	m.state.boundAddresses = boundAddresses
	m.state.defaultSender = defaultSender
	m.state.defaultWorkdir = strings.TrimSpace(input.DefaultWorkdir)
	m.state.autoBindAttempted = true
	m.state.mu.Unlock()

	return m.boundState(ctx)
}

func (m *sessionManager) boundState(ctx context.Context) (boundState, error) {
	if err := m.tryAutoBindCurrentSession(ctx); err != nil {
		return boundState{}, err
	}
	snapshot := m.snapshotState()

	warnings := make([]string, 0, 3)
	if snapshot.DetectedAgentDeckSession == "" {
		warnings = append(warnings, "unable to determine current agent-deck session id")
	}
	if snapshot.DetectedAgentSession == "" {
		warnings = append(warnings, "unable to determine current AI agent session id")
	}
	if len(snapshot.BoundAddresses) == 0 {
		warnings = append(warnings, "no mailbox addresses are currently bound")
	}

	return boundState{
		BoundAddresses:           snapshot.BoundAddresses,
		DefaultSender:            snapshot.DefaultSender,
		DefaultWorkdir:           snapshot.DefaultWorkdir,
		DetectedAgentDeckSession: snapshot.DetectedAgentDeckSession,
		DetectedAgentSession:     snapshot.DetectedAgentSession,
		Warnings:                 warnings,
	}, nil
}

func (m *sessionManager) snapshotState() stateSnapshot {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return stateSnapshot{
		BoundAddresses:           append([]string(nil), m.state.boundAddresses...),
		DefaultSender:            m.state.defaultSender,
		DefaultWorkdir:           m.state.defaultWorkdir,
		AutoBindAttempted:        m.state.autoBindAttempted,
		DetectedAgentDeckSession: m.state.detectedAgentDeckSession,
		DetectedAgentSession:     m.state.detectedAgentSession,
	}
}

func (m *sessionManager) tryAutoBindCurrentSession(ctx context.Context) error {
	snapshot := m.snapshotState()
	if len(snapshot.BoundAddresses) > 0 || snapshot.AutoBindAttempted {
		return nil
	}

	envAgentDeckID := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
	agentDeckSessionID := envAgentDeckID
	probeCompleted := envAgentDeckID != ""

	if agentDeckSessionID == "" {
		result, err := runProbe(ctx, m.runner, []string{"agent-deck", "session", "current", "--json"}, runOptions{timeout: syncCmdTimeout}, false)
		if err != nil {
			return err
		}
		if result != nil {
			probeCompleted = true
			if result.ExitCode == 0 {
				var current struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(result.Stdout), &current); err != nil {
					return fmt.Errorf("agent-deck session current returned invalid JSON: %w", err)
				}
				agentDeckSessionID = strings.TrimSpace(current.ID)
			}
		}
	}

	addresses := make([]string, 0, 2)
	detectedAgentDeckSession := ""
	defaultWorkdir := snapshot.DefaultWorkdir
	if agentDeckSessionID != "" {
		detectedAgentDeckSession = agentDeckSessionID
		addresses = append(addresses, agentDeckAddress(agentDeckSessionID))
		data, err := m.resolveSessionShowBestEffort(ctx, agentDeckSessionID)
		if err != nil {
			return err
		}
		if data != nil && strings.TrimSpace(data.Path) != "" {
			defaultWorkdir = strings.TrimSpace(data.Path)
		}
	}

	codexSessionID, err := m.detectCurrentCodexSessionID(ctx)
	if err != nil {
		return err
	}
	detectedAgentSession := ""
	if codexSessionID != "" {
		detectedAgentSession = codexSessionID
		addresses = append(addresses, codexAddress(codexSessionID))
	}

	if !probeCompleted && codexSessionID == "" && len(addresses) == 0 {
		return nil
	}

	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if len(m.state.boundAddresses) > 0 {
		return nil
	}
	m.state.boundAddresses = dedupe(addresses)
	m.state.detectedAgentDeckSession = detectedAgentDeckSession
	m.state.detectedAgentSession = detectedAgentSession
	m.state.defaultWorkdir = defaultWorkdir
	switch {
	case detectedAgentDeckSession != "":
		m.state.defaultSender = agentDeckAddress(detectedAgentDeckSession)
	case detectedAgentSession != "":
		m.state.defaultSender = codexAddress(detectedAgentSession)
	}
	m.state.autoBindAttempted = true
	return nil
}

func (m *sessionManager) detectCurrentCodexSessionID(ctx context.Context) (string, error) {
	if sessionID := strings.TrimSpace(os.Getenv("CODEX_SESSION_ID")); sessionID != "" {
		return sessionID, nil
	}

	seen := map[int]bool{}
	pid := os.Getppid()
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		row, err := m.getProcessRow(ctx, pid)
		if err != nil {
			return "", err
		}
		if row == nil {
			break
		}
		looksLikeCodex := row.Comm == "codex" || codexCommandPattern.MatchString(row.Args) || strings.Contains(row.Args, "@openai/codex")
		if looksLikeCodex {
			if fromArgs := extractCodexSessionIDFromArgs(row.Args); fromArgs != "" {
				return fromArgs, nil
			}
			if fromLsof, err := m.extractCodexSessionIDFromLsof(ctx, row.PID); err != nil {
				return "", err
			} else if fromLsof != "" {
				return fromLsof, nil
			}
			return "", nil
		}
		pid = row.PPID
	}
	return "", nil
}

func (m *sessionManager) getProcessRow(ctx context.Context, pid int) (*psRow, error) {
	if pid <= 1 {
		return nil, nil
	}
	result, err := runProbe(ctx, m.runner, []string{"ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,comm=,args="}, runOptions{timeout: syncCmdTimeout}, false)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, nil
	}
	return parsePSRow(result.Stdout), nil
}

func (m *sessionManager) extractCodexSessionIDFromLsof(ctx context.Context, pid int) (string, error) {
	if pid <= 1 {
		return "", nil
	}
	result, err := runProbe(ctx, m.runner, []string{"lsof", "-p", strconv.Itoa(pid)}, runOptions{timeout: syncCmdTimeout}, false)
	if err != nil {
		return "", err
	}
	if result == nil || result.ExitCode != 0 {
		return "", nil
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		match := codexSessionFilePattern.FindStringSubmatch(line)
		if len(match) == 2 {
			return match[1], nil
		}
	}
	return "", nil
}

func (m *sessionManager) mailboxAddresses(ctx context.Context, addresses []string) ([]string, error) {
	if len(addresses) > 0 {
		return dedupe(addresses), nil
	}
	bound, err := m.boundState(ctx)
	if err != nil {
		return nil, err
	}
	if len(bound.BoundAddresses) == 0 {
		return nil, errors.New("no mailbox addresses provided and no mailbox addresses are bound")
	}
	return append([]string(nil), bound.BoundAddresses...), nil
}

func (m *sessionManager) senderAddress(ctx context.Context, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override), nil
	}
	bound, err := m.boundState(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case bound.DefaultSender != "":
		return bound.DefaultSender, nil
	case len(bound.BoundAddresses) > 0:
		return bound.BoundAddresses[0], nil
	default:
		return "", errors.New("mailbox_send requires from_address or a bound default_sender")
	}
}

func (m *sessionManager) isLocalAddress(ctx context.Context, address string) bool {
	bound, err := m.boundState(ctx)
	if err != nil {
		return false
	}
	for _, candidate := range bound.BoundAddresses {
		if candidate == address {
			return true
		}
	}
	return false
}

func (m *sessionManager) resolveSessionShow(ctx context.Context, identifier string, timeout time.Duration) (*sessionData, error) {
	result, err := runProbe(ctx, m.runner, []string{"agent-deck", "session", "show", identifier, "--json"}, runOptions{timeout: timeout}, true)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, nil
	}
	data, err := parseSessionData(result.Stdout, "agent-deck session show")
	if err != nil {
		return nil, err
	}
	if data.Success != nil && !*data.Success {
		return nil, nil
	}
	return data, nil
}

func (m *sessionManager) resolveSessionShowBestEffort(ctx context.Context, identifier string) (*sessionData, error) {
	probe, err := m.probeSessionShowBestEffort(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if probe.Status != sessionShowProbeFound {
		return nil, nil
	}
	return probe.Data, nil
}

func (m *sessionManager) probeSessionShowBestEffort(ctx context.Context, identifier string) (sessionShowProbeResult, error) {
	result, err := runProbe(ctx, m.runner, []string{"agent-deck", "session", "show", identifier, "--json"}, runOptions{timeout: syncCmdTimeout}, false)
	if err != nil {
		return sessionShowProbeResult{}, err
	}
	if result == nil {
		return sessionShowProbeResult{Status: sessionShowProbeUnknown}, nil
	}
	if result.ExitCode != 0 {
		detail := strings.ToLower(strings.TrimSpace(result.Stderr + "\n" + result.Stdout))
		if strings.Contains(detail, "not found") {
			return sessionShowProbeResult{Status: sessionShowProbeNotFound}, nil
		}
		return sessionShowProbeResult{Status: sessionShowProbeUnknown}, nil
	}
	data, err := parseSessionData(result.Stdout, "agent-deck session show")
	if err != nil {
		return sessionShowProbeResult{}, err
	}
	if data.Success != nil && !*data.Success {
		return sessionShowProbeResult{Status: sessionShowProbeNotFound}, nil
	}
	return sessionShowProbeResult{Status: sessionShowProbeFound, Data: data}, nil
}

func (m *sessionManager) ensureSession(ctx context.Context, input agentDeckEnsureSessionInput) (map[string]any, error) {
	identifier := firstNonEmpty(input.SessionID, input.SessionRef)
	workdir := strings.TrimSpace(input.Workdir)
	if workdir == "" {
		return nil, errors.New("workdir is required when ensuring or creating a target session")
	}
	canonicalWorkdir, err := canonicalizeExistingPath(workdir)
	if err != nil {
		return nil, fmt.Errorf("workdir does not exist: %s", workdir)
	}
	workdir = canonicalWorkdir

	var data *sessionData
	if identifier != "" {
		data, err = m.resolveSessionShow(ctx, identifier, ensureSessionShowTimeout)
		if err != nil {
			return nil, err
		}
	}

	createdTarget := false
	startedSession := false
	notifyNeeded := false
	listenerStatus := "not_needed"
	noParentLink := input.NoParentLink
	targetGroupPath := strings.TrimSpace(input.GroupPath)

	if noParentLink && strings.TrimSpace(input.ParentSessionID) != "" {
		return nil, errors.New("no_parent_link cannot be combined with parent_session_id")
	}
	if targetGroupPath == "" && strings.TrimSpace(input.GroupParentSessionID) != "" {
		parentData, err := m.resolveSessionShow(ctx, input.GroupParentSessionID, ensureSessionShowTimeout)
		if err != nil {
			return nil, err
		}
		if parentData == nil {
			return nil, fmt.Errorf("group_parent_session_id not found: %s", input.GroupParentSessionID)
		}
		parentGroup := strings.TrimSpace(parentData.Group)
		childGroupName := firstNonEmpty(input.ChildGroupName, input.EnsureTitle, input.SessionRef, input.SessionID)
		if parentGroup == "" {
			targetGroupPath = sanitizeGroupSegment(childGroupName)
			if targetGroupPath == "" {
				return nil, errors.New("child group name resolves to an empty segment")
			}
		} else {
			targetGroupPath, err = buildChildGroupPath(parentGroup, childGroupName)
			if err != nil {
				return nil, err
			}
		}
	}
	if data == nil {
		if input.EnsureTitle == "" {
			return nil, errors.New("target session missing: provide session_id, session_ref, or ensure_title")
		}
		if input.EnsureCmd == "" {
			return nil, errors.New("ensure_cmd is required when creating a target session")
		}
		if strings.TrimSpace(input.ParentSessionID) == "" && targetGroupPath == "" && !noParentLink {
			return nil, errors.New("creating a target session requires either group_path/group_parent_session_id or parent_session_id")
		}
		if strings.TrimSpace(input.ParentSessionID) != "" {
			parentData, err := m.resolveSessionShow(ctx, input.ParentSessionID, ensureSessionShowTimeout)
			if err != nil {
				return nil, err
			}
			if parentData == nil {
				return nil, fmt.Errorf("parent_session_id not found: %s", input.ParentSessionID)
			}
			if targetGroupPath == "" && strings.TrimSpace(parentData.ParentSessionID) != "" {
				parentGroup := strings.TrimSpace(parentData.Group)
				if parentGroup != "" {
					childGroupName := firstNonEmpty(parentData.Title, input.ParentSessionID, parentData.ID)
					targetGroupPath, err = buildChildGroupPath(parentGroup, childGroupName)
					if err != nil {
						return nil, err
					}
				}
			}
		}
		if targetGroupPath != "" {
			if err := m.ensureGroupPath(ctx, targetGroupPath); err != nil {
				return nil, err
			}
		}

		listenerMessage := strings.TrimSpace(input.ListenerMessage)
		launchArgs := buildEnsureSessionLaunchArgs(ensureSessionLaunchInput{
			EnsureTitle:     input.EnsureTitle,
			EnsureCmd:       input.EnsureCmd,
			Workdir:         workdir,
			ParentSessionID: input.ParentSessionID,
			NoParentLink:    noParentLink,
			ListenerMessage: listenerMessage,
			GroupPath:       targetGroupPath,
		})
		launchResult, err := runCommand(ctx, m.runner, launchArgs, runOptions{})
		if err != nil {
			return nil, err
		}
		data, err = parseSessionData(launchResult.Stdout, "agent-deck launch")
		if err != nil {
			return nil, err
		}
		createdTarget = true
		startedSession = true
		listenerStatus = "started_waiting"
	} else {
		listenerMessage := strings.TrimSpace(input.ListenerMessage)
		existingPath := strings.TrimSpace(data.Path)
		if existingPath == "" {
			return nil, errors.New("existing session path unavailable: cannot verify workdir match")
		}
		canonicalExistingPath, err := canonicalizeExistingPath(existingPath)
		if err != nil {
			return nil, fmt.Errorf("canonicalize existing session path %q: %w", existingPath, err)
		}
		if canonicalExistingPath != workdir {
			return nil, fmt.Errorf("session path mismatch: existing='%s' expected='%s'", data.Path, input.Workdir)
		}
		existingGroup := strings.TrimSpace(data.Group)
		if targetGroupPath != "" && existingGroup != targetGroupPath {
			if err := m.ensureGroupPath(ctx, targetGroupPath); err != nil {
				return nil, err
			}
			if _, err := runCommand(ctx, m.runner, []string{"agent-deck", "group", "move", data.ID, targetGroupPath}, runOptions{}); err != nil {
				return nil, err
			}
			refreshed, err := m.resolveSessionShow(ctx, data.ID, ensureSessionShowTimeout)
			if err != nil {
				return nil, err
			}
			if refreshed != nil {
				data = refreshed
			}
		}
		if activeSessionStatuses[strings.TrimSpace(data.Status)] {
			notifyNeeded = true
			listenerStatus = "not_needed_existing_session"
		} else {
			startArgs := []string{"agent-deck", "session", "start", "--json"}
			if listenerMessage != "" {
				startArgs = append(startArgs, "-m", listenerMessage)
			}
			startArgs = append(startArgs, data.ID)
			if _, err := runCommand(ctx, m.runner, startArgs, runOptions{}); err != nil {
				return nil, err
			}
			refreshed, err := m.resolveSessionShow(ctx, data.ID, ensureSessionShowTimeout)
			if err != nil {
				return nil, err
			}
			if refreshed != nil {
				data = refreshed
			}
			startedSession = true
			if listenerMessage != "" {
				listenerStatus = "started_waiting"
			} else {
				listenerStatus = "started"
			}
		}
	}

	out := sessionInfoMap(data, firstNonEmpty(input.SessionRef, input.EnsureTitle, identifier))
	out["status"] = "ready"
	out["created_target"] = createdTarget
	out["started_session"] = startedSession
	out["notify_needed"] = notifyNeeded
	out["listener_status"] = listenerStatus
	return out, nil
}

func (m *sessionManager) listGroupPaths(ctx context.Context) (map[string]bool, error) {
	result, err := runCommand(ctx, m.runner, []string{"agent-deck", "group", "list", "--json"}, runOptions{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Groups []struct {
			Path string `json:"path"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return nil, fmt.Errorf("agent-deck group list returned invalid JSON: %w", err)
	}
	paths := map[string]bool{}
	for _, group := range payload.Groups {
		if trimmed := strings.TrimSpace(group.Path); trimmed != "" {
			paths[trimmed] = true
		}
	}
	return paths, nil
}

func (m *sessionManager) ensureGroupPath(ctx context.Context, groupPath string) error {
	trimmed := strings.TrimSpace(groupPath)
	if trimmed == "" {
		return nil
	}
	existing, err := m.listGroupPaths(ctx)
	if err != nil {
		return err
	}
	current := ""
	for _, rawSegment := range strings.Split(trimmed, "/") {
		segment := strings.TrimSpace(rawSegment)
		if segment == "" {
			return fmt.Errorf("invalid group path: %s", groupPath)
		}
		next := segment
		if current != "" {
			next = current + "/" + segment
		}
		if !existing[next] {
			createArgs := []string{"agent-deck", "group", "create", segment}
			if current != "" {
				createArgs = append(createArgs, "--parent", current)
			}
			if _, err := runCommand(ctx, m.runner, createArgs, runOptions{}); err != nil {
				return err
			}
			existing[next] = true
		}
		current = next
	}
	return nil
}

func canonicalizeExistingPath(path string) (string, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return resolvedPath, nil
}

func parseSessionData(text, context string) (*sessionData, error) {
	var data sessionData
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", context, err)
	}
	return &data, nil
}

func sessionInfoMap(data *sessionData, sessionRef string) map[string]any {
	return map[string]any{
		"session_id":     data.ID,
		"session_ref":    firstNonEmpty(sessionRef, data.Title, data.ID),
		"title":          nilIfEmpty(data.Title),
		"session_status": nilIfEmpty(data.Status),
		"group":          nilIfEmpty(data.Group),
		"path":           nilIfEmpty(data.Path),
		"addresses":      []string{agentDeckAddress(data.ID)},
	}
}

func parsePSRow(text string) *psRow {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 3 {
		return nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return nil
	}
	args := ""
	if len(fields) > 3 {
		args = strings.Join(fields[3:], " ")
	}
	return &psRow{
		PID:  pid,
		PPID: ppid,
		Comm: fields[2],
		Args: args,
	}
}

func extractCodexSessionIDFromArgs(args string) string {
	match := codexResumePattern.FindStringSubmatch(args)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
