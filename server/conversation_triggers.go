package server

import (
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

// The queue owner carries agent identity, not a cached policy snapshot. Each
// fire refreshes strategy before taking queueMu; completion sees the latest one.
type agentTriggerQueue struct {
	server *Server
	key    string
}

func (queue agentTriggerQueue) strategy() triggerBehavior {
	strategy := triggerBehavior{runMode: "serial", mergeMode: "all", maxParallel: 5}
	if queue.server.m.pg == nil {
		return strategy
	}
	role, err := queue.server.m.pg.GetAgentByKey(queue.key)
	if err != nil || role == nil {
		return strategy
	}
	if role.TriggerRunMode == "parallel" {
		strategy.runMode = "parallel"
	}
	switch role.TriggerMergeMode {
	case "by_task", "all", "none":
		strategy.mergeMode = role.TriggerMergeMode
	}
	strategy.maxParallel = role.TriggerMaxParallel
	return strategy
}

func (queue agentTriggerQueue) enqueue(fire triggeredRun) {
	if queue.server.m.pg == nil || queue.server.chatAgentRef() == nil {
		return
	}
	strategy := queue.server.readTriggerBehavior(queue.key)
	queue.server.queueMu.Lock()
	queue.server.triggerCfg[queue.key] = strategy
	queue.server.triggerQ[queue.key] = append(queue.server.triggerQ[queue.key], fire)
	queue.refill()
	queue.server.queueMu.Unlock()
}

// refill and take are called with queueMu held, as their stable server ports
// were. Active slots are reserved before launching a run.
func (queue agentTriggerQueue) refill() {
	if queue.server.updateBlocked() {
		return
	}
	if queue.server.ctx.Err() != nil {
		return
	}
	strategy := queue.server.triggerCfg[queue.key]
	limit := 1
	if strategy.runMode == "parallel" {
		if strategy.maxParallel <= 0 {
			limit = math.MaxInt
		} else {
			limit = strategy.maxParallel
		}
	}
	for queue.server.triggerActive[queue.key] < limit && len(queue.server.triggerQ[queue.key]) > 0 {
		fire := queue.server.nextTriggerRun(queue.key, strategy)
		queue.server.triggerActive[queue.key]++
		go queue.server.runAndPump(queue.key, fire)
	}
}

func (queue agentTriggerQueue) run(fire triggeredRun) {
	queue.server.runTriggeredRun(fire)
	queue.server.queueMu.Lock()
	queue.server.triggerActive[queue.key]--
	if queue.server.triggerActive[queue.key] <= 0 && len(queue.server.triggerQ[queue.key]) == 0 {
		delete(queue.server.triggerActive, queue.key)
		delete(queue.server.triggerQ, queue.key)
		delete(queue.server.triggerCfg, queue.key)
	} else {
		queue.server.pumpLocked(queue.key)
	}
	queue.server.queueMu.Unlock()
}

func (queue agentTriggerQueue) take(strategy triggerBehavior) triggeredRun {
	pending := queue.server.triggerQ[queue.key]
	if strategy.runMode == "parallel" || strategy.mergeMode == "none" {
		queue.server.triggerQ[queue.key] = pending[1:]
		return pending[0]
	}
	if strategy.mergeMode == "all" {
		queue.server.triggerQ[queue.key] = pending[:0:0]
		return mergeAllRuns(pending)
	}
	head := pending[0]
	if !head.mergeable || head.taskID == 0 {
		queue.server.triggerQ[queue.key] = pending[1:]
		return head
	}
	group := []triggeredRun{head}
	rest := pending[:0:0]
	for _, fire := range pending[1:] {
		if fire.mergeable && fire.taskID == head.taskID {
			group = append(group, fire)
		} else {
			rest = append(rest, fire)
		}
	}
	queue.server.triggerQ[queue.key] = rest
	return mergeTriggeredRuns(group)
}

type triggerBurst struct{ events []triggeredRun }

func triggerTaskContext(id int64, description, goal string) string {
	if description == "" && goal == "" {
		return ""
	}
	if goal != "" {
		return fmt.Sprintf("[Task #%d %s (goal: %s)]", id, trunc(description, 200), trunc(goal, 500))
	}
	return fmt.Sprintf("[Task #%d %s]", id, trunc(description, 200))
}

func triggerMessage(fire triggeredRun) string {
	if header := taskContextHeader(fire.taskID, fire.taskDesc, fire.taskGoal); header != "" {
		return header + "\n" + fire.message
	}
	return fire.message
}

func (burst triggerBurst) sameTask() triggeredRun {
	if len(burst.events) == 1 {
		return burst.events[0]
	}
	first := burst.events[0]
	var message strings.Builder
	fmt.Fprintf(&message, "[This conversation merges task #%d's %d trigger events; please handle them together]\n", first.taskID, len(burst.events))
	if header := taskContextHeader(first.taskID, first.taskDesc, first.taskGoal); header != "" {
		fmt.Fprintf(&message, "%s\n", header)
	}
	for index, fire := range burst.events {
		fmt.Fprintf(&message, "\n── Trigger %d ──\n%s\n", index+1, fire.message)
	}
	return triggeredRun{agentKey: first.agentKey, title: fmt.Sprintf("Merged triggers · task#%d · %d events", first.taskID, len(burst.events)), message: message.String(), taskID: first.taskID, mergeable: true}
}

func (burst triggerBurst) allTasks() triggeredRun {
	if len(burst.events) == 1 {
		return burst.events[0]
	}
	first := burst.events[0]
	order := []int64{}
	groups := map[int64][]triggeredRun{}
	for _, fire := range burst.events {
		if _, seen := groups[fire.taskID]; !seen {
			order = append(order, fire.taskID)
		}
		groups[fire.taskID] = append(groups[fire.taskID], fire)
	}
	var message strings.Builder
	fmt.Fprintf(&message, "[This conversation merges %d queued trigger events (across %d tasks); please handle them together]\n", len(burst.events), len(order))
	sequence := 0
	for _, id := range order {
		group := groups[id]
		if header := taskContextHeader(id, group[0].taskDesc, group[0].taskGoal); header != "" {
			fmt.Fprintf(&message, "\n%s\n", header)
		}
		for _, fire := range group {
			sequence++
			fmt.Fprintf(&message, "\n── Trigger %d (task#%d) ──\n%s\n", sequence, id, fire.message)
		}
	}
	return triggeredRun{agentKey: first.agentKey, title: fmt.Sprintf("Merged triggers · all · %d events", len(burst.events)), message: message.String(), taskID: first.taskID, mergeable: true}
}

type triggerConversation struct {
	server *Server
	fire   triggeredRun
}

func (run triggerConversation) execute() {
	defer func() {
		if failure := recover(); failure != nil {
			log.Printf("[trigger] run for %s panicked: %v", run.fire.agentKey, failure)
		}
	}()
	database := run.server.m.pg
	conversation, err := database.CreateConversation(run.fire.agentKey, firstLine(run.fire.title, 60), nil)
	if err != nil {
		log.Printf("[trigger] create conversation for %s failed: %v", run.fire.agentKey, err)
		return
	}
	message := finalTriggerMessage(run.fire)
	if _, err := database.AppendConvActivity(conversation.ID, db.Activity{Worker: run.fire.agentKey, Kind: "user", Summary: firstLine(message, 200), Detail: message}); err != nil {
		log.Printf("[trigger] append msg failed: %v", err)
	}
	key := run.server.convBusyKey(conversation.ID)
	run.server.chatMu.Lock()
	run.server.chatBusy[key] = true
	run.server.chatMu.Unlock()
	run.server.runConversationSync(conversation, message, key)
}
