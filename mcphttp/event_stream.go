package mcphttp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type rpcEventAssembly struct {
	wanted int
	data   strings.Builder
}

func (event *rpcEventAssembly) flush() *rpcResponse {
	if event.data.Len() == 0 {
		return nil
	}
	payload := event.data.String()
	event.data.Reset()
	var reply rpcResponse
	if json.Unmarshal([]byte(payload), &reply) != nil || reply.ID != event.wanted {
		return nil
	}
	return &reply
}
func (event *rpcEventAssembly) line(line string) *rpcResponse {
	if line == "" {
		return event.flush()
	}
	if strings.HasPrefix(line, "data:") {
		event.data.WriteString(strings.TrimSpace(line[len("data:"):]))
	}
	return nil
}
func readRPCEventStream(reader io.Reader, wanted int) (*rpcResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	event := rpcEventAssembly{wanted: wanted}
	for scanner.Scan() {
		if reply := event.line(scanner.Text()); reply != nil {
			return reply, nil
		}
	}
	// Preserve buffered-response precedence over a terminal reader/scan error.
	if reply := event.flush(); reply != nil {
		return reply, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("mcp: no matching response in event stream")
}
