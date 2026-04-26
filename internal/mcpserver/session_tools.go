package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentDeckResolveSessionInput struct {
	Session string `json:"session"`
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
	SessionID          string `json:"session_id,omitempty"`
	SessionRef         string `json:"session_ref,omitempty"`
	Workdir            string `json:"workdir"`
	StartupInstruction string `json:"startup_instruction,omitempty"`
}

func (s *Service) registerSessionTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_resolve_session",
		Description: "Resolve an agent-deck session ref or id and return its canonical session id, status, and mailbox addresses.",
	}, s.agentDeckResolveSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_create_session",
		Description: "Create a new agent-deck session in an explicit workdir. The target must not already exist. Accepts create-only lifecycle parameters such as ensure_title, ensure_cmd, parent_session_id, group_path, group_parent_session_id, child_group_name, no_parent_link, and startup_instruction. Optional startup_instruction is passed only to agent-deck launch --message; do not use it for task payloads or normal wakeups.",
	}, s.agentDeckCreateSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_require_session",
		Description: "Require an existing agent-deck session in an explicit workdir. Resolves session_id or session_ref, verifies the existing session already matches the requested workdir, and starts it if it is inactive. Optional startup_instruction is passed only when an inactive session must be started. Does not create sessions or accept create-only lifecycle parameters.",
	}, s.agentDeckRequireSession)
}

func (s *Service) agentDeckResolveSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckResolveSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	data, err := s.sessions.resolveSessionShow(ctx, input.Session, syncCmdTimeout)
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		return s.toolResult(ctx, map[string]any{
			"status":      "not_found",
			"session_ref": input.Session,
		})
	}
	out := sessionInfoMap(data, input.Session)
	out["status"] = "found"
	return s.toolResult(ctx, out)
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
		"session_id":          true,
		"session_ref":         true,
		"workdir":             true,
		"startup_instruction": true,
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
