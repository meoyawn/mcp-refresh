package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serve(ctx context.Context, svc *service) error {
	return newMCPServer(svc).Run(ctx, &mcp.StdioTransport{})
}

func newMCPServer(svc *service) *mcp.Server {
	destructive := false
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-refresh", Version: version}, &mcp.ServerOptions{
		Instructions: "When asked to refresh an MCP, call refresh with its name. The stored local ChatGPT browser session handles authentication. Use list to discover exact development MCP names or link IDs. Never ask the caller for cookies or access tokens.",
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list", Description: "List the user's private development MCP connections in ChatGPT using the stored browser session.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOutput, error) {
			links, err := svc.list(ctx)
			return nil, listOutput{MCPs: links}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "refresh", Description: "Refresh a development MCP's tools in ChatGPT. Supply only its exact name (case insensitive), link ID, or connector ID; authentication uses the stored local browser session.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input refreshInput) (*mcp.CallToolResult, refreshResult, error) {
			result, err := svc.refresh(ctx, input.MCP)
			return nil, result, err
		})
	return server
}

type listOutput struct {
	MCPs []link `json:"mcps"`
}
type refreshInput struct {
	MCP string `json:"mcp" jsonschema:"Exact development MCP name, link ID, or connector ID to refresh"`
}
