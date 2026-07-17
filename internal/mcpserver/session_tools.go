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
	SessionID  string `json:"session_id,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
	Workdir    string `json:"workdir"`
}

func (s *Service) registerSessionTools(server *mcp.Server) {
	addToolRequiringMailboxStatus(server, s, &mcp.Tool{
		Name:        "agent_deck_resolve_session",
		Description: "Resolve one agent-deck session ref or id with session, or resolve multiple independently with sessions. Single-session responses remain unchanged; batch responses return ordered per-session results.",
	}, s.agentDeckResolveSession)
	addToolRequiringMailboxStatus(server, s, &mcp.Tool{
		Name:        "agent_deck_create_session",
		Description: "Create a new agent-deck session in an explicit workdir. The target must not already exist. Supports explicit group placement, parent linkage, detached sessions, and optional startup_instruction passed only to agent-deck launch --message.",
	}, s.agentDeckCreateSession)
	addToolRequiringMailboxStatus(server, s, &mcp.Tool{
		Name:        "agent_deck_require_session",
		Description: "Require an existing agent-deck session in an explicit workdir. Resolves session_id or session_ref, verifies the existing session already matches the requested workdir, and starts it if it is inactive. Does not create sessions.",
	}, s.agentDeckRequireSession)
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
	if err := validateRequireSessionArgs(req); err != nil {
		return nil, nil, err
	}
	out, err := s.sessions.requireSession(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return s.toolResult(ctx, out)
}

func validateRequireSessionArgs(req *mcp.CallToolRequest) error {
	if req == nil || len(req.Params.Arguments) == 0 {
		return nil
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}

	allowedFields := map[string]bool{
		"session_id":  true,
		"session_ref": true,
		"workdir":     true,
	}
	unexpected := make([]string, 0, len(rawArgs))
	for field := range rawArgs {
		if !allowedFields[field] {
			unexpected = append(unexpected, field)
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	slices.Sort(unexpected)

	return fmt.Errorf("agent_deck_require_session does not accept extra fields: %s", strings.Join(unexpected, ", "))
}
