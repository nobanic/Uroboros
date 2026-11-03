package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/llmrec"
	"github.com/nobanic/UroborOS/sidequestion"
	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/transcript"
)

type sideProviderSelection struct {
	server *Server
	model  sidequestion.Model
}
type sideQuestionJob struct {
	server   *Server
	ctx      context.Context
	cancel   context.CancelFunc
	exchange sidequestion.Exchange
	parent   sidequestion.Parent
	provider llm.Provider
	snapshot sidequestion.Snapshot
	question string
}

func (selection sideProviderSelection) resolve() (llm.Provider, error) {
	var p llm.Provider
	var cfg agent.Config
	var ok bool
	if selection.model.ProfileID > 0 {
		// Validate the persisted reference even if a previous provider is cached.
		current, exists := selection.server.loadProfileConfig(selection.model.ProfileID)
		if !exists || sideModel(current, selection.model.ProfileID, selection.model.Name).Identity != selection.model.Identity {
			return nil, errors.New("the model configuration was deleted or changed; please run the Main Agent first to update the context")
		}
		p, cfg, ok = selection.server.providerForProfile(selection.model.ProfileID)
	} else {
		selection.server.cfgMu.Lock()
		p, cfg, ok = selection.server.llmDirect, selection.server.llmCfg, selection.server.llmOn
		selection.server.cfgMu.Unlock()
	}
	if !ok || p == nil || sideModel(cfg, selection.model.ProfileID, selection.model.Name).Identity != selection.model.Identity {
		return nil, errors.New("the model configuration was deleted or changed; please run the Main Agent first to update the context")
	}
	return p, nil
}
func (job *sideQuestionJob) execute() {
	defer job.cancel()
	defer func() {
		job.server.side.mu.Lock()
		if run, ok := job.server.side.runs[job.exchange.ID]; ok {
			close(run.done)
		}
		delete(job.server.side.runs, job.exchange.ID)
		job.server.side.mu.Unlock()
	}()
	job.ctx = transcript.WithSessionID(job.ctx, fmt.Sprintf("exp%d-btw-%s", job.parent.ExplorationID, job.exchange.ID))
	if job.parent.TaskID > 0 {
		job.ctx = llmrec.WithTaskID(job.ctx, strconv.FormatInt(job.parent.TaskID, 10))
	}
	// Deletions can race between request insertion and registration. Observe the
	// persisted lifecycle even while a provider is blocked without emitting text.
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-job.ctx.Done():
				return
			case <-tick.C:
				row, err := job.server.m.pg.SideRequest(job.ctx, job.exchange.ID)
				if err == nil && (row == nil || !row.Running()) {
					job.cancel()
					return
				}
			}
		}
	}()
	lastSave := time.Now()
	persist := func() (bool, error) {
		job.exchange.Sequence++
		writeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		ok, err := job.server.m.pg.UpdateSideRequest(writeCtx, job.exchange)
		if err != nil {
			log.Printf("[btw] answer persistence: %v", err)
			return true, err
		}
		return ok, nil
	}
	memory, runErr := job.server.m.pg.SideMemory(job.ctx, job.exchange)
	var answer sidequestion.Answer
	if runErr == nil {
		answer, job.exchange.Context, runErr = (sidequestion.SideQuestionService{Provider: job.provider}).Respond(job.ctx, job.snapshot, job.question, sidequestion.Replay{
			Memory: memory,
			Load: func(ctx context.Context, after int64) ([]sidequestion.Exchange, error) {
				return job.server.m.pg.SideReplayPage(ctx, job.exchange, after)
			},
			Save: func(ctx context.Context, memory sidequestion.Memory) error {
				return job.server.m.pg.SaveSideMemory(ctx, job.exchange, memory)
			},
		}, sidequestion.ContextOptions{OutputTokens: job.server.side.outputTokens}, func(a sidequestion.Answer, info sidequestion.ContextInfo) {
			phaseChanged := job.exchange.Context.Phase != info.Phase
			job.exchange.Answer, job.exchange.Usage, job.exchange.Context = a.Text, a.Usage, info
			if phaseChanged || time.Since(lastSave) >= 250*time.Millisecond {
				lastSave = time.Now()
				if ok, _ := persist(); !ok {
					job.cancel()
				}
			}
		})
	}
	job.exchange.Answer, job.exchange.Usage = answer.Text, answer.Usage
	job.exchange.Status = "completed"
	if job.ctx.Err() != nil {
		job.exchange.Status = "cancelled"
		job.exchange.Error = "Answer stopped"
		if errors.Is(job.ctx.Err(), context.DeadlineExceeded) {
			job.exchange.Status = "failed"
			job.exchange.Error = "the side-channel answer exceeded 120 seconds and was stopped"
		}
	} else if runErr != nil {
		job.exchange.Status = "failed"
		job.exchange.Error = runErr.Error()
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := persist(); err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}
