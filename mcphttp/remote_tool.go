package mcphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// Anonymous aliases retain encoding/json's established error type spelling.
type remoteToolCatalog = struct {
	Tools []remoteTool `json:"tools"`
}
type remoteToolResult = struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func remoteResultText(reply remoteToolResult) string {
	var text strings.Builder
	for _, block := range reply.Content {
		text.WriteString(block.Text)
	}
	return text.String()
}
func discoverRemoteTools(ctx context.Context, client *Client) ([]actool.CoreTool, error) {
	raw, err := client.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var catalog remoteToolCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	tools := make([]actool.CoreTool, 0, len(catalog.Tools))
	for _, descriptor := range catalog.Tools {
		tools = append(tools, bindRemoteTool(client, descriptor))
	}
	return tools, nil
}

type remoteToolBinding struct {
	client     *Client
	descriptor remoteTool
	name       string
}

func bindRemoteTool(client *Client, descriptor remoteTool) actool.CoreTool {
	binding := remoteToolBinding{client: client, descriptor: descriptor, name: ToolName(client.server, descriptor.Name)}
	schema := descriptor.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return actool.Build(actool.Spec{Name: binding.name, Description: descriptor.Description, Schema: schema,
		Permissions: binding.permissions, Run: binding.run})
}
func (binding remoteToolBinding) permissions(context.Context, json.RawMessage, permission.Context) permission.Decision {
	return permission.AskUser("call MCP tool " + binding.name + "?")
}
func (binding remoteToolBinding) run(ctx context.Context, input json.RawMessage, toolContext *actool.ToolContext) (actool.Result, error) {
	var arguments any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &arguments)
	}
	raw, err := binding.client.call(ctx, "tools/call", map[string]any{"name": binding.descriptor.Name, "arguments": arguments})
	if err != nil {
		return actool.Errorf("Error: " + err.Error()), nil
	}
	var reply remoteToolResult
	if err := json.Unmarshal(raw, &reply); err != nil {
		return actool.Errorf("Error: bad MCP response: " + err.Error()), nil
	}
	text := remoteResultText(reply)
	return actool.Result{Content: []llm.ContentBlock{llm.TextBlock(actool.Capture(toolContext, text))}, IsError: reply.IsError}, nil
}
func callRemoteTool(ctx context.Context, client *Client, name string, arguments any) (string, error) {
	raw, err := client.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return "", err
	}
	var reply remoteToolResult
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("bad MCP response: %w", err)
	}
	text := remoteResultText(reply)
	if reply.IsError {
		return text, fmt.Errorf("mcp tool %q error: %s", name, text)
	}
	return text, nil
}
