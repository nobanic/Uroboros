package server

import (
	"context"
	"encoding/json"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
	"strconv"
	"strings"
)

type toolResult struct{ value any }

func (result toolResult) json() (actool.Result, error) {
	b, err := json.Marshal(result.value)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

type stringArgument struct{ description string }

func (argument stringArgument) schema() map[string]any {
	return map[string]any{"type": "string", "description": argument.description}
}

type profileArgument struct{ raw json.RawMessage }

func (argument profileArgument) id() int64 {
	if len(argument.raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(argument.raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(argument.raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

type objectArguments struct {
	properties map[string]any
	required   []string
}

func (object objectArguments) schema() map[string]any {
	// Some OpenAI-compatible providers reject JSON Schema properties:null even
	// though Go's nil map is convenient for an argument-free tool. Emit the
	// canonical empty object so the whole tool catalog remains admissible.
	if object.properties == nil {
		object.properties = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": object.properties}
	if len(object.required) > 0 {
		req := make([]any, len(object.required))
		for i, r := range object.required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

// orchestrationTool owns the same document/callback and specializes only the
// two fixed read-only flags; permission and execution policies are shared.
type orchestrationTool struct {
	name, description string
	schema            map[string]any
	run               func(context.Context, json.RawMessage) (actool.Result, error)
}

func (tool orchestrationTool) build(readOnly bool) actool.CoreTool {
	specification := actool.Spec{Name: tool.name, Description: tool.description, Schema: tool.schema, Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}, Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
		return tool.run(ctx, in)
	}}
	if readOnly {
		specification.ReadOnly = func(json.RawMessage) bool {
			return true
		}
		specification.Concurrent = func(json.RawMessage) bool {
			return true
		}
	}
	return actool.Build(specification)
}
