package agent

import (
	"context"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/intercept"
	"github.com/nobanic/UroborOS/sidequestion"
	"github.com/nobanic/norma/harness"
	"github.com/nobanic/norma/llm"
)

// sessionActivity owns one prompt's observable projection. The SDK session,
// tool execution, approval policy and caller's persistence remain external.
// A reused session gets a fresh projection, audit trace and streaming buffer.
type sessionActivity struct {
	ctx       context.Context
	audit     *intercept.Trace
	emit      func(db.Activity)
	toolNames map[string]string
	lastTool  runTrace
	lastUsage *llm.Usage
	text      strings.Builder
	textKind  string
	finalText string
	reason    harness.TerminalReason
	err       error
}

func newSessionActivity(ctx context.Context, audit *intercept.Trace, emit func(db.Activity)) *sessionActivity {
	return &sessionActivity{ctx: ctx, audit: audit, emit: emit,
		toolNames: make(map[string]string), lastTool: runTrace{startedAt: time.Now()}}
}

func (p *sessionActivity) record(row db.Activity) {
	switch row.Kind {
	case "text", "tool_result":
		p.audit.Append(db.InterceptContextEntry{Kind: row.Kind, Tool: row.Tool, ToolUseID: row.ToolUseID, Text: row.Detail, IsError: row.IsError})
	}
	if p.emit != nil {
		p.emit(row)
	}
}

func (p *sessionActivity) flush() {
	if p.text.Len() == 0 {
		return
	}
	detail, kind := strings.TrimSpace(p.text.String()), p.textKind
	p.text.Reset()
	p.textKind = ""
	if detail != "" {
		p.record(db.Activity{Kind: kind, Summary: firstLine(detail, 200), Detail: detail})
	}
}

func (p *sessionActivity) delta(kind, text string) {
	if text == "" {
		return
	}
	if p.textKind != "" && p.textKind != kind {
		p.flush()
	}
	p.textKind = kind
	p.text.WriteString(text)
}

func (p *sessionActivity) observe(event harness.Event) {
	switch event.Kind {
	case harness.KindText:
		p.delta("text", event.Text)
	case harness.KindThinking:
		p.delta("thinking", event.Text)
	case harness.KindToolUse:
		if event.ToolUse != nil {
			p.toolStarted(event.ToolUse)
		}
	case harness.KindToolResult:
		if event.ToolResult != nil {
			p.toolFinished(event.ToolResult)
		}
	case harness.KindUsage:
		if event.Usage != nil {
			// Live accounting must not split the tail used for final-text de-dup.
			usage := *event.Usage
			p.lastUsage = &usage
			p.record(db.Activity{Kind: "usage", InputTokens: &usage.InputTokens, OutputTokens: &usage.OutputTokens,
				CacheReadTokens: &usage.CacheReadTokens, CacheWriteTokens: &usage.CacheWriteTokens})
		}
	case harness.KindResult:
		if event.Terminal != nil {
			p.terminal(event.Terminal)
		}
	}
}

func (p *sessionActivity) toolStarted(call *llm.ContentBlock) {
	p.flush()
	p.toolNames[call.ID] = call.Name
	auditInput := redactToolAuditInput(call.Name, call.Input)
	detail := string(auditInput)
	p.lastTool.start(call.ID, call.Name, detail)
	p.audit.Start(call.ID, call.Name, auditInput)
	p.record(db.Activity{Kind: "tool_use", Tool: call.Name, ToolUseID: call.ID,
		Summary: call.Name + " " + firstLine(detail, 200), Detail: detail})
}

func (p *sessionActivity) toolFinished(result *llm.ContentBlock) {
	p.flush()
	detail := blocksText(result.Content)
	p.lastTool.done(result.ToolUseID)
	p.audit.Complete(result.ToolUseID, detail, result.IsError)
	p.record(db.Activity{Kind: "tool_result", Tool: p.toolNames[result.ToolUseID], ToolUseID: result.ToolUseID,
		IsError: result.IsError, Summary: firstLine(detail, 200), Detail: detail})
}

func (p *sessionActivity) terminal(term *harness.Terminal) {
	if term.Reason != harness.ReasonAbortedStreaming {
		sidequestion.Finish(p.ctx, term.Messages)
	}
	p.finalText, p.reason = term.Text, term.Reason
	if p.textKind == "text" && strings.TrimSpace(p.text.String()) == strings.TrimSpace(term.Text) {
		p.text.Reset()
		p.textKind = ""
	}
	p.flush()
	summary, detail := term.Text, term.Text
	if summary == "" || term.Reason == harness.ReasonAbortedTools || term.Reason == harness.ReasonAbortedStreaming {
		summary, detail = terminalText(p.ctx, term, &p.lastTool)
	}
	usage := term.Usage
	p.record(db.Activity{Kind: "result", IsError: term.Err != nil, Summary: firstLine(summary, 400), Detail: detail,
		InputTokens: &usage.InputTokens, OutputTokens: &usage.OutputTokens,
		CacheReadTokens: &usage.CacheReadTokens, CacheWriteTokens: &usage.CacheWriteTokens})
	if term.Err != nil {
		p.err = term.Err
	}
}

func (p *sessionActivity) failed(err error) (string, harness.TerminalReason, error) {
	p.flush()
	if p.ctx.Err() != nil {
		summary, detail := terminalText(p.ctx, &harness.Terminal{Reason: p.reason, Err: p.ctx.Err()}, &p.lastTool)
		p.record(activityWithUsage(db.Activity{Kind: "result", Summary: firstLine(summary, 400), Detail: detail}, p.lastUsage))
		return p.finalText, p.reason, p.ctx.Err()
	}
	row := db.Activity{Kind: "result", IsError: true, Summary: "Execution error: " + err.Error(), Detail: err.Error()}
	p.record(activityWithUsage(row, p.lastUsage))
	return p.finalText, p.reason, err
}

func (p *sessionActivity) finish() (string, harness.TerminalReason, error) {
	p.flush()
	return p.finalText, p.reason, p.err
}
