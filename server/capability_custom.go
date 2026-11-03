package server

import (
	"context"
	"encoding/json"
	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
	"strings"
	"time"
)

type capabilityCustomRegistry struct{ exchange capabilityRequest }

func (controller capabilityCustomRegistry) create() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	var req customToolReq
	if err := decode(controller.exchange.request, &req); err != nil {
		return capabilityFailure(400, err.Error())
	}
	req.Key = strings.TrimSpace(req.Key)
	if !reToolKey.MatchString(req.Key) {
		return capabilityFailure(400, "key must start with a lowercase letter and contain only lowercase letters/digits/underscores")
	}
	if req.Kind != "command" && req.Kind != "script" && req.Kind != "http" && req.Kind != "shell" {
		return capabilityFailure(400, "kind must be command / script / http / shell")
	}
	if req.Kind == "http" && !hasSchemaProps(req.Schema) {
		return capabilityFailure(400, "http tools must provide a parameter JSON Schema (cannot be empty)")
	}
	if exist, _ := pg.GetTool(req.Key); exist != nil {
		return capabilityFailure(409, "this key already exists (built-in or custom tool)")
	}
	if err := pg.CreateCustomTool(&db.Tool{
		Key: req.Key, Description: req.Description, Schema: req.Schema, Agents: req.Agents,
		Enabled: req.Enabled, Kind: req.Kind, Exec: req.Exec, Deferred: req.Deferred,
	}); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"key": req.Key}}
}

func (controller capabilityCustomRegistry) update() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	key := controller.exchange.request.PathValue("key")
	existing, err := pg.GetTool(key)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	if existing == nil || existing.System {
		return capabilityFailure(400, "only custom tools can be edited")
	}
	var req customToolReq
	if err := decode(controller.exchange.request, &req); err != nil {
		return capabilityFailure(400, err.Error())
	}
	if req.Kind != "command" && req.Kind != "script" && req.Kind != "http" && req.Kind != "shell" {
		return capabilityFailure(400, "kind must be command / script / http / shell")
	}
	if req.Kind == "http" && !hasSchemaProps(req.Schema) {
		return capabilityFailure(400, "http tools must provide a parameter JSON Schema (cannot be empty)")
	}
	if err := pg.UpdateCustomTool(&db.Tool{
		Key: key, Description: req.Description, Schema: req.Schema, Agents: req.Agents,
		Enabled: req.Enabled, Kind: req.Kind, Exec: req.Exec, Deferred: req.Deferred,
	}); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"ok": true}}
}

func (controller capabilityCustomRegistry) remove() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	key := controller.exchange.request.PathValue("key")
	if err := pg.DeleteCustomTool(key); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"deleted": key}}
}

func (controller capabilityCustomRegistry) test() {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return
	}
	var req testToolReq
	if err := json.NewDecoder(controller.exchange.request.Body).Decode(&req); err != nil {
		writeErr(controller.exchange.writer, 400, "invalid request body")
		return
	}
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(controller.exchange.request.Context(), 10*time.Minute)
	defer cancel()
	tc := &actool.ToolContext{WorkingDir: controller.exchange.server.m.dir} // run in the project dir, like a real call
	var res actool.Result
	switch req.Kind {
	case "command":
		res, _ = controller.exchange.server.runCommandTool(ctx, req.Exec, params, tc)
	case "script":
		res, _ = controller.exchange.server.runScriptTool(ctx, "test", req.Exec, params, tc)
	case "http":
		res, _ = controller.exchange.server.runHTTPTool(ctx, req.Exec, params, tc)
	case "shell":
		writeErr(controller.exchange.writer, 400, "a shell-kind tool is a bash environment declaration with nothing executable")
		return
	default:
		writeErr(controller.exchange.writer, 400, "unknown tool kind: "+req.Kind)
		return
	}
	writeJSON(controller.exchange.writer, 200, map[string]any{"output": res.Flatten(), "is_error": res.IsError})
}
