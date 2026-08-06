package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addToolRequiringWaypostStatus[In, Out any](server *mcp.Server, service *Service, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	toolName := tool.Name
	mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		if err := service.requireWaypostStatusCalled(toolName); err != nil {
			return nil, zero, err
		}
		return handler(ctx, req, input)
	})
}

func (s *Service) markWaypostStatusCalled() {
	s.state.mu.Lock()
	s.state.statusToolCalled = true
	s.state.mu.Unlock()
}

func (s *Service) requireWaypostStatusCalled(toolName string) error {
	s.state.mu.Lock()
	called := s.state.statusToolCalled
	s.state.mu.Unlock()
	if called {
		return nil
	}
	return fmt.Errorf("%s cannot run until waypost_status succeeds; call it once to auto-bind addresses and report any binding warnings", toolName)
}
