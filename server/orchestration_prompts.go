package server

import "github.com/nobanic/UroborOS/agent"

// orchestrationPrompt shares four one-time migrations with fixed private keys
// and literal logging callbacks. Old prompt versions and attempt flags survive.
type orchestrationPrompt struct {
	server    *Server
	flag, key string
	failure   func(error)
	success   func()
}

func (prompt orchestrationPrompt) refresh() {
	(orchestrationSeed{server: prompt.server}).once(prompt.flag, func() {
		defer func() { _ = prompt.server.m.pg.SetSetting(prompt.flag, "true") }() // try only once, regardless of success
		record, err := prompt.server.m.pg.GetAgentByKey(prompt.key)
		if err != nil || record == nil {
			return // fresh DBs: before the agent row exists, seedPrompts seeds the latest default directly, so this migration is unneeded
		}
		template := agent.BuiltinPromptSeeds()[prompt.key]
		if template == "" {
			return
		}
		// fresh DB: seedPrompts already seeded the latest default -> the current version already equals the code default, no need to append a duplicate version.
		if cur, err := prompt.server.m.pg.CurrentPrompt(record.ID); err == nil && cur == template {
			return
		}
		if _, err := prompt.server.m.pg.ResetPromptToDefault(record.ID, template); err != nil {
			prompt.failure(err)
			return
		}
		prompt.success()
	})
}
