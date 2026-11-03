package traffic

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// Each catalog invocation builds fresh schema values. The zero-receiver seed
// path only describes commands; storage is accessed when a bound command runs.
type trafficParameter struct {
	key, kind, description string
	required               bool
	choices                []string
	itemKind               string
}

func bindTrafficCommand[Input any](name, description string, fields []trafficParameter, strictJSON bool, execute func(Input) (actool.Result, error)) actool.CoreTool {
	properties := make(map[string]any, len(fields))
	required := make([]any, 0)
	for _, field := range fields {
		shape := map[string]any{"type": field.kind, "description": field.description}
		if field.choices != nil {
			shape["enum"] = field.choices
		}
		if field.itemKind != "" {
			shape["items"] = map[string]any{"type": field.itemKind}
		}
		properties[field.key] = shape
		if field.required {
			required = append(required, field.key)
		}
	}
	return actool.Build(actool.Spec{
		Name: name, Description: description,
		Schema:   map[string]any{"type": "object", "properties": properties, "required": required},
		ReadOnly: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(_ context.Context, raw json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var input Input
			if err := json.Unmarshal(raw, &input); err != nil && strictJSON {
				return actool.Errorf("invalid " + name + " input: " + err.Error()), nil
			}
			output, err := execute(input)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return output, nil
		},
	})
}

// Aliases retain the anonymous input shape used by encoding/json diagnostics.
// Search/get/blob intentionally keep their partial-decode compatibility;
// match/routes/worklist report decoding failures before accessing storage.
type trafficSearchInput = struct {
	Host, Contains string
	BodyContains   string `json:"body_contains"`
	Limit, Page    int
}
type trafficGetInput = struct{ ID string }
type trafficBlobInput = struct {
	Hash           string
	Offset, Length int64
}
type trafficMatchInput = struct {
	ID            string `json:"id"`
	Pattern       string `json:"pattern"`
	Side          string `json:"side"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	ContextBytes  int    `json:"context_bytes"`
	MaxMatches    int    `json:"max_matches"`
}
type trafficRoutesInput = struct {
	ID            string   `json:"id"`
	Prefixes      []string `json:"prefixes"`
	ContextBytes  int      `json:"context_bytes"`
	MaxCandidates int      `json:"max_candidates"`
}
type trafficContractInput = struct {
	Host          string   `json:"host"`
	Patterns      []string `json:"patterns"`
	Regex         bool     `json:"regex"`
	CaseSensitive bool     `json:"case_sensitive"`
	ContextBytes  int      `json:"context_bytes"`
	MaxDocuments  int      `json:"max_documents"`
	MaxTotalBytes int      `json:"max_total_bytes"`
	MaxMatches    int      `json:"max_matches"`
}

func nativeTrafficJSON(value any) (actool.Result, error) {
	encoded, _ := json.Marshal(value)
	return actool.Text(string(encoded)), nil
}

func (t *Traffic) executeTrafficSearch(input trafficSearchInput) (actool.Result, error) {
	if strings.TrimSpace(input.Host) == "" {
		return actool.Result{}, errors.New("host is a required parameter: specify a bare host, host:port, or a full URL to avoid a full-database scan.")
	}
	captures, err := t.query(input.Host, input.Contains, input.BodyContains, input.Page, input.Limit)
	if err != nil {
		return actool.Result{}, err
	}
	if len(captures) == 0 {
		return actool.Text("No matching traffic."), nil
	}
	type location struct {
		ID      string `json:"id"`
		Method  string `json:"method"`
		URL     string `json:"url"`
		Status  int    `json:"status"`
		RespLen int    `json:"resp_len"`
	}
	index := make([]location, 0, len(captures))
	for _, capture := range captures {
		index = append(index, location{capture.ID, capture.Method, capture.URL, capture.Status, capture.RespLen})
	}
	return nativeTrafficJSON(index)
}

func (t *Traffic) executeTrafficGet(input trafficGetInput) (actool.Result, error) {
	request, response, err := t.Get(input.ID)
	if err != nil {
		return actool.Result{}, err
	}
	return actool.Text("=== REQUEST ===\n" + clip(request, 2500) + "\n\n=== RESPONSE ===\n" + clip(response, 4000)), nil
}

func (t *Traffic) executeTrafficBlob(input trafficBlobInput) (actool.Result, error) {
	length := input.Length
	if length <= 0 || length > maxBlobRead {
		length = maxBlobRead
	}
	data, total, err := t.BlobRange(input.Hash, input.Offset, length)
	if err != nil {
		return actool.Result{}, err
	}
	if len(data) == 0 {
		return actool.Text(fmt.Sprintf("Offset %d is past the end of the content (total length %d bytes).", input.Offset, total)), nil
	}
	prefix := fmt.Sprintf("[offset=%d this=%d total=%d]\n", input.Offset, len(data), total)
	if isBinaryBody("", data) {
		return actool.Text(prefix + "Binary content; showing the first 512 bytes as hexadecimal:\n" + hex.EncodeToString(clipBytes(data, 512))), nil
	}
	return actool.Text(prefix + truncateUTF8(data, len(data))), nil
}

func (t *Traffic) executeTrafficMatch(input trafficMatchInput) (actool.Result, error) {
	result, err := t.SearchBodyMatches(input.ID, input.Side, input.Pattern, input.Regex, input.CaseSensitive, input.ContextBytes, input.MaxMatches)
	if err != nil {
		return actool.Result{}, err
	}
	return nativeTrafficJSON(result)
}

func (t *Traffic) executeTrafficRoutes(input trafficRoutesInput) (actool.Result, error) {
	result, err := t.ExtractRouteCandidates(input.ID, input.Prefixes, input.ContextBytes, input.MaxCandidates)
	if err != nil {
		return actool.Result{}, err
	}
	return nativeTrafficJSON(result)
}

func (t *Traffic) executeTrafficContract(input trafficContractInput) (actool.Result, error) {
	result, err := t.BuildContractWorklist(input.Host, input.Patterns, input.Regex, input.CaseSensitive, input.ContextBytes, input.MaxDocuments, input.MaxTotalBytes, input.MaxMatches)
	if err != nil {
		return actool.Result{}, err
	}
	return nativeTrafficJSON(result)
}
