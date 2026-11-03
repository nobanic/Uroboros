package server

import "net/http"

const maxWorkerMessageBytes = 64 << 10

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage continues a paused Worker intent with a human-authored message.
// The message is injected as the next turn's input through the same
// resume-from-transcript path the worker uses on a normal resume (ExecuteWithMessage),
// so this reuses the existing pause/resume machinery rather than a bespoke protocol.
// The run happens in a dedicated goroutine outside the worker pool (runDetachedIntent),
// so the message is picked up immediately even when every pool slot is busy — the same
// way the main-agent chat handler starts its run directly. In-memory only: a process
// restart re-runs the intent from its transcript without the message, which is
// acceptable for this rare interrupt-then-continue action.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	s.taskCommand(w, r).workerMessage()
}
