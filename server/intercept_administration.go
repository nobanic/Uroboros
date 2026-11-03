package server

import (
	"errors"
	"net/http"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
)

// Each operation returns a document; a zero document means an existing helper
// already wrote its response. Storage/cache effects complete before encoding.
type interceptHTTP struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}
type interceptDocument struct {
	status int
	value  any
}

func (document interceptDocument) write(writer http.ResponseWriter) {
	if document.status != 0 {
		writeJSON(writer, document.status, document.value)
	}
}
func interceptFailure(status int, message string) interceptDocument {
	return interceptDocument{status: status, value: map[string]any{"error": message}}
}

type interceptToggleInput = struct {
	Enabled bool `json:"enabled"`
}
type interceptDecisionInput = struct {
	Decision string `json:"decision"`
}
type interceptToolsInput = struct {
	EnabledTools []string `json:"enabled_tools"`
}

// Rule admission keeps DB readiness, update ID, decoder, validation, write and
// cache invalidation in the original order. Create and update bind their mode.
type interceptRuleWrite struct {
	endpoint interceptHTTP
	update   bool
}

func (endpoint interceptHTTP) createRule() interceptDocument {
	return (interceptRuleWrite{endpoint: endpoint}).document()
}
func (endpoint interceptHTTP) updateRule() interceptDocument {
	return (interceptRuleWrite{endpoint: endpoint, update: true}).document()
}
func (change interceptRuleWrite) document() interceptDocument {
	pg := change.endpoint.server.pg(change.endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	var id int64
	if change.update {
		var ok bool
		id, ok = pathInt(change.endpoint.request, "id")
		if !ok {
			return interceptFailure(400, "bad rule id")
		}
	}
	var req interceptRuleReq
	if err := decode(change.endpoint.request, &req); err != nil {
		return interceptFailure(400, err.Error())
	}
	if err := validateInterceptRuleReq(req); err != nil {
		return interceptFailure(400, err.Error())
	}
	var rule db.InterceptRule
	var err error
	if change.update {
		rule, err = pg.UpdateInterceptRule(id, req.Name, req.MatchTarget, req.MatchType, req.Pattern, req.Action, req.Message, req.Priority, req.Enabled, req.TimeoutEnabled, req.TimeoutSeconds, req.TimeoutAction)
	} else {
		rule, err = pg.CreateInterceptRule(req.Name, req.MatchTarget, req.MatchType, req.Pattern, req.Action, req.Message, req.Priority, req.Enabled, req.TimeoutEnabled, req.TimeoutSeconds, req.TimeoutAction)
	}
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	change.endpoint.server.m.interceptor.Invalidate()
	return interceptDocument{status: 200, value: rule}
}

func (endpoint interceptHTTP) rules() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	rules, err := pg.ListInterceptRules()
	if err != nil {
		return interceptFailure(500, err.Error())
	}
	if rules == nil {
		rules = []db.InterceptRule{}
	}
	return interceptDocument{status: 200, value: map[string]any{"rules": rules}}
}
func (endpoint interceptHTTP) deleteRule() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	id, ok := pathInt(endpoint.request, "id")
	if !ok {
		return interceptFailure(400, "bad rule id")
	}
	if err := pg.DeleteInterceptRule(id); err != nil {
		return interceptFailure(500, err.Error())
	}
	endpoint.server.m.interceptor.Invalidate()
	return interceptDocument{status: 200, value: map[string]any{"deleted": id}}
}
func (endpoint interceptHTTP) toggleRule() interceptDocument {
	pg := endpoint.server.pg(endpoint.writer)
	if pg == nil {
		return interceptDocument{}
	}
	id, ok := pathInt(endpoint.request, "id")
	if !ok {
		return interceptFailure(400, "bad rule id")
	}
	var req interceptToggleInput

	if err := decode(endpoint.request, &req); err != nil {
		return interceptFailure(400, err.Error())
	}
	if err := pg.ToggleInterceptRule(id, req.Enabled); err != nil {
		return interceptFailure(500, err.Error())
	}
	endpoint.server.m.interceptor.Invalidate()
	return interceptDocument{status: 200, value: map[string]any{"ok": true, "enabled": req.Enabled}}
}
func (endpoint interceptHTTP) decide() interceptDocument {
	id, ok := pathInt(endpoint.request, "id")
	if !ok {
		return interceptFailure(400, "bad pending id")
	}
	var req interceptDecisionInput
	// "allowed" | "denied"

	if err := decode(endpoint.request, &req); err != nil {
		return interceptFailure(400, err.Error())
	}
	if req.Decision != "allowed" && req.Decision != "denied" {
		return interceptFailure(400, "decision must be allowed or denied")
	}
	if err := endpoint.server.m.interceptor.Decide(id, req.Decision == "allowed"); err != nil {
		if errors.Is(err, intercept.ErrAlreadyDecided) {
			return interceptFailure(409, err.Error())
		}
		return interceptFailure(500, err.Error())
	}
	return interceptDocument{status: 200, value: map[string]any{"ok": true}}
}
