package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/traffic"
	actool "github.com/nobanic/norma/tool"
)

type trafficSchemaInput struct {
	BaseURL       string            `json:"base_url"`
	ExchangeIDs   []string          `json:"exchange_ids"`
	PathTemplates map[string]string `json:"path_templates,omitempty"`
	IdentityRefs  map[string]string `json:"identity_refs,omitempty"`
	MaxBodyBytes  int64             `json:"max_body_bytes,omitempty"`
}

// ReadEvidence supplies full bodies under the traffic writer lock, including
// blob-backed captures. No traffic_get previews or model-written copies enter
// inference. The lock is released before calling the sidecar.
func readTrafficSchema(ctx context.Context, tr *traffic.Traffic, in trafficSchemaInput) ([]map[string]any, error) {
	base, err := url.Parse(in.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("base_url must be HTTP(S), without credentials, query or fragment")
	}
	if len(in.ExchangeIDs) == 0 {
		return nil, errors.New("select captured exchange_ids using traffic_search")
	}
	if in.MaxBodyBytes == 0 {
		in.MaxBodyBytes = 1 << 20
	}
	if in.MaxBodyBytes < 1 {
		return nil, errors.New("max_body_bytes must be positive")
	}
	seen := map[string]bool{}
	for _, id := range in.ExchangeIDs {
		if id == "" || seen[id] {
			return nil, errors.New("exchange_ids must be nonempty and unique")
		}
		seen[id] = true
	}
	for id := range in.IdentityRefs {
		if !seen[id] {
			return nil, errors.New("identity_refs must refer to selected exchange_ids")
		}
	}
	observations := []map[string]any{}
	err = tr.ReadEvidence(ctx, in.ExchangeIDs, func(e traffic.EvidenceExchange) error {
		u, err := url.Parse(e.URL)
		if err != nil || u.User != nil || !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
			return fmt.Errorf("exchange %s does not use the selected target origin", e.ID)
		}
		q := u.Query()
		for name := range q {
			if schemaSensitiveName(name) {
				q.Del(name)
			}
		}
		u.RawQuery, u.Fragment = q.Encode(), ""
		req, reqHash, reqPresent, err := trafficJSON(e.Request, in.MaxBodyBytes)
		if err != nil {
			return fmt.Errorf("exchange %s request: %w", e.ID, err)
		}
		resp, respHash, respPresent, err := trafficJSON(e.Response, in.MaxBodyBytes)
		if err != nil {
			return fmt.Errorf("exchange %s response: %w", e.ID, err)
		}
		identity := in.IdentityRefs[e.ID]
		if identity == "" {
			identity = "unknown"
		}
		observations = append(observations, map[string]any{
			"exchange_id": e.ID, "timestamp": e.TS, "method": e.Method, "url": u.String(), "status": e.Status,
			"request_json_present": reqPresent, "request_json": req,
			"response_json_present": respPresent, "response_json": resp, "response_content_type": e.ContentType,
			"request_body_sha256": reqHash, "response_body_sha256": respHash,
			"request_head_sha256": schemaBytesHash([]byte(e.ReqHead)), "response_head_sha256": schemaBytesHash([]byte(e.RespHead)),
			"identity_ref": identity,
		})
		return nil
	})
	return observations, err
}

func schemaBytesHash(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func schemaSensitiveName(name string) bool {
	switch strings.ReplaceAll(strings.ToLower(name), "-", "_") {
	case "authorization", "cookie", "set_cookie", "password", "token", "access_token", "refresh_token", "api_key", "apikey", "client_secret", "session", "sessionid", "csrf_token", "auth":
		return true
	}
	return false
}

func redactSchemaJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if schemaSensitiveName(key) {
				v[key] = "[REDACTED]"
			} else {
				v[key] = redactSchemaJSON(item)
			}
		}
	case []any:
		for i, item := range v {
			v[i] = redactSchemaJSON(item)
		}
	}
	return value
}

func trafficJSON(reader io.Reader, maxBytes int64) (any, string, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxBytes))
	if err != nil {
		return nil, "", false, err
	}
	var extra [1]byte
	n, readErr := reader.Read(extra[:])
	if n > 0 {
		return nil, "", false, errors.New("captured body exceeds max_body_bytes; increase it to infer from the complete body")
	}
	if readErr != nil && readErr != io.EOF {
		return nil, "", false, readErr
	}
	hash := schemaBytesHash(raw)
	if !json.Valid(raw) {
		return nil, hash, false, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, "", false, err
	}
	return redactSchemaJSON(value), hash, true, nil
}

func (s *Server) inferTrafficSchema(ctx context.Context, in trafficSchemaInput) (json.RawMessage, error) {
	client, err := s.experimentClient(ctx, "api_lab", "api_lab_import_traffic")
	if err != nil {
		return nil, err
	}
	defer client.Close()
	observations, err := readTrafficSchema(ctx, s.m.Traffic(), in)
	if err != nil {
		return nil, err
	}
	source := map[string]any{"kind": "console_captured_traffic", "task_id": agent.RunInfoFrom(ctx).TaskID,
		"identity_basis": "supplied_references_not_inferred_from_credentials", "path_templates": in.PathTemplates}
	raw, err := client.Call(ctx, "api_lab_import_traffic", map[string]any{
		"base_url": in.BaseURL, "exchanges": observations, "path_templates": in.PathTemplates, "source": source,
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		ID     string `json:"schema_id"`
		Hash   string `json:"hash"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	if result.ID == "" || result.ID != result.Hash {
		return nil, fmt.Errorf("API schema inference did not complete: %s", result.Reason)
	}
	return json.RawMessage(raw), nil
}

func (s *Server) trafficSchemaTool() actool.CoreTool {
	return wrTool("api_schema_from_traffic", "Infer an immutable OpenAPI schema directly from captured HTTP exchanges, without a target OpenAPI endpoint or model-copied evidence. Select exchange_ids from traffic_search for one base_url. Returns schema_id for api_lab_catalog/start and unverified response-to-request dependency candidates. Numeric/UUID paths are generalized; optional path_templates maps observed relative paths to explicit templates. Optional identity_refs maps exchange IDs to known configured identity names; identities and access policy are never guessed from credentials. The schema covers selected observed shapes only; define authorization/business checks separately. Original traffic IDs and body/header hashes are retained; body examples and auth headers are not stored in the schema. max_body_bytes (default 1 MiB per side) can be raised for complete large captures. No requests are made against the target.",
		objSchema(map[string]any{
			"base_url":       strParam("selected target origin and optional base path"),
			"exchange_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"path_templates": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"identity_refs":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"max_body_bytes": map[string]any{"type": "integer", "default": 1 << 20},
		}, "base_url", "exchange_ids"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			return toolinput.Decode(raw, func(input trafficSchemaInput) (actool.Result, error) {
				result, err := s.inferTrafficSchema(ctx, input)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(string(result)), nil

			})
		})
}

func (s *Server) createTrafficSchema(w http.ResponseWriter, r *http.Request) {
	var input trafficSchemaInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	result, err := s.inferTrafficSchema(r.Context(), input)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) seedTrafficSchemaTool() error {
	tool := s.trafficSchemaTool()
	schema, err := json.Marshal(tool.InputSchema())
	if err != nil {
		return err
	}
	bindings, _ := json.Marshal([]string{"goals", "planner", "mainagent", "worker", "auto", "pentest", "reporter"})
	return s.m.pg.SeedTool(tool.Name(), tool.Description(), schema, bindings)
}
