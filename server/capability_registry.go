package server

import "net/http"

// A registry exchange keeps the original server state, request and response
// lifetime together. Controllers encode before their original deferred cleanup.
type capabilityRequest struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}
type capabilityVisibility struct{ exchange capabilityRequest }

// Zero documents mean an existing readiness/identity helper already replied.
// All other commands finish their effects before the HTTP adapter encodes once.
type capabilityDocument struct {
	status int
	value  any
}

func (document capabilityDocument) write(writer http.ResponseWriter) {
	if document.status != 0 {
		writeJSON(writer, document.status, document.value)
	}
}
func capabilityFailure(status int, message string) capabilityDocument {
	return capabilityDocument{status: status, value: map[string]any{"error": message}}
}

func (controller capabilityVisibility) agents() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	id, _ := pathInt(controller.exchange.request, "id")
	agents, err := pg.ResourceAgents(controller.exchange.request.PathValue("kind"), id)
	if err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"agents": idStrings(agents)}}
}

func (controller capabilityVisibility) toggle() capabilityDocument {
	pg := controller.exchange.server.pg(controller.exchange.writer)
	if pg == nil {
		return capabilityDocument{}
	}
	var body struct {
		AgentID    int64  `json:"agent_id,string"`
		Kind       string `json:"kind"`
		ResourceID int64  `json:"resource_id"`
		Visible    bool   `json:"visible"`
	}
	if err := decode(controller.exchange.request, &body); err != nil {
		return capabilityFailure(400, err.Error())
	}
	if err := pg.ToggleVisibility(body.AgentID, body.Kind, body.ResourceID, body.Visible); err != nil {
		return capabilityFailure(500, err.Error())
	}
	return capabilityDocument{status: 200, value: map[string]any{"ok": true}}
}
