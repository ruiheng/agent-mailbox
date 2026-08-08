package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentDeckResolveSessionInput struct {
	Session  string   `json:"session,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
}

type agentDeckCreateSessionInput struct {
	EnsureTitle          string `json:"ensure_title,omitempty"`
	EnsureCmd            string `json:"ensure_cmd,omitempty"`
	ParentSessionID      string `json:"parent_session_id,omitempty"`
	GroupPath            string `json:"group_path,omitempty"`
	GroupParentSessionID string `json:"group_parent_session_id,omitempty"`
	ChildGroupName       string `json:"child_group_name,omitempty"`
	NoParentLink         bool   `json:"no_parent_link,omitempty"`
	Workdir              string `json:"workdir"`
	StartupInstruction   string `json:"startup_instruction,omitempty"`
}

type agentDeckRequireSessionInput struct {
	SessionID  string   `json:"session_id,omitempty"`
	SessionRef string   `json:"session_ref,omitempty"`
	Sessions   []string `json:"sessions,omitempty"`
	Workdir    string   `json:"workdir"`
}

// The generic tools intentionally expose only logical session inputs. A
// launch_profile is a configured host profile such as "codex"; it is never a
// shell command and group placement is deliberately left to host-specific
// compatibility tools.
type sessionResolveInput struct {
	Host     string   `json:"host,omitempty"`
	Session  string   `json:"session,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
}

type sessionCreateInput struct {
	Host            string `json:"host,omitempty"`
	SessionName     string `json:"session_name"`
	Workdir         string `json:"workdir"`
	ParentSessionID string `json:"parent_session_id"`
	LaunchProfile   string `json:"launch_profile"`
}

type sessionRequireInput struct {
	Host       string   `json:"host,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	SessionRef string   `json:"session_ref,omitempty"`
	Sessions   []string `json:"sessions,omitempty"`
	Workdir    string   `json:"workdir"`
}

func (s *Service) registerSessionTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_resolve",
		Description: "Resolve one session or an ordered batch through Agent Deck or Thurbox. host overrides current-host detection; otherwise a valid nested Thurbox context wins, then Agent Deck is used when detected.",
	}, s.sessionResolve)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_create",
		Description: "Create a session in an explicit workdir through Agent Deck or Thurbox. launch_profile is a configured logical profile name, not a shell command.",
	}, s.sessionCreate)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_require",
		Description: "Ensure existing sessions in an explicit workdir through Agent Deck or Thurbox; starts a known stopped session when the selected host supports it and never creates one.",
	}, s.sessionRequire)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_resolve_session",
		Description: "Resolve one session ID/ref with session, or independently resolve multiple with sessions; batches return ordered results.",
	}, s.agentDeckResolveSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_create_session",
		Description: "Create a session in an explicit workdir; target must not exist. Supports group placement, parent linkage, detachment, and startup_instruction passed only to agent-deck launch --message.",
	}, s.agentDeckCreateSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_require_session",
		Description: "Ensure one existing session ID/ref or multiple sessions in an explicit workdir. Each must already match that workdir and is started if needed; never creates sessions.",
	}, s.agentDeckRequireSession)
}

func (s *Service) sessionResolve(ctx context.Context, req *mcp.CallToolRequest, input sessionResolveInput) (*mcp.CallToolResult, map[string]any, error) {
	batch, err := validateGenericResolveSessionArgs(req, input)
	if err != nil {
		return nil, nil, err
	}
	host, err := s.sessions.selectSessionHost(ctx, input.Host)
	if err != nil {
		return nil, nil, err
	}
	if !batch {
		out, err := s.genericResolveSessionResult(ctx, host, input.Session)
		if err != nil {
			return nil, nil, err
		}
		return s.toolResult(ctx, out)
	}

	results := make([]map[string]any, 0, len(input.Sessions))
	for _, session := range input.Sessions {
		out, err := s.genericResolveSessionResult(ctx, host, session)
		if err != nil {
			out = map[string]any{
				"host":        string(host),
				"status":      "error",
				"session_ref": session,
				"error":       err.Error(),
			}
		}
		results = append(results, out)
	}
	return s.toolResult(ctx, map[string]any{"host": string(host), "results": results})
}

func (s *Service) genericResolveSessionResult(ctx context.Context, host sessionHost, session string) (map[string]any, error) {
	data, err := s.sessions.resolveHostSession(ctx, host, session, syncCmdTimeout)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{
			"host":        string(host),
			"status":      "not_found",
			"session_ref": session,
		}, nil
	}
	out := hostSessionInfoMap(data, session)
	out["status"] = "found"
	return out, nil
}

func validateGenericResolveSessionArgs(req *mcp.CallToolRequest, input sessionResolveInput) (bool, error) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return false, errors.New("session_resolve requires exactly one of session or sessions")
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
		return false, fmt.Errorf("invalid tool arguments: %w", err)
	}
	_, hasSession := rawArgs["session"]
	_, hasSessions := rawArgs["sessions"]
	if hasSession == hasSessions {
		return false, errors.New("session_resolve requires exactly one of session or sessions")
	}
	if hasSessions && len(input.Sessions) == 0 {
		return false, errors.New("session_resolve sessions must contain at least one session")
	}
	if hasSession && strings.TrimSpace(input.Session) == "" {
		return false, errors.New("session_resolve session must not be empty")
	}
	for _, session := range input.Sessions {
		if strings.TrimSpace(session) == "" {
			return false, errors.New("session_resolve sessions must not contain an empty session")
		}
	}
	return hasSessions, nil
}

func (s *Service) sessionCreate(ctx context.Context, _ *mcp.CallToolRequest, input sessionCreateInput) (*mcp.CallToolResult, map[string]any, error) {
	// A missing configuration must fail before host selection, which may probe
	// Agent Deck. Resolve and require intentionally do not share this gate.
	if s.sessions.sessionHostConfig == nil {
		return nil, nil, errors.New("generic session creation requires session-host configuration")
	}
	host, err := s.sessions.selectSessionHost(ctx, input.Host)
	if err != nil {
		return nil, nil, err
	}
	out, err := s.sessions.createHostSession(ctx, host, input.SessionName, input.Workdir, input.ParentSessionID, input.LaunchProfile)
	if err != nil {
		return nil, nil, err
	}
	return s.toolResult(ctx, out)
}

func (s *Service) sessionRequire(ctx context.Context, req *mcp.CallToolRequest, input sessionRequireInput) (*mcp.CallToolResult, map[string]any, error) {
	batch, err := validateGenericRequireSessionArgs(req, input)
	if err != nil {
		return nil, nil, err
	}
	host, err := s.sessions.selectSessionHost(ctx, input.Host)
	if err != nil {
		return nil, nil, err
	}
	if !batch {
		out, err := s.sessions.requireHostSession(ctx, host, firstNonEmpty(input.SessionID, input.SessionRef), input.Workdir)
		if err != nil {
			return nil, nil, err
		}
		return s.toolResult(ctx, out)
	}

	workdir, err := canonicalizeTargetWorkdir(input.Workdir, "requiring")
	if err != nil {
		return nil, nil, err
	}
	results := make([]map[string]any, 0, len(input.Sessions))
	for _, session := range input.Sessions {
		out, err := s.sessions.requireHostSessionWithCanonicalWorkdir(ctx, host, session, input.Workdir, workdir)
		if err != nil {
			out = genericRequireErrorResult(host, session, err)
		}
		results = append(results, out)
	}
	return s.toolResult(ctx, map[string]any{"host": string(host), "results": results})
}

func validateGenericRequireSessionArgs(req *mcp.CallToolRequest, input sessionRequireInput) (bool, error) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return false, errors.New("session_require requires exactly one of session_id, session_ref, or sessions")
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
		return false, fmt.Errorf("invalid tool arguments: %w", err)
	}
	_, hasSessionID := rawArgs["session_id"]
	_, hasSessionRef := rawArgs["session_ref"]
	_, hasSessions := rawArgs["sessions"]
	count := 0
	for _, present := range []bool{hasSessionID, hasSessionRef, hasSessions} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false, errors.New("session_require requires exactly one of session_id, session_ref, or sessions")
	}
	if hasSessions && len(input.Sessions) == 0 {
		return false, errors.New("session_require sessions must contain at least one session")
	}
	if !hasSessions && strings.TrimSpace(firstNonEmpty(input.SessionID, input.SessionRef)) == "" {
		return false, errors.New("session_require session_id or session_ref must not be empty")
	}
	for _, session := range input.Sessions {
		if strings.TrimSpace(session) == "" {
			return false, errors.New("session_require sessions must not contain an empty session")
		}
	}
	return hasSessions, nil
}

func genericRequireErrorResult(host sessionHost, sessionRef string, err error) map[string]any {
	return map[string]any{
		"host":            string(host),
		"status":          "error",
		"session_ref":     sessionRef,
		"started_session": false,
		"error":           err.Error(),
	}
}

func (s *Service) agentDeckResolveSession(ctx context.Context, req *mcp.CallToolRequest, input agentDeckResolveSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	batch, err := validateResolveSessionArgs(req, input)
	if err != nil {
		return nil, nil, err
	}
	if !batch {
		out, err := s.resolveSessionResult(ctx, input.Session)
		if err != nil {
			return nil, nil, err
		}
		return s.toolResult(ctx, out)
	}

	results := make([]map[string]any, 0, len(input.Sessions))
	for _, session := range input.Sessions {
		out, err := s.resolveSessionResult(ctx, session)
		if err != nil {
			out = map[string]any{
				"status":      "error",
				"session_ref": session,
				"error":       err.Error(),
			}
		}
		results = append(results, out)
	}
	return s.toolResult(ctx, map[string]any{"results": results})
}

func (s *Service) resolveSessionResult(ctx context.Context, session string) (map[string]any, error) {
	data, err := s.sessions.resolveSessionShow(ctx, session, syncCmdTimeout)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return map[string]any{
			"status":      "not_found",
			"session_ref": session,
		}, nil
	}
	out := sessionInfoMap(data, session)
	out["status"] = "found"
	return out, nil
}

func validateResolveSessionArgs(req *mcp.CallToolRequest, input agentDeckResolveSessionInput) (bool, error) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return false, errors.New("agent_deck_resolve_session requires exactly one of session or sessions")
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
		return false, fmt.Errorf("invalid tool arguments: %w", err)
	}
	_, hasSession := rawArgs["session"]
	_, hasSessions := rawArgs["sessions"]
	if hasSession == hasSessions {
		return false, errors.New("agent_deck_resolve_session requires exactly one of session or sessions")
	}
	if hasSessions && len(input.Sessions) == 0 {
		return false, errors.New("agent_deck_resolve_session sessions must contain at least one session")
	}
	return hasSessions, nil
}

func (s *Service) agentDeckCreateSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckCreateSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	out, err := s.sessions.createSession(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return s.toolResult(ctx, out)
}

func (s *Service) agentDeckRequireSession(ctx context.Context, req *mcp.CallToolRequest, input agentDeckRequireSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	batch, err := validateRequireSessionArgs(req, input)
	if err != nil {
		return nil, nil, err
	}
	if batch {
		workdir, err := canonicalizeTargetWorkdir(input.Workdir, "requiring")
		if err != nil {
			return nil, nil, err
		}
		results := make([]map[string]any, 0, len(input.Sessions))
		for _, session := range input.Sessions {
			out, err := s.sessions.requireSessionWithCanonicalWorkdir(ctx, agentDeckRequireSessionInput{
				SessionRef: session,
				Workdir:    input.Workdir,
			}, workdir)
			if err != nil {
				out = map[string]any{
					"status":      "error",
					"session_ref": session,
					"error":       err.Error(),
				}
			}
			results = append(results, out)
		}
		return s.toolResult(ctx, map[string]any{"results": results})
	}
	out, err := s.sessions.requireSession(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return s.toolResult(ctx, out)
}

func validateRequireSessionArgs(req *mcp.CallToolRequest, input agentDeckRequireSessionInput) (bool, error) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return false, nil
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
		return false, fmt.Errorf("invalid tool arguments: %w", err)
	}

	allowedFields := map[string]bool{
		"session_id":  true,
		"session_ref": true,
		"sessions":    true,
		"workdir":     true,
	}
	unexpected := make([]string, 0, len(rawArgs))
	for field := range rawArgs {
		if !allowedFields[field] {
			unexpected = append(unexpected, field)
		}
	}
	if len(unexpected) > 0 {
		slices.Sort(unexpected)
		return false, fmt.Errorf("agent_deck_require_session does not accept extra fields: %s", strings.Join(unexpected, ", "))
	}

	_, hasSessions := rawArgs["sessions"]
	_, hasSessionID := rawArgs["session_id"]
	_, hasSessionRef := rawArgs["session_ref"]
	if !hasSessions {
		return false, nil
	}
	if hasSessionID || hasSessionRef {
		return false, errors.New("agent_deck_require_session sessions cannot be combined with session_id or session_ref")
	}
	if len(input.Sessions) == 0 {
		return false, errors.New("agent_deck_require_session sessions must contain at least one session")
	}
	return true, nil
}
