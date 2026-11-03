package server

import (
	"context"
	"errors"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/sidequestion"
	"log"
	"os"
	"strconv"
	"time"
)

// Lifecycle operations retain dynamic Server.side and Server.ctx lookups; a
// publisher or checkpoint worker must never capture an obsolete state pointer.
type sideQuestionLifecycle struct{ server *Server }

func (lifecycle sideQuestionLifecycle) initialize() {
	lifecycle.server.side = &sideQuestionState{pending: map[string]sidequestion.Snapshot{}, latest: map[string]sidequestion.Snapshot{}, seen: map[string][2]int64{}, runs: map[string]sideRun{}, done: make(chan struct{})}
	if n, err := strconv.Atoi(os.Getenv("UROBOROS_BTW_MAX_OUTPUT_TOKENS")); err == nil && n >= 256 && n <= 32768 {
		lifecycle.server.side.outputTokens = n
	}
	if lifecycle.server.m.pg == nil {
		close(lifecycle.server.side.done)
		return
	}
	if err := lifecycle.server.m.pg.InterruptSideRequests(lifecycle.server.ctx); err != nil {
		log.Printf("[btw] recover: %v", err)
	}
	lifecycle.server.ctx = sidequestion.WithPublisher(lifecycle.server.ctx, lifecycle.publish)
	go lifecycle.run()
}
func (lifecycle sideQuestionLifecycle) publish(snap sidequestion.Snapshot) {
	lifecycle.server.side.mu.Lock()
	defer lifecycle.server.side.mu.Unlock()
	key := snap.Parent.Key()
	old, ok := lifecycle.server.side.seen[key]
	if ok && (old[0] > snap.RunID || old[0] == snap.RunID && old[1] >= snap.Version) {
		return
	}
	lifecycle.server.side.latest[key] = snap
	lifecycle.server.side.seen[key] = [2]int64{snap.RunID, snap.Version}
	lifecycle.server.side.pending[key] = snap
}
func (lifecycle sideQuestionLifecycle) run() {
	defer close(lifecycle.server.side.done)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			lifecycle.server.flushSideSnapshots()
		case <-lifecycle.server.ctx.Done():
			lifecycle.server.flushSideSnapshots()
			return
		}
	}
}
func (lifecycle sideQuestionLifecycle) flush() {
	if lifecycle.server.side == nil || lifecycle.server.m.pg == nil {
		return
	}
	lifecycle.server.side.commands.Lock()
	defer lifecycle.server.side.commands.Unlock()
	lifecycle.server.side.mu.Lock()
	pending := lifecycle.server.side.pending
	lifecycle.server.side.pending = map[string]sidequestion.Snapshot{}
	lifecycle.server.side.mu.Unlock()
	for key, snap := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := lifecycle.server.m.pg.SaveSideSnapshot(ctx, snap)
		cancel()
		lifecycle.server.side.mu.Lock()
		if err == nil || errors.Is(err, db.ErrSideParentGone) {
			if cur, ok := lifecycle.server.side.latest[key]; ok && cur.RunID == snap.RunID && cur.Version == snap.Version {
				delete(lifecycle.server.side.latest, key)
			}
		} else if newer, ok := lifecycle.server.side.pending[key]; !ok || newer.RunID < snap.RunID || newer.RunID == snap.RunID && newer.Version < snap.Version {
			lifecycle.server.side.pending[key] = snap
		}
		lifecycle.server.side.mu.Unlock()
		if err != nil && !errors.Is(err, db.ErrSideParentGone) {
			log.Printf("[btw] checkpoint persistence: %v", err)
		}
	}
}
func (lifecycle sideQuestionLifecycle) snapshot(ctx context.Context, p sidequestion.Parent) (*sidequestion.Snapshot, error) {
	lifecycle.server.side.mu.Lock()
	snap, ok := lifecycle.server.side.latest[p.Key()]
	lifecycle.server.side.mu.Unlock()
	if ok {
		return &snap, nil
	}
	return lifecycle.server.m.pg.SideSnapshot(ctx, p.Key())
}
func (lifecycle sideQuestionLifecycle) cancel(match func(sidequestion.Parent) bool) []<-chan struct{} {
	if lifecycle.server.side == nil {
		return nil
	}
	lifecycle.server.side.mu.Lock()
	defer lifecycle.server.side.mu.Unlock()
	var done []<-chan struct{}
	for _, run := range lifecycle.server.side.runs {
		if match(run.parent) {
			run.cancel()
			done = append(done, run.done)
		}
	}
	return done
}
func (lifecycle sideQuestionLifecycle) drainTask(ctx context.Context, taskID string) error {
	if lifecycle.server.side == nil {
		return nil
	}
	id, _ := strconv.ParseInt(taskID, 10, 64)
	lifecycle.server.side.commands.Lock()
	done := lifecycle.server.cancelSideWhere(func(p sidequestion.Parent) bool { return p.TaskID == id })
	lifecycle.server.side.commands.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	lifecycle.server.flushSideSnapshots()
	// A failed flush must not silently leave the latest checkpoint out of archive.
	lifecycle.server.side.mu.Lock()
	defer lifecycle.server.side.mu.Unlock()
	for _, snap := range lifecycle.server.side.pending {
		if snap.Parent.TaskID == id {
			return errors.New("the side-channel context has not been saved yet; please retry")
		}
	}
	return nil
}
func (lifecycle sideQuestionLifecycle) cancelWorker(taskID string, expID, iid int64) {
	if lifecycle.server.side == nil {
		return
	}
	id, _ := strconv.ParseInt(taskID, 10, 64)
	p := sidequestion.Parent{TaskID: id, ExplorationID: expID, IntentID: iid}
	lifecycle.server.side.commands.Lock()
	defer lifecycle.server.side.commands.Unlock()
	lifecycle.server.cancelSideWhere(func(parent sidequestion.Parent) bool { return parent == p })
}
