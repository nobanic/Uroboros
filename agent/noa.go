package agent

import (
	"log"
	"path/filepath"

	"github.com/nobanic/norma/agentcore"
	"github.com/nobanic/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the "model-driven context compaction" mechanism introduced in norma
// v0.4.0, exposed as an experimental platform feature the user toggles in system
// settings. It is mutually exclusive with the built-in compaction: noaadapter.Enable
// is the only entry point — one call mounts the context takeover hook (Compactor),
// the Compress tool, and three persistent prompts; not calling Enable means it is
// off (built-in compaction keeps working). The toggle is resolved by the
// noaEnabledFn each agent injects, read once per run, so flipping it only affects
// runs started afterwards, with no need to rebuild the agent.

// enableNoa wires noa into opts when the resolver reports it is on. archiveRoot is
// the persistent base dir for the compacted source text (the global workDir, so
// every agent lands under <workDir>/noa rather than being scattered across
// task/intent dirs); sessionID names the archive sub-dir beneath it (globally
// unique, so there is no collision within the same base dir).
//
// noa is experimental: a wiring failure must not interrupt the real task. On error
// it is reported via onWarn and we fall back to the built-in compaction. On success
// we clear opts.Compaction to avoid agentcore warning that two context managers are
// set at once.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa compaction failed to enable, falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but when both are set agentcore warns every time; clear it explicitly.
	opts.Compaction = nil
}
