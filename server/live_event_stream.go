package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nobanic/UroborOS/db"
)

// eventTail owns one response's live cursor and delivery lifetime. Replays own
// their source-specific batching; only admitted live events advance this cursor.
type eventTail[T any] struct {
	writer   http.ResponseWriter
	flush    http.Flusher
	cursor   int64
	sequence func(T) int64
	accept   func(T) bool
	send     func(T)
}

func eventStreamHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
}

func (tail *eventTail[T]) follow(ctx context.Context, events <-chan T) {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			id := tail.sequence(event)
			if id <= tail.cursor || tail.accept != nil && !tail.accept(event) {
				continue
			}
			tail.cursor = id
			tail.send(event)
		case <-ping.C:
			fmt.Fprint(tail.writer, ": ping\n\n")
			tail.flush.Flush()
		}
	}
}

type backendLogStream struct {
	writer  http.ResponseWriter
	request *http.Request
}

func (stream backendLogStream) serve() {
	flush, supported := stream.writer.(http.Flusher)
	if !supported {
		writeErr(stream.writer, 500, "streaming unsupported")
		return
	}
	tail := eventTail[LogLine]{writer: stream.writer, flush: flush, cursor: int64(atoiDefault(stream.request.URL.Query().Get("since"), 0)), sequence: func(line LogLine) int64 { return line.Seq }}
	eventStreamHeaders(stream.writer)
	events, release := logSink.subscribe()
	defer release()
	tail.send = func(line LogLine) {
		encoded, _ := json.Marshal(line)
		fmt.Fprintf(stream.writer, "data: %s\n\n", encoded)
		flush.Flush()
	}
	lines, cursor := logSink.recent(tail.cursor, 1000)
	for _, line := range lines {
		tail.send(line)
	}
	if cursor > tail.cursor {
		tail.cursor = cursor
	}
	flush.Flush()
	tail.follow(stream.request.Context(), events)
}

type taskActivityStream struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

func (stream taskActivityStream) serve() {
	task := stream.server.m.ResolveTask(stream.request.URL.Query().Get("task"))
	if task == nil {
		writeErr(stream.writer, 404, "task not found")
		return
	}
	flush, supported := stream.writer.(http.Flusher)
	if !supported {
		writeErr(stream.writer, 500, "streaming unsupported")
		return
	}
	var intent *int64
	if raw := stream.request.URL.Query().Get("intent"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			intent = &parsed
		}
	}
	tail := eventTail[db.Activity]{writer: stream.writer, flush: flush, cursor: int64(atoiDefault(stream.request.URL.Query().Get("since"), 0)), sequence: func(event db.Activity) int64 { return event.ID }}
	// Automatic reconnect carries the last received id; invalid headers leave
	// the manual/history cursor intact, including the legacy negative-id behavior.
	if raw := stream.request.Header.Get("Last-Event-ID"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			tail.cursor = parsed
		}
	}
	eventStreamHeaders(stream.writer)
	events, release := stream.server.engine.Broadcaster().Subscribe(task.ID)
	defer release()
	tail.accept = func(event db.Activity) bool { return intent == nil || event.NodeID != nil && *event.NodeID == *intent }
	tail.send = func(event db.Activity) {
		encoded, _ := json.Marshal(activityDTO(event))
		fmt.Fprintf(stream.writer, "id: %d\ndata: %s\n\n", event.ID, encoded)
		flush.Flush()
	}
	if !stream.replay(task, intent, &tail) {
		return
	}
	flush.Flush()
	tail.follow(stream.request.Context(), events)
}

func (stream taskActivityStream) replay(task *Task, intent *int64, tail *eventTail[db.Activity]) bool {
	// The DB is authoritative. Subscribe before this loop, compensate every
	// backlog batch, and let the live cursor drop the buffered replay overlap.
	const batch = 500
	for {
		events, cursor, err := task.Store.ActivityList(intent, tail.cursor, batch)
		if err != nil {
			log.Printf("[activity/stream] task=%s replay since=%d: %v", task.ID, tail.cursor, err)
			return false
		}
		for _, event := range events {
			tail.send(event)
		}
		if cursor > tail.cursor {
			tail.cursor = cursor
		}
		if len(events) < batch {
			return true
		}
	}
}
