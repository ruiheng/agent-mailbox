package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Success *bool  `json:"success,omitempty"`
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
	result, err := runProbe(ctx, m.runner, []string{"agent-deck", "session", "show", identifier, "--json"}, runOptions{timeout: syncCmdTimeout}, false)
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

func (m *sessionManager) ensureSession(ctx context.Context, input agentDeckEnsureSessionInput) (map[string]any, error) {
	bound, err := m.boundState(ctx)
	if err != nil {
		return nil, err
	}

	identifier := firstNonEmpty(input.SessionID, input.SessionRef)
	workdir := firstNonEmpty(input.Workdir, bound.DefaultWorkdir)
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

	if data == nil {
		if input.EnsureTitle == "" {
			return nil, errors.New("target session missing: provide session_id, session_ref, or ensure_title")
		}
		if input.EnsureCmd == "" {
			return nil, errors.New("ensure_cmd is required when creating a target session")
		}
		if input.ParentSessionID == "" {
			return nil, errors.New("parent_session_id is required when creating a target session")
		}
		if workdir == "" {
			return nil, errors.New("workdir is required when creating a target session")
		}
		info, statErr := os.Stat(workdir)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("workdir does not exist: %s", workdir)
		}

		listenerMessage := strings.TrimSpace(input.ListenerMessage)
		launchArgs := []string{
			"agent-deck", "launch", "--json",
			"--title", input.EnsureTitle,
			"--parent", input.ParentSessionID,
			"--cmd", input.EnsureCmd,
		}
		if listenerMessage != "" {
			launchArgs = append(launchArgs, "--message", listenerMessage)
		}
		launchArgs = append(launchArgs, workdir)
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
			listenerStatus = "started_waiting"
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
