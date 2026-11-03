package mcphttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// An exchange borrows the existing HTTP client and session locks for one RPC.
// The constructor, ID allocation, cancellation and legacy transport retain
// their original owners; this layer never retries or acquires tool authority.
type streamableExchange struct {
	client *Client
	frame  rpcRequest
	expect bool
}

func (exchange streamableExchange) request(ctx context.Context) (*http.Request, string, error) {
	encoded, err := json.Marshal(exchange.frame)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, exchange.client.url, bytes.NewReader(encoded))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	version := exchange.client.protocol
	if version == "" {
		version = protocolVersion
	}
	request.Header.Set("MCP-Protocol-Version", version)
	for key, value := range exchange.client.headers {
		request.Header.Set(key, value)
	}
	exchange.client.mu.Lock()
	session := exchange.client.sessionID
	exchange.client.mu.Unlock()
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	return request, session, nil
}
func (exchange streamableExchange) rememberSession(response *http.Response, initial string) {
	if initial != "" {
		return
	}
	if assigned := response.Header.Get("Mcp-Session-Id"); assigned != "" {
		exchange.client.mu.Lock()
		exchange.client.sessionID = assigned
		exchange.client.mu.Unlock()
	}
}
func (exchange streamableExchange) decode(response *http.Response) (*rpcResponse, error) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http %d: %s", response.StatusCode, readSnippet(response.Body))
	}
	if !exchange.expect {
		return nil, nil
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return readRPCEventStream(response.Body, exchange.frame.ID)
	}
	var reply rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("mcp: decode json response: %w", err)
	}
	// JSON replies retain their original ID behavior; SSE selects a matching ID.
	return &reply, nil
}
func (exchange streamableExchange) perform(ctx context.Context) (*rpcResponse, error) {
	request, session, err := exchange.request(ctx)
	if err != nil {
		return nil, err
	}
	response, err := exchange.client.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	exchange.rememberSession(response, session)
	return exchange.decode(response)
}
