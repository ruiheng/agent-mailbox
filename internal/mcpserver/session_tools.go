package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentDeckResolveSessionInput struct {
	Session string `json:"session"`
}

type agentDeckEnsureSessionInput struct {
	SessionID       string `json:"session_id,omitempty"`
	SessionRef      string `json:"session_ref,omitempty"`
	EnsureTitle     string `json:"ensure_title,omitempty"`
	EnsureCmd       string `json:"ensure_cmd,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	ListenerMessage string `json:"listener_message,omitempty"`
}

func (s *Service) registerSessionTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_resolve_session",
		Description: "Resolve an agent-deck session ref or id and return its canonical session id, status, and mailbox addresses.",
	}, s.agentDeckResolveSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "agent_deck_ensure_session",
		Description: "Resolve or create an agent-deck session. If the target exists but is not active, start it; if it is already active, return notify_needed=true.",
	}, s.agentDeckEnsureSession)
}

func (s *Service) agentDeckResolveSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckResolveSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	data, err := s.sessions.resolveSessionShow(ctx, input.Session, syncCmdTimeout)
	if err != nil {
		return nil, nil, err
	}
	if data == nil {
		return nil, map[string]any{
			"status":      "not_found",
			"session_ref": input.Session,
		}, nil
	}
	out := sessionInfoMap(data, input.Session)
	out["status"] = "found"
	return nil, out, nil
}

func (s *Service) agentDeckEnsureSession(ctx context.Context, _ *mcp.CallToolRequest, input agentDeckEnsureSessionInput) (*mcp.CallToolResult, map[string]any, error) {
	out, err := s.sessions.ensureSession(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}
