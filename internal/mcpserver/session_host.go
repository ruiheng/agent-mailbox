package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// sessionHost is deliberately a closed set. Adding another host is an
// explicit product change, not a runtime registration mechanism.
type sessionHost string

const (
	sessionHostAgentDeck sessionHost = "agent-deck"
	sessionHostThurbox   sessionHost = "thurbox"

	genericSessionCreateOutputUnparseableDetail = "generic session create returned unusable output"
	genericAgentDeckNestedParentDetail          = "generic agent-deck create does not support a parent that is itself a child session"
	genericAgentDeckEmptyParentGroupDetail      = "generic agent-deck create requires a parent with a non-empty group"
	genericAgentDeckGroupMismatchDetail         = "refreshed agent-deck session group does not match the parent group snapshot"
)

var (
	genericSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	thurboxUUIDPattern        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// Source-backed Thurbox v1.7.1 evidence: src/cli/sessions.rs List calls
	// Database::list_active_sessions, while src/session/mod.rs declares
	// HOOK_STATES as working, blocked, done, and idle (or null in JSON). That
	// pinned list grammar has no stopped state we can safely restart. Keep the
	// stopped set explicit (and empty) rather than guessing from tmux or an
	// unrelated field. The restart fixture remains parser-grammar evidence.
	thurboxActiveSessionStatuses = map[string]bool{
		"":        true,
		"working": true,
		"blocked": true,
		"done":    true,
		"idle":    true,
	}
	thurboxStoppedSessionStatuses = map[string]bool{}
)

type hostSessionData struct {
	Host            sessionHost
	ID              string
	Name            string
	Status          string
	Group           string
	Path            string
	ParentSessionID string
}

type hostWorkdirVerification struct {
	State        string
	ObservedPath string
	Err          error
}

type hostSessionOutputFailureKind string

const (
	hostSessionOutputGrammarFailure  hostSessionOutputFailureKind = "grammar"
	hostSessionOutputIdentityFailure hostSessionOutputFailureKind = "identity"
)

// hostSessionOutputError distinguishes a host's parseable command execution
// from output that no longer matches the approved session grammar. Recovery
// after a confirmed start must not classify this through error text.
type hostSessionOutputError struct {
	cause error
	kind  hostSessionOutputFailureKind
}

func (e *hostSessionOutputError) Error() string {
	return e.cause.Error()
}

func (e *hostSessionOutputError) Unwrap() error {
	return e.cause
}

func hostSessionOutputFailure(err error) error {
	if err == nil {
		return nil
	}
	return &hostSessionOutputError{cause: err, kind: hostSessionOutputGrammarFailure}
}

func hostSessionIdentityFailure(err error) error {
	if err == nil {
		return nil
	}
	return &hostSessionOutputError{cause: err, kind: hostSessionOutputIdentityFailure}
}

func isHostSessionOutputFailure(err error) bool {
	var outputErr *hostSessionOutputError
	return errors.As(err, &outputErr)
}

func isHostSessionIdentityFailure(err error) bool {
	var outputErr *hostSessionOutputError
	return errors.As(err, &outputErr) && outputErr.kind == hostSessionOutputIdentityFailure
}

func parseSessionHost(value string) (sessionHost, error) {
	switch sessionHost(value) {
	case sessionHostAgentDeck:
		return sessionHostAgentDeck, nil
	case sessionHostThurbox:
		return sessionHostThurbox, nil
	default:
		return "", fmt.Errorf("unsupported session host %q; supported hosts are agent-deck and thurbox", value)
	}
}

// selectSessionHost intentionally treats a valid Thurbox environment as the
// immediate host. A nested Thurbox session commonly retains an outer Agent
// Deck identity, which is context rather than an ambiguity.
func (m *sessionManager) selectSessionHost(ctx context.Context, override string) (sessionHost, error) {
	if override != "" {
		return parseSessionHost(override)
	}
	if sessionID, _ := detectCurrentThurboxSessionID(); sessionID != "" {
		return sessionHostThurbox, nil
	}

	snapshot := m.snapshotState()
	if strings.TrimSpace(snapshot.DetectedAgentDeckSession) != "" {
		return sessionHostAgentDeck, nil
	}
	agentDeckSessionID, _, _, _, err := m.detectCurrentAgentDeckSessionID(ctx, snapshot.DetectedToolSessions["codex"], snapshot.DefaultWorkdir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(agentDeckSessionID) != "" {
		return sessionHostAgentDeck, nil
	}
	return "", errors.New("session host is unknown; set host explicitly")
}

func detectCurrentThurboxSessionID() (string, []string) {
	sessionID := strings.TrimSpace(os.Getenv("THURBOX_SESSION"))
	if sessionID == "" {
		return "", nil
	}
	if !thurboxUUIDPattern.MatchString(sessionID) {
		return "", []string{"THURBOX_SESSION is set but is not a valid Thurbox v1.7.1 session UUID; ignoring it for auto-bind"}
	}
	return sessionID, nil
}

func thurboxAddress(sessionID string) string {
	return "thurbox/" + sessionID
}

func (m *sessionManager) resolveHostSession(ctx context.Context, host sessionHost, identifier string, timeout time.Duration) (*hostSessionData, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, errors.New("session identifier is required")
	}

	switch host {
	case sessionHostAgentDeck:
		data, err := m.resolveSessionShow(ctx, identifier, timeout)
		if err != nil || data == nil {
			return nil, err
		}
		hostData := hostSessionFromAgentDeck(data)
		if hostData.ID == "" {
			return nil, hostSessionOutputFailure(errors.New("agent-deck session show returned a session without an id"))
		}
		return hostData, nil
	case sessionHostThurbox:
		return m.resolveThurboxSession(ctx, identifier, timeout)
	default:
		return nil, fmt.Errorf("unsupported session host %q", host)
	}
}

func hostSessionFromAgentDeck(data *sessionData) *hostSessionData {
	if data == nil {
		return nil
	}
	return &hostSessionData{
		Host:            sessionHostAgentDeck,
		ID:              strings.TrimSpace(data.ID),
		Name:            strings.TrimSpace(data.Title),
		Status:          strings.TrimSpace(data.Status),
		Group:           strings.TrimSpace(data.Group),
		Path:            strings.TrimSpace(data.Path),
		ParentSessionID: strings.TrimSpace(data.ParentSessionID),
	}
}

// resolveThurboxSession follows the v1.7.1 CLI contract: session get for a
// UUID and session list for exact-name resolution. List output is not used as
// a permissive fallback for malformed get output.
func (m *sessionManager) resolveThurboxSession(ctx context.Context, identifier string, timeout time.Duration) (*hostSessionData, error) {
	if thurboxUUIDPattern.MatchString(identifier) {
		result, err := runProbe(ctx, m.runner, []string{"thurbox-cli", "session", "get", "--json", identifier}, runOptions{timeout: timeout}, true)
		if err != nil {
			return nil, err
		}
		if thurboxSessionGetIsMissing(result, identifier) {
			return nil, nil
		}
		if result == nil || result.ExitCode != 0 {
			return nil, thurboxProbeFailure("thurbox v1.7.1 session get", result)
		}
		data, err := parseThurboxSessionRecord(result.Stdout, "thurbox v1.7.1 session get")
		if err != nil {
			return nil, hostSessionOutputFailure(err)
		}
		if data.ID != identifier {
			return nil, hostSessionIdentityFailure(fmt.Errorf("thurbox v1.7.1 session get returned session id %q, want %q", data.ID, identifier))
		}
		return data, nil
	}

	result, err := runProbe(ctx, m.runner, []string{"thurbox-cli", "session", "list", "--json"}, runOptions{timeout: timeout}, true)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 {
		return nil, thurboxProbeFailure("thurbox v1.7.1 session list", result)
	}
	sessions, err := parseThurboxSessionList(result.Stdout)
	if err != nil {
		return nil, hostSessionOutputFailure(err)
	}

	var found *hostSessionData
	for index := range sessions {
		candidate := sessions[index]
		if candidate.ID != identifier && candidate.Name != identifier {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("thurbox session reference is ambiguous: %s", identifier)
		}
		found = &candidate
	}
	return found, nil
}

// In pinned Thurbox v1.7.1, sessions::resolve formats a missing UUID as
// "Session not found: <uuid>". src/bin/thurbox-cli.rs adds the "error: "
// prefix and exits 1. No other non-zero get/list result means not found.
func thurboxSessionGetIsMissing(result *RunResult, identifier string) bool {
	return result != nil &&
		result.ExitCode == 1 &&
		strings.TrimSpace(result.Stdout) == "" &&
		strings.TrimSpace(result.Stderr) == "error: Session not found: "+identifier
}

func thurboxProbeFailure(operation string, result *RunResult) error {
	if result == nil {
		return fmt.Errorf("%s returned no result", operation)
	}
	return fmt.Errorf("%s failed with exit code %d", operation, result.ExitCode)
}

func parseThurboxSessionList(text string) ([]hostSessionData, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	var rawSessions []json.RawMessage
	if err := decoder.Decode(&rawSessions); err != nil {
		return nil, fmt.Errorf("thurbox v1.7.1 session list returned invalid JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("thurbox v1.7.1 session list returned invalid JSON: %w", err)
	}

	sessions := make([]hostSessionData, 0, len(rawSessions))
	for _, raw := range rawSessions {
		data, err := parseThurboxSessionRecord(string(raw), "thurbox v1.7.1 session list")
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *data)
	}
	return sessions, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("contains multiple JSON values")
	}
	return err
}

// parseThurboxSessionRecord accepts precisely the get/list object emitted by
// Thurbox v1.7.1. In particular cwd is the one effective-workdir field; this
// adapter never falls back to repo_path, worktree_path, or another lookalike.
func parseThurboxSessionRecord(text, context string) (*hostSessionData, error) {
	fields, err := parseStrictJSONObject(text, context, []string{
		"id", "name", "agent", "backend_type", "agent_session_id", "cwd",
		"parent_session_id", "display_order", "worktrees", "hook_state",
	})
	if err != nil {
		return nil, err
	}

	id, err := thurboxRequiredString(fields, "id", context)
	if err != nil {
		return nil, err
	}
	if !thurboxUUIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%s returned invalid session id %q", context, id)
	}
	name, err := thurboxRequiredString(fields, "name", context)
	if err != nil {
		return nil, err
	}
	if _, err := thurboxRequiredString(fields, "agent", context); err != nil {
		return nil, err
	}
	if _, err := thurboxRequiredString(fields, "backend_type", context); err != nil {
		return nil, err
	}
	if _, err := thurboxOptionalString(fields, "agent_session_id", context); err != nil {
		return nil, err
	}
	path, err := thurboxOptionalString(fields, "cwd", context)
	if err != nil {
		return nil, err
	}
	parentSessionID, err := thurboxOptionalString(fields, "parent_session_id", context)
	if err != nil {
		return nil, err
	}
	if parentSessionID != "" && !thurboxUUIDPattern.MatchString(parentSessionID) {
		return nil, fmt.Errorf("%s returned invalid parent_session_id %q", context, parentSessionID)
	}
	if err := thurboxOptionalInteger(fields, "display_order", context); err != nil {
		return nil, err
	}
	if err := thurboxWorktrees(fields, context); err != nil {
		return nil, err
	}
	status, err := thurboxOptionalString(fields, "hook_state", context)
	if err != nil {
		return nil, err
	}
	status = strings.TrimSpace(status)
	if !thurboxActiveSessionStatuses[status] && !thurboxStoppedSessionStatuses[status] {
		return nil, fmt.Errorf("%s returned unclassified hook_state %q", context, status)
	}

	return &hostSessionData{
		Host:            sessionHostThurbox,
		ID:              id,
		Name:            name,
		Status:          status,
		Path:            path,
		ParentSessionID: parentSessionID,
	}, nil
}

// parseThurboxCreatedSession accepts precisely the v1.7.1 create object. A
// successful create with any other output is a recovery condition, not a
// generic tool error, because the host may already have created the session.
func parseThurboxCreatedSession(text string) (*hostSessionData, error) {
	const context = "thurbox v1.7.1 session create"
	fields, err := parseStrictJSONObject(text, context, []string{
		"id", "name", "agent", "agent_session_id", "cwd", "parent_session_id",
	})
	if err != nil {
		return nil, err
	}
	id, err := thurboxRequiredString(fields, "id", context)
	if err != nil {
		return nil, err
	}
	if !thurboxUUIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%s returned invalid session id %q", context, id)
	}
	name, err := thurboxRequiredString(fields, "name", context)
	if err != nil {
		return nil, err
	}
	if _, err := thurboxRequiredString(fields, "agent", context); err != nil {
		return nil, err
	}
	if _, err := thurboxOptionalString(fields, "agent_session_id", context); err != nil {
		return nil, err
	}
	path, err := thurboxOptionalString(fields, "cwd", context)
	if err != nil {
		return nil, err
	}
	parentSessionID, err := thurboxOptionalString(fields, "parent_session_id", context)
	if err != nil {
		return nil, err
	}
	if parentSessionID != "" && !thurboxUUIDPattern.MatchString(parentSessionID) {
		return nil, fmt.Errorf("%s returned invalid parent_session_id %q", context, parentSessionID)
	}
	return &hostSessionData{
		Host:            sessionHostThurbox,
		ID:              id,
		Name:            name,
		Path:            path,
		ParentSessionID: parentSessionID,
	}, nil
}

func parseThurboxRestartResult(text, sessionID string) error {
	const context = "thurbox v1.7.1 session restart"
	fields, err := parseStrictJSONObject(text, context, []string{"restarted", "session_id", "session_name"})
	if err != nil {
		return err
	}
	rawRestarted := fields["restarted"]
	var restarted bool
	if err := json.Unmarshal(rawRestarted, &restarted); err != nil || !restarted {
		return fmt.Errorf("%s returned invalid restarted state", context)
	}
	returnedID, err := thurboxRequiredString(fields, "session_id", context)
	if err != nil {
		return err
	}
	if returnedID != sessionID {
		return fmt.Errorf("%s returned session_id %q, want %q", context, returnedID, sessionID)
	}
	if _, err := thurboxRequiredString(fields, "session_name", context); err != nil {
		return err
	}
	return nil
}

func parseStrictJSONObject(text, context string, allowed []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", context, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON: %w", context, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("%s returned a non-object JSON value", context)
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = true
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("%s returned no %q field", context, key)
		}
	}
	unknown := make([]string, 0)
	for key := range fields {
		if !allowedSet[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%s returned unknown fields: %s", context, strings.Join(unknown, ", "))
	}
	return fields, nil
}

func thurboxRequiredString(fields map[string]json.RawMessage, key, context string) (string, error) {
	value, err := thurboxOptionalString(fields, key, context)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s returned an empty %q field", context, key)
	}
	return strings.TrimSpace(value), nil
}

func thurboxOptionalString(fields map[string]json.RawMessage, key, context string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("%s returned no %q field", context, key)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s returned invalid %q field: %w", context, key, err)
	}
	return strings.TrimSpace(value), nil
}

func thurboxOptionalInteger(fields map[string]json.RawMessage, key, context string) error {
	raw, ok := fields[key]
	if !ok {
		return fmt.Errorf("%s returned no %q field", context, key)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s returned invalid %q field: %w", context, key, err)
	}
	return nil
}

func thurboxWorktrees(fields map[string]json.RawMessage, context string) error {
	raw, ok := fields["worktrees"]
	if !ok {
		return fmt.Errorf("%s returned no %q field", context, "worktrees")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%s returned invalid %q field: %w", context, "worktrees", err)
	}
	for _, entry := range entries {
		fields, err := parseStrictJSONObject(string(entry), context+" worktree", []string{"repo_path", "worktree_path", "branch"})
		if err != nil {
			return err
		}
		for _, key := range []string{"repo_path", "worktree_path", "branch"} {
			if _, err := thurboxRequiredString(fields, key, context+" worktree"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateGenericSessionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("session_name is required when creating a target session")
	}
	if !genericSessionNamePattern.MatchString(name) {
		return "", errors.New("session_name must use letters, digits, dot, underscore, or hyphen")
	}
	return name, nil
}

func selectedHostLaunchValue(host sessionHost, fullCommandLine, thurboxAgentKey string) (string, error) {
	switch host {
	case sessionHostAgentDeck:
		fullCommandLine = strings.TrimSpace(fullCommandLine)
		if fullCommandLine == "" {
			return "", errors.New("full_command_line is required when creating an agent-deck session")
		}
		return fullCommandLine, nil
	case sessionHostThurbox:
		thurboxAgentKey = strings.TrimSpace(thurboxAgentKey)
		if thurboxAgentKey == "" {
			return "", errors.New("thurbox_agent_key is required when creating a thurbox session")
		}
		return thurboxAgentKey, nil
	default:
		return "", fmt.Errorf("unsupported session host %q", host)
	}
}

func (m *sessionManager) createHostSession(ctx context.Context, host sessionHost, name, workdir, parentSessionID, fullCommandLine, thurboxAgentKey string) (map[string]any, error) {
	name, err := validateGenericSessionName(name)
	if err != nil {
		return nil, err
	}
	launchValue, err := selectedHostLaunchValue(host, fullCommandLine, thurboxAgentKey)
	if err != nil {
		return nil, err
	}
	canonicalWorkdir, err := canonicalizeTargetWorkdir(workdir, "creating")
	if err != nil {
		return nil, err
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil, errors.New("parent_session_id is required when creating a target session")
	}
	if strings.Contains(parentSessionID, "/") {
		return nil, errors.New("parent_session_id must be a same-host session ID, not an address")
	}

	parent, err := m.resolveHostSession(ctx, host, parentSessionID, ensureSessionShowTimeout)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, fmt.Errorf("parent_session_id not found: %s", parentSessionID)
	}
	if parent.ID != parentSessionID {
		return nil, fmt.Errorf("parent_session_id must be a same-host session ID, not a session name: %s", parentSessionID)
	}
	parentGroupSnapshot := ""
	if host == sessionHostAgentDeck {
		if parent.ParentSessionID != "" {
			return nil, errors.New(genericAgentDeckNestedParentDetail)
		}
		parentGroupSnapshot = strings.TrimSpace(parent.Group)
		if parentGroupSnapshot == "" {
			return nil, errors.New(genericAgentDeckEmptyParentGroupDetail)
		}
	}

	existing, err := m.resolveHostSession(ctx, host, name, ensureSessionShowTimeout)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := validateHostSessionWorkdir(existing, workdir, canonicalWorkdir); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("target session already exists: %s", name)
	}

	var created *hostSessionData
	switch host {
	case sessionHostAgentDeck:
		result, err := runRedactedCommand(ctx, m.runner, []string{
			"agent-deck", "launch", "--json", "--title", name, "--cmd", launchValue,
			"--group", parentGroupSnapshot, "--parent", parentSessionID, canonicalWorkdir,
		}, runOptions{}, "generic agent-deck session create")
		if err != nil {
			return nil, err
		}
		legacyData, parseErr := parseSessionData(result.Stdout, "agent-deck launch")
		if parseErr != nil || legacyData == nil || strings.TrimSpace(legacyData.ID) == "" {
			return createRecoveryResult(host, name, parentSessionID, canonicalWorkdir), nil
		}
		created = hostSessionFromAgentDeck(legacyData)
	case sessionHostThurbox:
		result, err := runRedactedCommand(ctx, m.runner, []string{
			"thurbox-cli", "session", "create", "--json", "--name", name,
			"--repo-path", canonicalWorkdir, "--agent", launchValue, "--parent", parentSessionID,
		}, runOptions{}, "generic thurbox session create")
		if err != nil {
			return nil, err
		}
		created, err = parseThurboxCreatedSession(result.Stdout)
		if err != nil || created == nil || strings.TrimSpace(created.ID) == "" {
			return createRecoveryResult(host, name, parentSessionID, canonicalWorkdir), nil
		}
	default:
		return nil, fmt.Errorf("unsupported session host %q", host)
	}

	refreshed, err := m.resolveHostSession(ctx, host, created.ID, ensureSessionShowTimeout)
	if err != nil {
		state := "post_create_lookup_failed"
		if isHostSessionIdentityFailure(err) {
			state = "post_create_identity_mismatch"
		}
		return createdUnverifiedResult(created, name, canonicalWorkdir, state, "", err.Error()), nil
	}
	if refreshed == nil {
		return createdUnverifiedResult(created, name, canonicalWorkdir, "post_create_lookup_failed", "", "target session not found after create"), nil
	}
	if err := verifyCreatedHostSessionIdentity(host, created, refreshed, name, parentSessionID); err != nil {
		resultData := created
		observedPath := ""
		if refreshed.ID == created.ID {
			resultData = refreshed
			observedPath = refreshed.Path
		}
		return createdUnverifiedResult(resultData, name, canonicalWorkdir, "post_create_identity_mismatch", observedPath, err.Error()), nil
	}
	if host == sessionHostAgentDeck && strings.TrimSpace(refreshed.Group) != parentGroupSnapshot {
		return createdUnverifiedResult(refreshed, name, canonicalWorkdir, "post_create_group_mismatch", refreshed.Path, genericAgentDeckGroupMismatchDetail), nil
	}
	verification := verifyHostSessionWorkdir(refreshed, workdir, canonicalWorkdir)
	if verification.State != "verified" {
		return createdUnverifiedResult(refreshed, name, canonicalWorkdir, verification.State, verification.ObservedPath, verification.Err.Error()), nil
	}
	return createdVerifiedResult(refreshed, name, canonicalWorkdir), nil
}

func verifyCreatedHostSessionIdentity(host sessionHost, created, refreshed *hostSessionData, requestedName, requestedParentSessionID string) error {
	if created == nil || refreshed == nil {
		return errors.New("target session identity unavailable after create")
	}
	if created.ID != refreshed.ID {
		return fmt.Errorf("created session id %q does not match refreshed session id %q", created.ID, refreshed.ID)
	}
	switch host {
	case sessionHostAgentDeck:
		// Agent Deck's launch output is a receipt: only its session ID is needed
		// to retrieve the authoritative post-create session record below.
	case sessionHostThurbox:
		if created.Name != requestedName {
			return fmt.Errorf("created session name %q does not match requested name %q", created.Name, requestedName)
		}
		if created.ParentSessionID != requestedParentSessionID {
			return fmt.Errorf("created session parent %q does not match requested parent %q", created.ParentSessionID, requestedParentSessionID)
		}
	default:
		return fmt.Errorf("unsupported session host %q", host)
	}
	if refreshed.Name != requestedName {
		return fmt.Errorf("refreshed session name %q does not match requested name %q", refreshed.Name, requestedName)
	}
	if refreshed.ParentSessionID != requestedParentSessionID {
		return fmt.Errorf("refreshed session parent %q does not match requested parent %q", refreshed.ParentSessionID, requestedParentSessionID)
	}
	return nil
}

type hostSessionSelectorKind string

const (
	hostSessionSelectorID  hostSessionSelectorKind = "session_id"
	hostSessionSelectorRef hostSessionSelectorKind = "session_ref"
)

func (m *sessionManager) requireHostSession(ctx context.Context, host sessionHost, identifier, workdir string, selectorKind hostSessionSelectorKind, autoRestart bool) (map[string]any, error) {
	canonicalWorkdir, err := canonicalizeTargetWorkdir(workdir, "requiring")
	if err != nil {
		return nil, err
	}
	return m.requireHostSessionWithCanonicalWorkdir(ctx, host, identifier, workdir, canonicalWorkdir, selectorKind, autoRestart)
}

func (m *sessionManager) requireHostSessionWithCanonicalWorkdir(ctx context.Context, host sessionHost, identifier, requestedWorkdir, canonicalWorkdir string, selectorKind hostSessionSelectorKind, autoRestart bool) (map[string]any, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, errors.New("session identifier is required when requiring a target session")
	}
	data, err := m.resolveHostSession(ctx, host, identifier, ensureSessionShowTimeout)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return notFoundHostSessionResult(host, identifier), nil
	}
	if selectorKind == hostSessionSelectorID && data.ID != identifier {
		return nil, fmt.Errorf("session_id must exactly match the resolved host session ID: %s", identifier)
	}
	if err := validateHostSessionWorkdir(data, requestedWorkdir, canonicalWorkdir); err != nil {
		return nil, err
	}
	if hostSessionIsReady(host, data) {
		return readyVerifiedResult(data, identifier, canonicalWorkdir, false), nil
	}
	if !autoRestart {
		return notReadyVerifiedResult(data, identifier, canonicalWorkdir), nil
	}

	switch host {
	case sessionHostAgentDeck:
		if _, err := runCommand(ctx, m.runner, []string{"agent-deck", "session", "start", "--json", data.ID}, runOptions{}); err != nil {
			return nil, err
		}
	case sessionHostThurbox:
		if !thurboxSessionNeedsRestart(data.Status) {
			return nil, fmt.Errorf("thurbox session status %q is not restartable", data.Status)
		}
		result, err := runCommand(ctx, m.runner, []string{"thurbox-cli", "session", "restart", "--json", data.ID}, runOptions{})
		if err != nil {
			return nil, err
		}
		if err := parseThurboxRestartResult(result.Stdout, data.ID); err != nil {
			return readyUnverifiedResult(data, identifier, canonicalWorkdir, "post_start_output_unparseable", "", err.Error()), nil
		}
	default:
		return nil, fmt.Errorf("unsupported session host %q", host)
	}

	return m.reverifyStartedHostSession(ctx, host, data, identifier, requestedWorkdir, canonicalWorkdir)
}

func (m *sessionManager) reverifyStartedHostSession(ctx context.Context, host sessionHost, previous *hostSessionData, sessionRef, requestedWorkdir, canonicalWorkdir string) (map[string]any, error) {
	refreshed, err := m.resolveHostSession(ctx, host, previous.ID, ensureSessionShowTimeout)
	if err != nil {
		state := "post_start_lookup_failed"
		if isHostSessionOutputFailure(err) {
			state = "post_start_output_unparseable"
		}
		return readyUnverifiedResult(previous, sessionRef, canonicalWorkdir, state, "", err.Error()), nil
	}
	if refreshed == nil {
		return readyUnverifiedResult(previous, sessionRef, canonicalWorkdir, "post_start_disappeared", "", "target session not found after start"), nil
	}
	if refreshed.ID != previous.ID {
		return readyUnverifiedResult(previous, sessionRef, canonicalWorkdir, "post_start_output_unparseable", "", fmt.Sprintf("refreshed session id %q does not match started session id %q", refreshed.ID, previous.ID)), nil
	}
	if !hostSessionIsReady(host, refreshed) {
		return readyUnverifiedResult(previous, sessionRef, canonicalWorkdir, "post_start_not_ready", refreshed.Path, "target session is not ready after start"), nil
	}
	verification := verifyHostSessionWorkdir(refreshed, requestedWorkdir, canonicalWorkdir)
	if verification.State != "verified" {
		return readyUnverifiedResult(previous, sessionRef, canonicalWorkdir, postStartVerificationState(verification.State), verification.ObservedPath, verification.Err.Error()), nil
	}
	return readyVerifiedResult(refreshed, sessionRef, canonicalWorkdir, true), nil
}

func postStartVerificationState(state string) string {
	switch state {
	case "path_mismatch":
		return "post_start_path_mismatch"
	case "path_unavailable":
		return "post_start_path_unavailable"
	default:
		return "post_start_lookup_failed"
	}
}

func hostSessionIsReady(host sessionHost, data *hostSessionData) bool {
	if data == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(data.Status))
	switch host {
	case sessionHostAgentDeck:
		return activeSessionStatuses[status]
	case sessionHostThurbox:
		return thurboxActiveSessionStatuses[status]
	default:
		return false
	}
}

func thurboxSessionNeedsRestart(status string) bool {
	return thurboxStoppedSessionStatuses[strings.ToLower(strings.TrimSpace(status))]
}

func verifyHostSessionWorkdir(data *hostSessionData, requestedWorkdir, canonicalWorkdir string) hostWorkdirVerification {
	if data == nil || strings.TrimSpace(data.Path) == "" {
		return hostWorkdirVerification{
			State: "path_unavailable",
			Err:   errors.New("existing session path unavailable: cannot verify workdir match"),
		}
	}
	canonicalExistingPath, err := canonicalizeExistingPath(data.Path)
	if err != nil {
		return hostWorkdirVerification{
			State:        "path_unavailable",
			ObservedPath: data.Path,
			Err:          fmt.Errorf("canonicalize existing session path %q: %w", data.Path, err),
		}
	}
	if canonicalExistingPath != canonicalWorkdir {
		return hostWorkdirVerification{
			State:        "path_mismatch",
			ObservedPath: data.Path,
			Err:          fmt.Errorf("session path mismatch: existing='%s' expected='%s'", data.Path, requestedWorkdir),
		}
	}
	return hostWorkdirVerification{State: "verified", ObservedPath: data.Path}
}

func validateHostSessionWorkdir(data *hostSessionData, requestedWorkdir, canonicalWorkdir string) error {
	verification := verifyHostSessionWorkdir(data, requestedWorkdir, canonicalWorkdir)
	return verification.Err
}

func createdVerifiedResult(data *hostSessionData, sessionRef, canonicalWorkdir string) map[string]any {
	out := hostSessionInfoMap(data, sessionRef)
	out["status"] = "created"
	out["created_target"] = true
	out["started_session"] = true
	out["recovery_required"] = false
	out["verification"] = verificationMap("verified", canonicalWorkdir, data.Path, "")
	return out
}

func createdUnverifiedResult(data *hostSessionData, sessionRef, canonicalWorkdir, state, observedPath, detail string) map[string]any {
	out := hostSessionInfoMap(data, sessionRef)
	out["status"] = "created_unverified"
	out["created_target"] = true
	out["started_session"] = true
	out["recovery_required"] = true
	out["verification"] = verificationMap(state, canonicalWorkdir, observedPath, detail)
	return out
}

func createRecoveryResult(host sessionHost, sessionRef, parentSessionID, canonicalWorkdir string) map[string]any {
	return map[string]any{
		"host":              string(host),
		"status":            "create_recovery_required",
		"created_target":    nil,
		"started_session":   nil,
		"recovery_required": true,
		"verification":      verificationMap("create_output_unparseable", canonicalWorkdir, "", genericSessionCreateOutputUnparseableDetail),
		"session_id":        nil,
		"session_ref":       sessionRef,
		"session_name":      sessionRef,
		"session_status":    nil,
		"path":              nil,
		"parent_session_id": nilIfEmpty(parentSessionID),
		"addresses":         []string{},
	}
}

func readyVerifiedResult(data *hostSessionData, sessionRef, canonicalWorkdir string, started bool) map[string]any {
	out := hostSessionInfoMap(data, sessionRef)
	out["status"] = "ready"
	out["created_target"] = false
	out["started_session"] = started
	out["recovery_required"] = false
	out["verification"] = verificationMap("verified", canonicalWorkdir, data.Path, "")
	return out
}

func notReadyVerifiedResult(data *hostSessionData, sessionRef, canonicalWorkdir string) map[string]any {
	out := hostSessionInfoMap(data, sessionRef)
	out["status"] = "not_ready"
	out["created_target"] = false
	out["started_session"] = false
	out["recovery_required"] = false
	out["verification"] = verificationMap("verified", canonicalWorkdir, data.Path, "")
	return out
}

func notFoundHostSessionResult(host sessionHost, sessionRef string) map[string]any {
	out := hostSessionInfoMap(nil, sessionRef)
	out["host"] = string(host)
	out["status"] = "not_found"
	out["created_target"] = false
	out["started_session"] = false
	out["recovery_required"] = false
	return out
}

func readyUnverifiedResult(previous *hostSessionData, sessionRef, canonicalWorkdir, state, observedPath, detail string) map[string]any {
	out := hostSessionInfoMap(previous, sessionRef)
	out["status"] = "ready_unverified"
	out["created_target"] = false
	out["started_session"] = true
	out["recovery_required"] = true
	out["verification"] = verificationMap(state, canonicalWorkdir, observedPath, detail)
	return out
}

func verificationMap(state, requestedWorkdir, observedPath, detail string) map[string]any {
	out := map[string]any{
		"state":             state,
		"requested_workdir": nilIfEmpty(requestedWorkdir),
		"observed_path":     nilIfEmpty(observedPath),
	}
	if strings.TrimSpace(detail) != "" {
		out["error"] = detail
	}
	return out
}

func hostSessionInfoMap(data *hostSessionData, sessionRef string) map[string]any {
	addresses := []string{}
	if data != nil && strings.TrimSpace(data.ID) != "" {
		addresses = append(addresses, hostSessionAddress(data.Host, data.ID))
	}
	if data == nil {
		return map[string]any{
			"session_id":        nil,
			"session_ref":       sessionRef,
			"session_name":      nil,
			"session_status":    nil,
			"path":              nil,
			"parent_session_id": nil,
			"addresses":         addresses,
		}
	}
	return map[string]any{
		"host":              string(data.Host),
		"session_id":        data.ID,
		"session_ref":       firstNonEmpty(sessionRef, data.Name, data.ID),
		"session_name":      nilIfEmpty(data.Name),
		"session_status":    nilIfEmpty(data.Status),
		"path":              nilIfEmpty(data.Path),
		"parent_session_id": nilIfEmpty(data.ParentSessionID),
		"addresses":         addresses,
	}
}

func hostSessionAddress(host sessionHost, sessionID string) string {
	switch host {
	case sessionHostAgentDeck:
		return agentDeckAddress(sessionID)
	case sessionHostThurbox:
		return thurboxAddress(sessionID)
	default:
		return ""
	}
}
