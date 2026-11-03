package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// A program borrows its execution JSON at catalog assembly, just as the original
// callable closure did. Interpreter settings and session state are resolved at
// invocation time; changing a row cannot retarget an already assembled callable.
type customToolProgram struct {
	host       *Server
	name, kind string
	source     json.RawMessage
}

func (program customToolProgram) tool(description string, schema map[string]any) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: program.name, Description: description, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: program.invoke,
	})
}

func (program customToolProgram) invoke(ctx context.Context, raw json.RawMessage, session *actool.ToolContext) (actool.Result, error) {
	var parameters map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parameters)
	}
	if parameters == nil {
		parameters = map[string]any{}
	}
	call := customToolInvocation{program.host, program.name, ctx, parameters, session}
	switch program.kind {
	case "command":
		return executeCustomProgram[customCommandProgram](call, program.source)
	case "script":
		return executeCustomProgram[customPythonProgram](call, program.source)
	case "http":
		return executeCustomProgram[customHTTPProgram](call, program.source)
	default:
		return actool.Errorf("unknown custom tool kind: " + program.kind), nil
	}
}

func (host *Server) customProgram(row *db.Tool) actool.CoreTool {
	// Preserve evaluation order: schema first, then the borrowed execution snapshot.
	schema := ensureSchema(row.Schema)
	program := customToolProgram{host, row.Key, row.Kind, row.Exec}
	return program.tool(row.Description, schema)
}

// Both the editor dry-run and installed callable enter this invocation boundary.
// It owns only caller state, never a credential/provider cache or task authority.
type customToolInvocation struct {
	host       *Server
	name       string
	context    context.Context
	parameters map[string]any
	session    *actool.ToolContext
}

type customProgramOperation interface {
	execute(customToolInvocation) (actool.Result, error)
}

func executeCustomProgram[Operation customProgramOperation](call customToolInvocation, raw json.RawMessage) (actool.Result, error) {
	var operation Operation
	// Historic specs tolerate malformed/type-mismatched JSON and retain partial
	// fields populated by encoding/json; validation belongs to each protocol.
	_ = json.Unmarshal(raw, &operation)
	return operation.execute(call)
}

func (call customToolInvocation) render(source string, quote func(string) string) string {
	return renderTemplate(source, call.parameters, quote)
}

func (call customToolInvocation) deadline(milliseconds, fallback int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(call.context, timeoutOr(milliseconds, fallback))
}

func (call customToolInvocation) captured(output string, failure error) (actool.Result, error) {
	if failure != nil {
		return actool.Errorf(failure.Error()), nil
	}
	return actool.Text(actool.Capture(call.session, output)), nil
}

func (call customToolInvocation) location() (string, []string) {
	directory := call.host.m.dir
	if call.session == nil {
		return directory, nil
	}
	if call.session.WorkingDir != "" {
		directory = call.session.WorkingDir
	}
	return directory, call.session.Env
}

// Named spec views retain the original wire fields/tags without a second schema.
type customCommandProgram commandExec
type customPythonProgram scriptExec
type customHTTPProgram httpExec

func (program customCommandProgram) execute(call customToolInvocation) (actool.Result, error) {
	if strings.TrimSpace(program.Command) == "" {
		return actool.Errorf("command is empty"), nil
	}
	input, _ := json.Marshal(map[string]any{"command": call.render(program.Command, shellQuote)})
	ctx := call.context
	if program.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = call.deadline(program.TimeoutMs, 120000)
		defer cancel()
	}
	// Keep the SDK Bash implementation, task manager, safety floor, environment,
	// capture and its own independent timeout/background policy authoritative.
	return actool.NewBash().Call(ctx, input, call.session)
}

func (program customPythonProgram) execute(call customToolInvocation) (actool.Result, error) {
	if strings.TrimSpace(program.Code) == "" {
		return actool.Errorf("script code is empty"), nil
	}
	interpreter := call.host.pythonInterpreter()
	if interpreter == "" {
		return actool.Errorf("no python interpreter configured or detected (set it in system settings)"), nil
	}
	directory, environment := call.location()
	output, failure := execPython(call.context, interpreter, call.name, program.Code, call.parameters, directory, environment, timeoutOr(program.TimeoutMs, 120000))
	return call.captured(output, failure)
}

func (program customHTTPProgram) execute(call customToolInvocation) (actool.Result, error) {
	method := strings.ToUpper(strings.TrimSpace(program.Method))
	if method == "" {
		method = "GET"
	}
	endpoint := call.render(program.URL, identity)
	if strings.TrimSpace(endpoint) == "" {
		return actool.Errorf("http url is empty"), nil
	}
	var body io.Reader
	if program.Body != "" {
		body = strings.NewReader(call.render(program.Body, identity))
	}
	ctx, cancel := call.deadline(program.TimeoutMs, 30000)
	defer cancel()
	request, failure := http.NewRequestWithContext(ctx, method, endpoint, body)
	if failure != nil {
		return actool.Errorf(failure.Error()), nil
	}
	for name, value := range program.Headers {
		request.Header.Set(name, call.render(value, identity))
	}
	client := &http.Client{Timeout: timeoutOr(program.TimeoutMs, 30000)}
	if transport := call.host.httpProxyTransport(httpExec(program)); transport != nil {
		client.Transport = transport
	}
	response, failure := client.Do(request)
	if failure != nil {
		return actool.Errorf("request failed: " + failure.Error()), nil
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	encoded, _ := json.Marshal(map[string]any{"status": response.StatusCode, "body": string(payload)})
	return call.captured(string(encoded), nil)
}
