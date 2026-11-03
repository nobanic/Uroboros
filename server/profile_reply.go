package server

import "github.com/nobanic/UroborOS/agent"

// profileReply captures immutable reply choices for each constructed runner.
type profileReply struct{ configuration agent.Config }

func (reply profileReply) nonStreaming() func() bool {
	nonStreaming := !reply.configuration.Stream
	return func() bool { return nonStreaming }
}
func (reply profileReply) maxTokens() func() int {
	maxTokens := reply.configuration.MaxTokens
	return func() int { return maxTokens }
}
