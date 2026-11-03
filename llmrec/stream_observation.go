package llmrec

import (
	"strings"

	"github.com/nobanic/norma/llm"
)

// streamObservation is per iteration, not per returned sequence. Reiterating a
// sequence reuses its original Capture/identity but starts fresh accumulation.
type streamObservation struct {
	call           *callObservation
	text, thinking strings.Builder
	usage          llm.Usage
	stop           string
	finished       bool
}

func (c *callObservation) stream(yield func(llm.StreamEvent, error) bool) {
	response := streamObservation{call: c}
	defer func() { response.finish(c.ctx.Err()) }()
	for event, err := range c.recorder.inner.Stream(c.ctx, c.request) {
		if err != nil {
			// Store the failure before forwarding the original event/error pair.
			response.finish(err)
			if !yield(event, err) {
				return
			}
			return
		}
		response.include(event)
		if !yield(event, err) {
			return
		}
	}
	response.finish(nil)
}

func (s *streamObservation) include(event llm.StreamEvent) {
	switch event.Type {
	case llm.SETextDelta:
		if s.call.bodies {
			s.text.WriteString(event.Text)
		}
	case llm.SEThinkingDelta:
		if s.call.bodies {
			s.thinking.WriteString(event.Text)
		}
	case llm.SEMessageStart:
		s.usage.Add(event.Usage)
	case llm.SEMessageDelta:
		if event.StopReason != "" {
			s.stop = event.StopReason
		}
		s.usage.Add(event.Usage)
	}
}

func (s *streamObservation) finish(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.call.meter(s.usage, err)
	if s.call.bodies {
		s.call.record(s.text.String(), s.thinking.String(), s.usage, s.stop, err)
	}
}
