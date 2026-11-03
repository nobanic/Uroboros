package db

import "fmt"

// installationCatalog owns the ordered, nontransactional installation stages.
// It never replaces operator state with defaults after its original marker fires.
type installationCatalog struct{ db *DB }

type installationStage struct {
	context string
	apply   func() error
}

type installationMarker struct {
	key, value, errorContext string
	ignoreWriteError         bool
}

func (c installationCatalog) once(marker installationMarker, apply func() error) error {
	if value, _, _ := c.db.GetSetting(marker.key); value == marker.value {
		return nil
	}
	if err := apply(); err != nil {
		return err
	}
	err := c.db.SetSetting(marker.key, marker.value)
	if marker.ignoreWriteError {
		return nil
	}
	return installationError(marker.errorContext, err)
}

func installationError(context string, err error) error {
	if err != nil && context != "" {
		return fmt.Errorf("%s: %w", context, err)
	}
	return err
}

func (c installationCatalog) exec(statement string, args ...any) error {
	_, err := c.db.Exec(statement, args...)
	return err
}

func (c installationCatalog) seed() error {
	stages := []installationStage{
		{"", c.agents},
		{"cleanup renamed vars", func() error {
			// These old names must not collide with the global/runtime catalog.
			return c.exec(`DELETE FROM agent_prompt_vars WHERE var_name IN ('EngagementTitle', 'CoverageGaps', 'Now')`)
		}},
		{"", c.agentDefaults},
		{"seed web_search enabled default", func() error {
			_, err := c.db.InsertSettingIfAbsent("web_search_enabled", "true")
			return err
		}},
		{"seed Tor research enabled default", func() error {
			_, err := c.db.InsertSettingIfAbsent("tor_research_enabled", "true")
			return err
		}},
		{"seed browser mcp", func() error {
			// Browser stays disabled and retains later edits, including visibility.
			// Traffic/proxy configuration and owned child paths remain runtime policy.
			return c.exec(`
INSERT INTO mcp_servers(name, transport, command, args, env, enabled)
VALUES ('browser', 'stdio', 'npx', $1, '{}', false)
ON CONFLICT (name) DO NOTHING`, `["@playwright/mcp","--headless"]`)
		}},
		{"seed skill visibility", c.db.seedBuiltinSkillVisibility},
		{"seed intercept rules", c.db.seedDefaultInterceptRules},
		{"seed intercept rules v2", c.db.seedDefaultInterceptRulesV2},
		{"seed intercept rules v3", c.db.seedDefaultInterceptRulesV3},
		{"seed asset intercept rules", c.db.seedDefaultAssetInterceptRules},
		{"reconcile asset intercept rules v2", c.db.reconcileDefaultAssetInterceptRulesV2},
	}
	for _, stage := range stages {
		if err := installationError(stage.context, stage.apply()); err != nil {
			return err
		}
	}
	return nil
}

func (c installationCatalog) agents() error {
	for _, a := range builtinAgents {
		var agentID int64
		err := c.db.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled, web_search, interactive_shell, run_seconds)
VALUES ($1, $2, NULLIF($3,''), $4, true, true, $5, $6, COALESCE($7, 1200))
ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id`, a.key, a.name, a.desc, a.role, a.webSearch, a.interactiveShell, a.runSeconds).Scan(&agentID)
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
		for _, v := range a.vars {
			if _, err := c.db.Exec(`
INSERT INTO agent_prompt_vars(agent_id, var_name, description, example, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, var_name) DO UPDATE
  SET description = EXCLUDED.description, example = EXCLUDED.example, source = EXCLUDED.source`,
				agentID, v.name, v.desc, v.example, v.source); err != nil {
				return fmt.Errorf("agent %s var %s: %w", a.key, v.name, err)
			}
		}
	}
	return nil
}

func (c installationCatalog) agentDefaults() error {
	// The interactive marker's write error was intentionally ignored originally.
	// Research propagates its marker error; preserve this difference explicitly.
	defaults := []struct {
		marker    installationMarker
		statement string
		context   string
	}{
		{installationMarker{"interactive_shell_default_v1", "true", "", true},
			`UPDATE agents SET interactive_shell=true WHERE key IN ('planner','worker','mainagent','auto')`,
			"seed interactive_shell defaults"},
		{installationMarker{"web_search_default_on_v1", "true", "mark web_search defaults", false},
			`UPDATE agents SET web_search=true WHERE key IN ('planner','worker','mainagent','auto','pentest')`,
			"seed web_search defaults"},
	}
	for _, seed := range defaults {
		if err := c.once(seed.marker, func() error {
			return installationError(seed.context, c.exec(seed.statement))
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c installationCatalog) skills() error {
	for skillName, agentKeys := range builtinSkillVisibility {
		for _, key := range agentKeys {
			if _, err := c.db.Exec(`
INSERT INTO agent_skill_visibility(agent_id, skill_name, enabled)
SELECT id, $2, true FROM agents WHERE key=$1
ON CONFLICT (agent_id, skill_name) DO NOTHING`, key, skillName); err != nil {
				return fmt.Errorf("skill %s → agent %s: %w", skillName, key, err)
			}
		}
	}
	return nil
}
