package server

import (
	"context"
	"log"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/skill"
	actool "github.com/nobanic/norma/tool"
)

func (session *augmentationSession) connectServers(ctx context.Context, key string) {
	gated := make(map[string]bool)
	if session.registry != nil {
		for _, entry := range session.registry.List() {
			for _, name := range entry.MCPs {
				gated[name] = true
			}
		}
	}
	database := session.catalog.database
	ids, _ := database.AgentVisible(session.actor.ID, "mcp")
	visible := idSet(ids)
	overrides, policyErr := database.AgentMCPToolOverrides(session.actor.ID)
	if policyErr != nil {
		log.Printf("[mcp] %s tool policy unavailable; omitting MCP tools: %v", key, policyErr)
	}
	servers, _ := database.ListMCP()
	for _, server := range servers {
		if !server.Enabled || policyErr != nil || backendOnlyMCP(server.Name) {
			continue
		}
		direct, locked := visible[server.ID], gated[server.Name]
		if direct || locked {
			session.connectServer(ctx, server, direct && !locked, overrides[server.ID])
		}
	}
}

func (session *augmentationSession) connectServer(ctx context.Context, server *db.MCPServer, global bool, overrides map[string]bool) {
	client, err := connectMCP(ctx, server)
	if err != nil {
		log.Printf("[mcp] %s connect failed: %v", server.Name, err)
		return
	}
	session.closers = append(session.closers, client)
	tools, err := client.Tools(ctx)
	if err != nil {
		log.Printf("[mcp] %s tools/list failed: %v", server.Name, err)
		return
	}
	for _, tool := range tools {
		if !mcpToolAllowed(server.Name, tool.Name(), overrides) {
			continue
		}
		session.extra = append(session.extra, tool)
		session.allNames = append(session.allNames, tool.Name())
		session.byServer[server.Name] = append(session.byServer[server.Name], tool.Name())
		if global {
			session.global = append(session.global, tool.Name())
			session.globalSet[tool.Name()] = true
		}
	}
}

func (session *augmentationSession) unlockSkill(name string) {
	if session.registry != nil {
		if entry, found := session.registry.Get(name); found {
			for _, server := range entry.MCPs {
				session.unlock.Add(session.byServer[server]...)
			}
		}
	}
}

func (session *augmentationSession) attachSkill(ctx context.Context) {
	if session.registry == nil {
		return
	}
	session.registry.OnInvoke = func(entry skill.Skill) string {
		session.unlockSkill(entry.Name)
		var reveal []string
		for _, server := range entry.MCPs {
			for _, name := range session.byServer[server] {
				if !session.globalSet[name] {
					reveal = append(reveal, name)
				}
			}
		}
		return actool.RenderDeferredToolsBlock(reveal)
	}
	session.extra = append(session.extra, meterSkillTool(session.registry.Tool(), session.catalog.database, session.registry, session.actor.Key, agent.RunInfoFrom(ctx)))
}
