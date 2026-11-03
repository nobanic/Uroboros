package llmrec

import (
	"context"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/transcript"
)

// callObservation owns eager call identity and optional wire capture. Provider
// execution remains delegated, while each iteration gets its own response state.
type callObservation struct {
	recorder      *Recorder
	ctx           context.Context
	request       llm.CompletionRequest
	start         time.Time
	session       string
	taskID        string
	explorationID int64
	worker        string
	bodies        bool
	requestBody   string
	capture       *Capture
}

func (r *Recorder) observeCall(ctx context.Context, request llm.CompletionRequest) *callObservation {
	bodies := r.enabled == nil || r.enabled()
	start := time.Now()
	session := transcript.SessionIDFrom(ctx)
	parsedID, worker := parseSession(session)
	if override := workerFrom(ctx); override != "" {
		worker = override
	}
	explorationID := db.ParseExpID(parsedID)
	taskID := TaskIDFrom(ctx)
	if taskID == "" {
		taskID = parsedID
	}
	call := &callObservation{recorder: r, ctx: ctx, request: request, start: start,
		session: session, taskID: taskID, explorationID: explorationID, worker: worker, bodies: bodies}
	if bodies {
		call.requestBody = r.serializeRequest(request)
		call.ctx, call.capture = NewCapture(ctx)
	}
	return call
}

func (c *callObservation) complete() (llm.Message, string, llm.Usage, error) {
	message, stop, usage, err := c.recorder.inner.Complete(c.ctx, c.request)
	c.meter(usage, err)
	if c.bodies {
		c.record(message.Text(), thinkingText(message), usage, stop, err)
	}
	return message, stop, usage, err
}

func (c *callObservation) meter(usage llm.Usage, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	c.recorder.recordUsage(c.taskID, c.explorationID, c.worker, usage, int(time.Since(c.start).Milliseconds()), status)
}

func (c *callObservation) record(text, thinking string, usage llm.Usage, stop string, err error) {
	c.recorder.record(c.request, c.session, c.taskID, c.worker, c.requestBody, c.capture, c.start, text, thinking, usage, stop, err)
}
