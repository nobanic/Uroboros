package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nobanic/UroborOS/db"
)

// One request borrows its original input, metadata and audit. It owns only the
// pending-channel registration and optional timer, never a worker or tool run.
type approvalRequest struct {
	owner             *Interceptor
	ctx               context.Context
	conversation      int64
	task, agent, tool string
	input             []byte
	decision          Decision
	audit             *db.InterceptAudit
	emit              func(db.Activity)
	id                int64
}

func (r *approvalRequest) log(status string) {
	id, err := r.owner.db.CreateDecidedIntercept(r.decision.RuleID, r.conversation, r.task, r.agent, r.tool, r.input, status, r.decision.Message, r.audit)
	if err == nil {
		r.owner.bindResult(r.ctx, id, r.audit)
	}
}

func approvedRecord(detail *db.InterceptDetail) bool {
	return detail != nil && (detail.Status == "allowed" || (detail.Audit != nil && detail.Audit.EffectiveAction == "allow"))
}

func (r *approvalRequest) run() bool {
	id, err := r.owner.db.CreateInterceptPending(r.decision.RuleID, r.conversation, r.task, r.agent, r.tool, r.input, r.decision.Message, r.audit)
	if err != nil {
		return false
	}
	r.id = id
	r.owner.bindResult(r.ctx, id, r.audit)
	answer := r.owner.pending.add(id)
	defer r.owner.pending.remove(id)
	// Registration follows persistence; a polling client may already have decided.
	if saved, err := r.owner.db.GetInterceptDetail(id); err == nil && saved != nil && saved.Status != "pending" {
		return approvedRecord(saved)
	}
	r.announce()
	var expiry <-chan time.Time
	if r.decision.TimeoutEnabled {
		seconds := r.decision.TimeoutSeconds
		if seconds <= 0 {
			seconds = 60
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		expiry = timer.C
	}
	select {
	case allowed := <-answer:
		return allowed
	case <-expiry:
		return r.timeout()
	case <-r.ctx.Done():
		r.cancel()
		return false
	}
}

func (r *approvalRequest) announce() {
	detail, _ := json.Marshal(map[string]any{"pending_id": r.id, "tool": r.tool, "input": json.RawMessage(r.input)})
	activity := db.Activity{
		Kind: "intercept_request", Summary: fmt.Sprintf("Tool %s requests approval (#%d)", r.tool, r.id), Detail: string(detail),
	}
	if r.conversation != 0 {
		_, _ = r.owner.db.AppendConvActivity(r.conversation, activity)
	} else if r.emit != nil {
		r.emit(activity)
	}
}

func (r *approvalRequest) cancel() {
	_, _ = r.owner.db.ResolveIntercept(r.id, "denied", "deny", "work canceled")
	_ = r.owner.db.CompleteIntercept(r.id, r.audit.RunID, r.audit.ToolUseID, "not_executed", "work canceled before execution", false)
}

func (r *approvalRequest) timeout() bool {
	allowed := r.decision.TimeoutAction == "allow"
	action := "deny"
	if allowed {
		action = "allow"
	}
	resolved, err := r.owner.db.ResolveIntercept(r.id, "timeout", action, "approval timed out; handling per the timeout policy")
	if err != nil {
		return false
	}
	if resolved {
		return allowed
	}
	detail, err := r.owner.db.GetInterceptDetail(r.id)
	return err == nil && approvedRecord(detail)
}

func (i *Interceptor) resolveHuman(id int64, allowed bool) error {
	status, action, reason := "denied", "deny", "denied by a human"
	if allowed {
		status, action, reason = "allowed", "allow", "allowed by a human"
	}
	resolved, err := i.db.ResolveIntercept(id, status, action, reason)
	if err != nil {
		return err
	}
	if !resolved {
		return ErrAlreadyDecided
	}
	i.pending.resolve(id, allowed)
	return nil
}
