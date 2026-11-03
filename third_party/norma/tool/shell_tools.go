package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/nobanic/norma/permission"
)

// The interactive-shell tool family (opt-in; NOT in DefaultTools). ShellSessionTools
// returns all five. They require a task Manager on the ToolContext (tc.Tasks) — that is
// where the PTY sessions live and get cleaned up.

// ShellSessionTools returns the five interactive-shell tools. Callers add them to
// Options.Tools only when interactive shell is enabled for the agent.
func ShellSessionTools() []CoreTool {
	return []CoreTool{NewShellOpen(), NewShellSend(), NewShellRead(), NewShellClose(), NewShellList()}
}

func sessTool(name, desc string, schema map[string]any, readOnly bool, run func(context.Context, json.RawMessage, *ToolContext) (Result, error)) CoreTool {
	return Build(Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return readOnly },
		Concurrent: func(json.RawMessage) bool { return false },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: run,
	})
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func strProp(d string) map[string]any  { return map[string]any{"type": "string", "description": d} }
func boolProp(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }
func intProp(d string) map[string]any  { return map[string]any{"type": "integer", "description": d} }
func arrStr(d string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": d}
}

func jsonResult(v any) (Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Errorf("marshal: " + err.Error()), nil
	}
	return Text(string(b)), nil
}

func sessManager(tc *ToolContext) (*Manager, *Result) {
	if InteractiveShellDisabled() {
		r := Errorf("interactive shell is globally disabled (AGENT_CORE_DISABLE_INTERACTIVE_SHELL)")
		return nil, &r
	}
	if tc == nil || tc.Tasks == nil {
		r := Errorf("interactive shell requires an enabled background task manager")
		return nil, &r
	}
	return tc.Tasks, nil
}

func ms(v int, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Millisecond
}

// NewShellOpen starts an interactive session.
func NewShellOpen() CoreTool {
	desc := "Start a persistent PTY session for an interactive or real-TTY program such as msfconsole, SSH, a database or language REPL, a password prompt, or an nc session. Returns session_id. Use Bash for one-shot non-interactive commands. mode: interactive waits for the first prompt or quiet period and returns startup output; hands-free (default) returns immediately for later shell_read polling; dispatch returns immediately and sends a completion notification; monitor returns immediately and reports each watch match while the session remains active for this run. prompt_regex can precisely identify completion. Always call shell_close when finished."
	schema := obj(map[string]any{
		"command":          strProp("Interactive program to run, such as 'msfconsole -q', 'ssh user@host', or 'python3'"),
		"mode":             strProp("interactive | hands-free (default) | dispatch | monitor"),
		"watch":            arrStr("Required in monitor mode: keywords or regular expressions; each match is reported once"),
		"prompt_regex":     strProp("Optional prompt regular expression used to detect command completion at the end of output"),
		"rows":             intProp("Optional PTY row count (default 24)"),
		"cols":             intProp("Optional PTY column count (default 80)"),
		"quiet_ms":         intProp("Optional quiet period in milliseconds used to detect completion (default 8000)"),
		"startup_grace_ms": intProp("Optional startup grace period before quiet detection begins (default 15000)"),
	}, "command")
	return sessTool("shell_open", desc, schema, false, func(ctx context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			Command        string   `json:"command"`
			Mode           string   `json:"mode"`
			PromptRegex    string   `json:"prompt_regex"`
			Watch          []string `json:"watch"`
			Rows           int      `json:"rows"`
			Cols           int      `json:"cols"`
			QuietMs        int      `json:"quiet_ms"`
			StartupGraceMs int      `json:"startup_grace_ms"`
		}
		_ = json.Unmarshal(in, &a)
		if a.Command == "" {
			return Errorf("command is required"), nil
		}
		mode := a.Mode
		if mode == "" {
			mode = "hands-free"
		}
		switch mode {
		case "hands-free", "interactive", "dispatch", "monitor":
		default:
			return Errorf("mode must be interactive, hands-free, dispatch, or monitor"), nil
		}
		var watch []*regexp.Regexp
		if mode == "monitor" {
			if len(a.Watch) == 0 {
				return Errorf("monitor mode requires watch keywords or regular expressions"), nil
			}
			for _, w := range a.Watch {
				re, err := regexp.Compile(w)
				if err != nil {
					return Errorf("invalid watch regular expression " + w + ": " + err.Error()), nil
				}
				watch = append(watch, re)
			}
		}
		var pr *regexp.Regexp
		if a.PromptRegex != "" {
			re, err := regexp.Compile(a.PromptRegex)
			if err != nil {
				return Errorf("invalid prompt_regex: " + err.Error()), nil
			}
			pr = re
		}
		workDir := ""
		var env []string
		if tc != nil {
			workDir, env = tc.WorkingDir, tc.Env
		}
		s, err := m.openSession(a.Command, workDir, env, a.Rows, a.Cols, mode, pr)
		if err != nil {
			return Errorf("failed to open session: " + err.Error()), nil
		}
		quiet, grace := ms(a.QuietMs, sessDefQuietMs), ms(a.StartupGraceMs, sessDefGraceMs)
		switch mode {
		case "interactive":
			by := s.waitComplete(ctx, quiet, grace, ms(0, sessWaitCapMs), pr, "")
			out, cur, _ := s.readSince(0, 0, sessDefReadByte*4, false)
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "done_by": by, "output": out, "cursor": cur, "running": s.running()})
		case "dispatch":
			go m.dispatchWaiter(s, quiet, grace, ms(0, sessWaitCapMs))
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "dispatched": true})
		case "monitor":
			go m.monitorWaiter(s, watch)
			return jsonResult(map[string]any{"session_id": s.id, "state": "running", "monitoring": len(watch)})
		default: // hands-free
			return jsonResult(map[string]any{"session_id": s.id, "state": "running"})
		}
	})
}

// NewShellSend sends input or keys to a session.
func NewShellSend() CoreTool {
	desc := "Send input to an interactive session. text, submit, keys, hex, and paste can be combined and are sent in that order. wait=true (default) waits for command completion and returns incremental output."
	schema := obj(map[string]any{
		"session_id":   strProp("Session ID returned by shell_open"),
		"text":         strProp("Text to enter without an automatic newline"),
		"submit":       boolProp("Append Enter after text"),
		"keys":         arrStr("Named keys: ctrl+c, ctrl+d, tab, enter, esc, arrows, and others"),
		"hex":          arrStr("Raw bytes in hexadecimal, for example ['0x1b','0x5b','0x41']"),
		"paste":        strProp("Multiline bracketed paste that does not execute each line separately"),
		"wait":         boolProp("Wait synchronously for command completion (default true)"),
		"prompt_regex": strProp("Optional override for the session completion prompt"),
		"quiet_ms":     intProp("Optional quiet threshold in milliseconds (default 8000)"),
		"timeout_ms":   intProp("Optional hard wait timeout in milliseconds (default 120000)"),
	}, "session_id")
	return sessTool("shell_send", desc, schema, false, func(ctx context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID   string   `json:"session_id"`
			Text        string   `json:"text"`
			Paste       string   `json:"paste"`
			PromptRegex string   `json:"prompt_regex"`
			Submit      bool     `json:"submit"`
			Keys        []string `json:"keys"`
			Hex         []string `json:"hex"`
			Wait        *bool    `json:"wait"`
			QuietMs     int      `json:"quiet_ms"`
			TimeoutMs   int      `json:"timeout_ms"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("session not found: " + a.SessionID), nil
		}
		if !s.running() {
			return Errorf("session has ended (exit " + fmtExit(s.exit()) + "); use shell_read for final output or shell_close"), nil
		}
		before := s.total
		// build & write input in order: text, submit(\r), keys, hex, paste
		var payload []byte
		if a.Text != "" {
			payload = append(payload, a.Text...)
		}
		if a.Submit {
			payload = append(payload, '\r')
		}
		for _, k := range a.Keys {
			if b, ok := keyToBytes(k); ok {
				payload = append(payload, b...)
			} else {
				return Errorf("unknown key: " + k), nil
			}
		}
		if len(a.Hex) > 0 {
			payload = append(payload, hexToBytes(a.Hex)...)
		}
		if a.Paste != "" {
			payload = append(payload, "\x1b[200~"...)
			payload = append(payload, a.Paste...)
			payload = append(payload, "\x1b[201~"...)
		}
		if len(payload) == 0 {
			return Errorf("no input supplied; provide at least one of text, submit, keys, hex, or paste"), nil
		}
		if _, err := s.ptmx.Write(payload); err != nil {
			return Errorf("write failed: " + err.Error()), nil
		}
		wait := a.Wait == nil || *a.Wait
		if !wait {
			return jsonResult(map[string]any{"session_id": s.id, "sent": len(payload), "running": s.running()})
		}
		pr := s.prompt
		if a.PromptRegex != "" {
			if re, err := regexp.Compile(a.PromptRegex); err == nil {
				pr = re
			}
		}
		by := s.waitComplete(ctx, ms(a.QuietMs, sessDefQuietMs), 0, ms(a.TimeoutMs, sessWaitCapMs), pr, "")
		out, cur, omitted := s.readSince(before, 0, sessDefReadByte, false)
		return jsonResult(map[string]any{"session_id": s.id, "output": out, "done_by": by, "cursor": cur, "running": s.running(), "omitted": omitted})
	})
}

// NewShellRead returns incremental output or a full-screen snapshot.
func NewShellRead() CoreTool {
	desc := "Read output from an interactive session without sending input. view=stream (default) returns an incremental raw stream and supports drain for all pending output; view=screen returns a full VT screen snapshot for programs such as vim or htop."
	schema := obj(map[string]any{
		"session_id":   strProp("Session ID"),
		"view":         strProp("stream (default) | screen"),
		"since_cursor": intProp("stream: read after this cursor; when omitted, continue from the session cursor"),
		"max_lines":    intProp("stream: maximum returned lines (default 200)"),
		"max_bytes":    intProp("stream: maximum returned bytes (default 8192)"),
		"drain":        boolProp("stream: return all raw output since the previous read and ignore limits"),
	}, "session_id")
	// per-session read cursor kept here (tool-side) so unspecified reads continue.
	return sessTool("shell_read", desc, schema, true, func(_ context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID   string `json:"session_id"`
			View        string `json:"view"`
			SinceCursor *int64 `json:"since_cursor"`
			MaxLines    int    `json:"max_lines"`
			MaxBytes    int    `json:"max_bytes"`
			Drain       bool   `json:"drain"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("session not found: " + a.SessionID), nil
		}
		if a.View == "screen" {
			grid, rows, cols, cx, cy := s.screen()
			return jsonResult(map[string]any{"view": "screen", "screen": grid, "rows": rows, "cols": cols,
				"cursor": map[string]int{"row": cy, "col": cx}, "running": s.running()})
		}
		var from int64
		if a.SinceCursor != nil {
			from = *a.SinceCursor
		} else {
			from = s.readCursor()
		}
		maxLines, maxBytes := a.MaxLines, a.MaxBytes
		if maxLines == 0 {
			maxLines = sessDefReadLine
		}
		if maxBytes == 0 {
			maxBytes = sessDefReadByte
		}
		out, cur, omitted := s.readSince(from, maxLines, maxBytes, a.Drain)
		s.setReadCursor(cur)
		res := map[string]any{"view": "stream", "output": out, "cursor": cur, "running": s.running(), "omitted": omitted}
		if ec := s.exit(); ec != nil {
			res["exit_code"] = *ec
		}
		return jsonResult(res)
	})
}

// NewShellClose closes a session.
func NewShellClose() CoreTool {
	schema := obj(map[string]any{"session_id": strProp("Session ID")}, "session_id")
	return sessTool("shell_close", "Close an interactive session, terminate its process, and release its PTY.", schema, false, func(_ context.Context, in json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		var a struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(in, &a)
		s, ok := m.session(a.SessionID)
		if !ok {
			return Errorf("session not found: " + a.SessionID), nil
		}
		ec := s.exit()
		s.kill()
		m.sessMu.Lock()
		delete(m.sessions, a.SessionID)
		m.sessMu.Unlock()
		return jsonResult(map[string]any{"closed": a.SessionID, "exit_code": exitOrNil(ec)})
	})
}

// NewShellList lists sessions.
func NewShellList() CoreTool {
	return sessTool("shell_list", "List all current interactive sessions and their status.", obj(map[string]any{}), true, func(_ context.Context, _ json.RawMessage, tc *ToolContext) (Result, error) {
		m, errR := sessManager(tc)
		if errR != nil {
			return *errR, nil
		}
		m.reapSessions()
		m.sessMu.Lock()
		defer m.sessMu.Unlock()
		out := make([]map[string]any, 0, len(m.sessions))
		for _, s := range m.sessions {
			s.mu.Lock()
			out = append(out, map[string]any{"session_id": s.id, "command": s.command, "state": s.state,
				"mode": s.mode, "idle_ms": time.Since(s.lastOut).Milliseconds()})
			s.mu.Unlock()
		}
		return jsonResult(map[string]any{"sessions": out})
	})
}

// --- small helpers ---

func fmtExit(ec *int) string {
	if ec == nil {
		return "?"
	}
	return fmt.Sprintf("%d", *ec)
}
func exitOrNil(ec *int) any {
	if ec == nil {
		return nil
	}
	return *ec
}
