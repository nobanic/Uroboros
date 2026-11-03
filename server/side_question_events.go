package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Reconnect sends the current cumulative snapshot even when a client supplies
// Last-Event-ID. Its poll/heartbeat and terminal policy belong to this response.
func (endpoint sideQuestionHTTP) events() {
	flusher, ok := endpoint.writer.(http.Flusher)
	if !ok {
		writeErr(endpoint.writer, 500, "streaming unavailable")
		return
	}
	id := endpoint.request.PathValue("requestID")
	first, err := endpoint.server.m.pg.SideRequest(endpoint.request.Context(), id)
	if err != nil {
		writeErr(endpoint.writer, 500, err.Error())
		return
	}
	if first == nil {
		writeErr(endpoint.writer, 404, "side question not found")
		return
	}
	endpoint.writer.Header().Set("Content-Type", "text/event-stream")
	endpoint.writer.Header().Set("Cache-Control", "no-cache")
	endpoint.writer.Header().Set("X-Accel-Buffering", "no")
	seq := int64(-1)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		e := first
		first = nil
		if e == nil {
			e, err = endpoint.server.m.pg.SideRequest(endpoint.request.Context(), id)
		}
		if err != nil {
			return
		}
		if e == nil {
			fmt.Fprint(endpoint.writer, "event: cleared\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		if e.Sequence > seq {
			b, _ := json.Marshal(e)
			fmt.Fprintf(endpoint.writer, "id: %d\nevent: snapshot\ndata: %s\n\n", e.Sequence, b)
			flusher.Flush()
			seq = e.Sequence
		}
		if !e.Running() {
			return
		}
		select {
		case <-endpoint.request.Context().Done():
			return
		case <-endpoint.server.ctx.Done():
			return
		case <-tick.C:
		case <-heartbeat.C:
			fmt.Fprint(endpoint.writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
