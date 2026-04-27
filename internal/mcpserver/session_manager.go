package mcpserver

import (
	"context"
	"database/sql"
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

	_ "github.com/mattn/go-sqlite3"
	"github.com/ruiheng/agent-mailbox/internal/mailbox"
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

type agentDeckDBMatch struct {
	SessionID   string
	ProjectPath string
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
	boundAddresses, err := mailbox.NormalizeAddressList(input.Addresses)
	if err != nil {
		return boundState{}, err
	}
	defaultSender := strings.TrimSpace(input.DefaultSender)
	if defaultSender == "" && len(boundAddresses) > 0 {
		defaultSender = boundAddresses[0]
	}
	if defaultSender != "" {
		defaultSender, err = mailbox.NormalizeAddress(defaultSender)
		if err != nil {
			return boundState{}, fmt.Errorf("invalid default_sender: %w", err)
		}
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

	codexSessionID, err := m.detectCurrentCodexSessionID(ctx)
	if err != nil {
		return err
	}

	defaultWorkdir := snapshot.DefaultWorkdir
	if agentDeckSessionID == "" && codexSessionID != "" {
		match, err := lookupAgentDeckSessionByCodexID(ctx, codexSessionID)
		if err != nil {
			return err
		}
		if match != nil {
			agentDeckSessionID = match.SessionID
			if strings.TrimSpace(match.ProjectPath) != "" && defaultWorkdir == "" {
				defaultWorkdir = strings.TrimSpace(match.ProjectPath)
			}
		}
	}

	addresses := make([]string, 0, 2)
	detectedAgentDeckSession := ""
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

func lookupAgentDeckSessionByCodexID(ctx context.Context, codexSessionID string) (*agentDeckDBMatch, error) {
	codexSessionID = strings.TrimSpace(codexSessionID)
	if codexSessionID == "" {
		return nil, nil
	}
	for _, dbPath := range agentDeckStateDBPaths() {
		match, err := lookupAgentDeckSessionByCodexIDInDB(ctx, dbPath, codexSessionID)
		if err != nil {
			continue
		}
		if match != nil {
			return match, nil
		}
	}
	return nil, nil
}

func lookupAgentDeckSessionByCodexIDInDB(ctx context.Context, dbPath, codexSessionID string) (*agentDeckDBMatch, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, nil
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat agent-deck state database: %w", err)
	}
	if info.IsDir() {
		return nil, nil
	}

	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open agent-deck state database: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT id, project_path, tool_data
		FROM instances
		WHERE tool = 'codex' OR command LIKE '%codex%' OR tool_data LIKE '%codex_session_id%'
		ORDER BY last_accessed DESC, created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query agent-deck state database: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, projectPath, rawToolData string
		if err := rows.Scan(&id, &projectPath, &rawToolData); err != nil {
			return nil, fmt.Errorf("scan agent-deck state database: %w", err)
		}
		var toolData struct {
			CodexSessionID string `json:"codex_session_id"`
		}
		if err := json.Unmarshal([]byte(rawToolData), &toolData); err != nil {
			continue
		}
		if strings.TrimSpace(toolData.CodexSessionID) == codexSessionID {
			return &agentDeckDBMatch{
				SessionID:   strings.TrimSpace(id),
				ProjectPath: strings.TrimSpace(projectPath),
			}, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read agent-deck state database: %w", err)
	}
	return nil, nil
}

func agentDeckStateDBPaths() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return nil
	}
	baseDir := filepath.Join(homeDir, ".agent-deck")
	profilesDir := filepath.Join(baseDir, "profiles")

	profiles := []string{}
	if envProfile := strings.TrimSpace(os.Getenv("AGENTDECK_PROFILE")); envProfile != "" {
		profiles = append(profiles, filepath.Base(envProfile))
	}
	if configProfile := agentDeckDefaultProfile(filepath.Join(baseDir, "config.json")); configProfile != "" {
		profiles = append(profiles, filepath.Base(configProfile))
	}
	profiles = append(profiles, "default")
	if entries, err := os.ReadDir(profilesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				profiles = append(profiles, entry.Name())
			}
		}
	}

	paths := make([]string, 0, len(profiles))
	seen := map[string]bool{}
	for _, profile := range profiles {
		if profile == "" || profile == "." || profile == ".." {
			continue
		}
		path := filepath.Join(profilesDir, profile, "state.db")
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func agentDeckDefaultProfile(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var config struct {
		DefaultProfile string `json:"default_profile"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return ""
	}
	return strings.TrimSpace(config.DefaultProfile)
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
		return mailbox.NormalizeAddressList(addresses)
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
		return mailbox.NormalizeAddress(override)
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

func (m *sessionManager) createSession(ctx context.Context, input agentDeckCreateSessionInput) (map[string]any, error) {
	workdir, err := canonicalizeTargetWorkdir(input.Workdir, "creating")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.EnsureTitle) == "" {
		return nil, errors.New("ensure_title is required when creating a target session")
	}
	if strings.TrimSpace(input.EnsureCmd) == "" {
		return nil, errors.New("ensure_cmd is required when creating a target session")
	}

	existing, err := m.resolveSessionShow(ctx, input.EnsureTitle, ensureSessionShowTimeout)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := validateExistingSessionWorkdir(existing, input.Workdir, workdir); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("target session already exists: %s", input.EnsureTitle)
	}

	targetGroupPath, launchParentSessionID, launchNoParentLink, err := m.prepareCreateSessionLaunch(ctx, input)
	if err != nil {
		return nil, err
	}
	if targetGroupPath != "" {
		if err := m.ensureGroupPath(ctx, targetGroupPath); err != nil {
			return nil, err
		}
	}

	launchArgs := buildCreateSessionLaunchArgs(createSessionLaunchInput{
		EnsureTitle:        input.EnsureTitle,
		EnsureCmd:          input.EnsureCmd,
		Workdir:            workdir,
		ParentSessionID:    launchParentSessionID,
		NoParentLink:       launchNoParentLink,
		StartupInstruction: strings.TrimSpace(input.StartupInstruction),
		GroupPath:          targetGroupPath,
	})
	launchResult, err := runCommand(ctx, m.runner, launchArgs, runOptions{})
	if err != nil {
		return nil, err
	}
	data, err := parseSessionData(launchResult.Stdout, "agent-deck launch")
	if err != nil {
		return nil, err
	}

	out := sessionInfoMap(data, input.EnsureTitle)
	out["status"] = "created"
	out["created_target"] = true
	out["started_session"] = true
	out["notify_needed"] = false
	if strings.TrimSpace(input.StartupInstruction) != "" {
		out["startup_instruction_status"] = "started_waiting"
	} else {
		out["startup_instruction_status"] = "started"
	}
	return out, nil
}

func (m *sessionManager) requireSession(ctx context.Context, input agentDeckRequireSessionInput) (map[string]any, error) {
	identifier := firstNonEmpty(input.SessionID, input.SessionRef)
	if identifier == "" {
		return nil, errors.New("session_id or session_ref is required when requiring a target session")
	}

	workdir, err := canonicalizeTargetWorkdir(input.Workdir, "requiring")
	if err != nil {
		return nil, err
	}

	data, err := m.resolveSessionShow(ctx, identifier, ensureSessionShowTimeout)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("target session not found: %s", identifier)
	}
	if err := validateExistingSessionWorkdir(data, input.Workdir, workdir); err != nil {
		return nil, err
	}

	data, startedSession, notifyNeeded, startupInstructionStatus, err := m.startSessionIfNeeded(ctx, data, strings.TrimSpace(input.StartupInstruction))
	if err != nil {
		return nil, err
	}

	out := sessionInfoMap(data, firstNonEmpty(input.SessionRef, identifier))
	out["status"] = "ready"
	out["created_target"] = false
	out["started_session"] = startedSession
	out["notify_needed"] = notifyNeeded
	out["startup_instruction_status"] = startupInstructionStatus
	return out, nil
}

func canonicalizeTargetWorkdir(workdir, action string) (string, error) {
	trimmed := strings.TrimSpace(workdir)
	if trimmed == "" {
		return "", fmt.Errorf("workdir is required when %s a target session", action)
	}
	canonicalWorkdir, err := canonicalizeExistingPath(trimmed)
	if err != nil {
		return "", fmt.Errorf("workdir does not exist: %s", workdir)
	}
	return canonicalWorkdir, nil
}

func validateExistingSessionWorkdir(data *sessionData, requestedWorkdir, canonicalWorkdir string) error {
	existingPath := strings.TrimSpace(data.Path)
	if existingPath == "" {
		return errors.New("existing session path unavailable: cannot verify workdir match")
	}
	canonicalExistingPath, err := canonicalizeExistingPath(existingPath)
	if err != nil {
		return fmt.Errorf("canonicalize existing session path %q: %w", existingPath, err)
	}
	if canonicalExistingPath != canonicalWorkdir {
		return fmt.Errorf("session path mismatch: existing='%s' expected='%s'", data.Path, requestedWorkdir)
	}
	return nil
}

func (m *sessionManager) prepareCreateSessionLaunch(ctx context.Context, input agentDeckCreateSessionInput) (string, string, bool, error) {
	targetGroupPath := strings.TrimSpace(input.GroupPath)
	noParentLink := input.NoParentLink
	if noParentLink && strings.TrimSpace(input.ParentSessionID) != "" {
		return "", "", false, errors.New("no_parent_link cannot be combined with parent_session_id")
	}

	if targetGroupPath == "" && strings.TrimSpace(input.GroupParentSessionID) != "" {
		parentData, err := m.resolveSessionShow(ctx, input.GroupParentSessionID, ensureSessionShowTimeout)
		if err != nil {
			return "", "", false, err
		}
		if parentData == nil {
			return "", "", false, fmt.Errorf("group_parent_session_id not found: %s", input.GroupParentSessionID)
		}
		childGroupName := firstNonEmpty(input.ChildGroupName, input.EnsureTitle)
		targetGroupPath, err = deriveGroupPathFromParentGroup(strings.TrimSpace(parentData.Group), childGroupName)
		if err != nil {
			return "", "", false, err
		}
	}

	launchParentSessionID := strings.TrimSpace(input.ParentSessionID)
	launchNoParentLink := noParentLink
	if launchParentSessionID == "" && targetGroupPath == "" && !launchNoParentLink {
		return "", "", false, errors.New("creating a target session requires either group_path/group_parent_session_id or parent_session_id")
	}
	if launchParentSessionID == "" {
		return targetGroupPath, launchParentSessionID, launchNoParentLink, nil
	}

	parentData, err := m.resolveSessionShow(ctx, launchParentSessionID, ensureSessionShowTimeout)
	if err != nil {
		return "", "", false, err
	}
	if parentData == nil {
		return "", "", false, fmt.Errorf("parent_session_id not found: %s", input.ParentSessionID)
	}
	if strings.TrimSpace(parentData.ParentSessionID) == "" {
		return targetGroupPath, launchParentSessionID, launchNoParentLink, nil
	}
	if targetGroupPath == "" {
		targetGroupPath, err = deriveGroupPathFromParentGroup(strings.TrimSpace(parentData.Group), firstNonEmpty(parentData.Title, input.ParentSessionID, parentData.ID))
		if err != nil {
			return "", "", false, err
		}
	}
	return targetGroupPath, "", true, nil
}

func (m *sessionManager) startSessionIfNeeded(ctx context.Context, data *sessionData, startupInstruction string) (*sessionData, bool, bool, string, error) {
	if activeSessionStatuses[strings.TrimSpace(data.Status)] {
		return data, false, true, "not_needed_existing_session", nil
	}

	startArgs := []string{"agent-deck", "session", "start", "--json"}
	if startupInstruction != "" {
		startArgs = append(startArgs, "-m", startupInstruction)
	}
	startArgs = append(startArgs, data.ID)
	if _, err := runCommand(ctx, m.runner, startArgs, runOptions{}); err != nil {
		return nil, false, false, "", err
	}

	refreshed, err := m.resolveSessionShow(ctx, data.ID, ensureSessionShowTimeout)
	if err != nil {
		return nil, false, false, "", err
	}
	if refreshed != nil {
		data = refreshed
	}

	startupInstructionStatus := "started"
	if startupInstruction != "" {
		startupInstructionStatus = "started_waiting"
	}
	return data, true, false, startupInstructionStatus, nil
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
