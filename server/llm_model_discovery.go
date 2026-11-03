package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type modelDiscoveryEndpoint struct {
	url     string
	headers http.Header
}

func modelDiscoveryEndpoints(provider, base, key string) []modelDiscoveryEndpoint {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	endpoint := func(address string, anthropic bool) modelDiscoveryEndpoint {
		headers := make(http.Header)
		if anthropic {
			headers.Set("x-api-key", key)
			headers.Set("anthropic-version", "2023-06-01")
		} else {
			headers.Set("Authorization", "Bearer "+key)
		}
		return modelDiscoveryEndpoint{url: address, headers: headers}
	}
	switch strings.TrimSpace(provider) {
	case "openai", "openai-responses":
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		base = strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(base, "/chat/completions"), "/responses"), "/")
		return []modelDiscoveryEndpoint{endpoint(base+"/models", false)}
	default:
		if base == "" {
			base = "https://api.anthropic.com"
		}
		base = strings.TrimRight(strings.TrimSuffix(base, "/v1/messages"), "/")
		endpoints := []modelDiscoveryEndpoint{endpoint(base+"/v1/models", true)}
		if stripped := strings.TrimRight(strings.TrimSuffix(base, "/anthropic"), "/"); stripped != base {
			endpoints = append(endpoints, endpoint(stripped+"/models", false), endpoint(stripped+"/v1/models", false))
		}
		return endpoints
	}
}

type modelDiscoveryProbe struct {
	client  *http.Client
	lastErr string
	emptyOK bool
}

func (probe *modelDiscoveryProbe) read(ctx context.Context, endpoint modelDiscoveryEndpoint) []string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.url, nil)
	if err != nil {
		probe.lastErr = "failed to build request: " + err.Error()
		return nil
	}
	request.Header = endpoint.headers
	response, err := probe.client.Do(request)
	if err != nil {
		probe.lastErr = "request failed: " + err.Error()
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		probe.lastErr = fmt.Sprintf("API returned %d: %s", response.StatusCode, string(body[:min(len(body), 512)]))
		return nil
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		probe.lastErr = "failed to parse response: " + err.Error()
		return nil
	}
	models := make([]string, 0, len(catalog.Data))
	for _, entry := range catalog.Data {
		if entry.ID != "" {
			models = append(models, entry.ID)
		}
	}
	if len(models) == 0 {
		probe.emptyOK = true
	}
	return models
}

func discoverModelCatalog(ctx context.Context, form modelDiscoveryInput, key string) map[string]any {
	transport := &http.Transport{}
	if proxy := strings.TrimSpace(form.Proxy); proxy != "" {
		if parsed, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	probe := modelDiscoveryProbe{client: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
	for _, endpoint := range modelDiscoveryEndpoints(form.Provider, form.BaseURL, key) {
		if models := probe.read(ctx, endpoint); len(models) > 0 {
			return map[string]any{"ok": true, "models": models}
		}
	}
	if probe.emptyOK {
		return map[string]any{"ok": true, "models": []string{}}
	}
	if probe.lastErr == "" {
		probe.lastErr = "did not obtain the model list"
	}
	return map[string]any{"ok": false, "error": probe.lastErr}
}

func (admin llmAdministration) models() {
	var form modelDiscoveryInput
	if !admin.input(&form) {
		return
	}
	key := strings.TrimSpace(form.APIKey)
	if key == "" {
		if profile := admin.storedProfile(form.ProfileID); profile != nil {
			key = profile.APIKey
		}
	}
	if key == "" {
		admin.reply(map[string]any{"ok": false, "error": "No API Key provided"})
		return
	}
	admin.reply(discoverModelCatalog(admin.request.Context(), form, key))
}
