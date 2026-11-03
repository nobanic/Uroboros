package server

import (
	"context"
	"io"
	"path/filepath"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/skill"
	actool "github.com/nobanic/norma/tool"
)

type agentAugmentation struct {
	database *db.DB
	skillDir string
	host     func() ([]actool.CoreTool, map[string][]string)
}

type augmentationSession struct {
	catalog   *agentAugmentation
	actor     *db.Agent
	registry  *skill.Registry
	extra     []actool.CoreTool
	closers   []io.Closer
	byServer  map[string][]string
	allNames  []string
	global    []string
	globalSet map[string]bool
	unlock    *actool.UnlockSet
}

func installAgentAugmentation(database *db.DB, skillDir string, host func() ([]actool.CoreTool, map[string][]string)) {
	catalog := &agentAugmentation{database: database, skillDir: skillDir, host: host}
	agent.ToolAugment = catalog.assemble
}

func (catalog *agentAugmentation) assemble(ctx context.Context, key string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
	actor, err := catalog.database.GetAgentByKey(key)
	if err != nil || actor == nil {
		return nil, agent.DeferredInfo{}, nil
	}
	session := &augmentationSession{
		catalog: catalog, actor: actor, registry: catalog.skills(actor.ID),
		byServer: make(map[string][]string), globalSet: make(map[string]bool),
	}
	session.connectServers(ctx, key)
	session.unlock = actool.NewUnlockSet(session.global...)
	session.attachSkill(ctx)
	session.attachHost()
	return session.extra, agent.DeferredInfo{
		Deferred: session.allNames, GlobalNames: session.global,
		Unlock: session.unlock, UnlockSkill: session.unlockSkill,
	}, session.close
}

func (catalog *agentAugmentation) skills(actorID int64) *skill.Registry {
	names, _ := catalog.database.AgentSkillNames(actorID)
	if len(names) == 0 {
		return nil
	}
	visible := make(map[string]bool, len(names))
	for _, name := range names {
		visible[name] = true
	}
	all, err := skill.LoadDir(catalog.skillDir)
	if err != nil || all == nil {
		return nil
	}
	selected := skill.NewRegistry()
	for _, entry := range all.List() {
		if entry.Dir != "" && visible[filepath.Base(entry.Dir)] {
			selected.Add(entry)
		}
	}
	if len(selected.List()) == 0 {
		return nil
	}
	return selected
}

func (session *augmentationSession) attachHost() {
	if session.catalog.host == nil {
		return
	}
	tools, deferred := session.catalog.host()
	for index, tool := range tools {
		switch tool.Name() {
		case "bind_finding_experiment", "get_finding_experiments", "replay_finding_experiment", "record_api_lab_retest_result", "api_schema_from_traffic":
			tools[index] = &experimentAgentTool{CoreTool: tool, key: session.actor.Key}
		}
	}
	session.extra = append(session.extra, tools...)
	for name, roles := range deferred {
		if !contains(roles, session.actor.Key) {
			continue
		}
		session.allNames = append(session.allNames, name)
		session.global = append(session.global, name)
		session.globalSet[name] = true
		if session.unlock != nil {
			session.unlock.Add(name)
		}
	}
}

func (session *augmentationSession) close() {
	for _, client := range session.closers {
		_ = client.Close()
	}
}
