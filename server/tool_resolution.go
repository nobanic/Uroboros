package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

type toolResolution struct {
	database   *db.DB
	domain     map[string]actool.CoreTool
	review     discoveryReviewChecker
	provenance *sourceProvenanceGovernor
	bash       bool
}

func (catalog *toolResolution) decorate(tool actool.CoreTool, row *db.Tool, role string, run agent.RunInfo) actool.CoreTool {
	var schema map[string]any
	if len(row.Schema) > 0 {
		_ = json.Unmarshal(row.Schema, &schema)
	}
	return meterTool(agent.DecorateTool(tool, row.Description, schema), catalog.database, row.Key, role, run)
}

func (catalog *toolResolution) guard(tools []actool.CoreTool, role string, run agent.RunInfo) []actool.CoreTool {
	for index, tool := range tools {
		tool = guardDiscoveryWorkflowTool(tool, catalog.review, role, run)
		tools[index] = guardTaskTool(tool, catalog.database, run)
	}
	return tools
}

func (catalog *toolResolution) resolve(ctx context.Context, role string, incoming []actool.CoreTool) []actool.CoreTool {
	run := agent.RunInfoFrom(ctx)
	rows, err := catalog.database.ListTools()
	if err != nil {
		log.Printf("[tools] read tools table failed, allowing by code defaults: %v", err)
		fallback := make([]actool.CoreTool, len(incoming))
		copy(fallback, incoming)
		return catalog.guard(fallback, role, run)
	}
	byName := make(map[string]*db.Tool, len(rows))
	for _, row := range rows {
		byName[row.Key] = row
	}
	selected := incoming[:0:0]
	for _, candidate := range incoming {
		candidate = catalog.provenance.wrap(candidate, role, run)
		if candidate.Name() == "Bash" && !catalog.bash {
			continue
		}
		row, known := byName[candidate.Name()]
		if !known {
			selected = append(selected, candidate)
		} else if row.Enabled && contains(row.Agents, role) {
			selected = append(selected, catalog.decorate(candidate, row, role, run))
		}
	}
	selected = catalog.inject(selected, incoming, rows, role, run)
	selected = catalog.shellTools(selected, rows, role)
	return catalog.guard(selected, role, run)
}

func (catalog *toolResolution) inject(selected, incoming []actool.CoreTool, rows []*db.Tool, role string, run agent.RunInfo) []actool.CoreTool {
	if len(catalog.domain) == 0 {
		return selected
	}
	// Presence belongs to the original per-task input, before filtering or
	// decoration. A server registry must never shadow its task-owned instance.
	present := make(map[string]bool, len(incoming))
	for _, tool := range incoming {
		present[tool.Name()] = true
	}
	for _, row := range rows {
		if row.Kind == "shell" || !row.Enabled || !contains(row.Agents, role) || present[row.Key] {
			continue
		}
		if tool, found := catalog.domain[row.Key]; found {
			selected = append(selected, catalog.decorate(tool, row, role, run))
		}
	}
	return selected
}

const assembledInteractiveShellNote = "\n\nPrograms that need [interactive input] (msfconsole / interactive ssh login / REPLs such as mysql, psql, python / password or yes/no prompts / nc reverse shell) must not use Bash (it has no stdin and will hang); use shell_open to start an interactive session instead (shell_close when done). One-shot, non-interactive commands still use Bash."

func appendAssembledBashNote(tools []actool.CoreTool, note string) {
	for index, tool := range tools {
		if tool.Name() == "Bash" {
			tools[index] = agent.DecorateTool(tool, tool.Description()+note, tool.InputSchema())
			return
		}
	}
}

func (catalog *toolResolution) shellTools(tools []actool.CoreTool, rows []*db.Tool, role string) []actool.CoreTool {
	var hints []string
	for _, row := range rows {
		if row.Kind == "shell" && row.Enabled && contains(row.Agents, role) {
			hints = append(hints, "- "+row.Key+": "+row.Description)
		}
	}
	if catalog.bash && len(hints) > 0 {
		appendAssembledBashNote(tools, "\n\nThe following tools are installed in this bash environment and can be called directly via Bash:\n"+strings.Join(hints, "\n"))
	}
	if catalog.bash && !actool.InteractiveShellDisabled() {
		if actor, err := catalog.database.GetAgentByKey(role); err == nil && actor != nil && actor.InteractiveShell {
			tools = append(tools, actool.ShellSessionTools()...)
			appendAssembledBashNote(tools, assembledInteractiveShellNote)
		}
	}
	return tools
}
